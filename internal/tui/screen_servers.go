package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Muster-dev/muster-tui/internal/fleet"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

type testResult struct {
	ok      bool
	latency time.Duration
	err     string
	running bool
}

// serversScreen: fleet machines and groups, cloud login, and actions.
type serversScreen struct {
	env     *Env
	sel     int
	results map[string]*testResult
	tasks   map[int]string
	adding  *form
	addErr  string
	addTask int
	want    string
	// fleet status --json results (U), by machine name
	health    map[string]fleet.MachineStatus
	healthID  int
	healthErr string
}

type fleetStatusMsg struct {
	st  []fleet.MachineStatus
	err error
}

type sshTestMsg struct {
	name    string
	latency time.Duration
	err     error
}

func newServers(env *Env) *serversScreen {
	return &serversScreen{env: env, results: map[string]*testResult{}, tasks: map[int]string{}, health: map[string]fleet.MachineStatus{}}
}

func (s *serversScreen) Key() string     { return "servers" }
func (s *serversScreen) Title() string   { return "Servers" }
func (s *serversScreen) Capturing() bool { return s.adding != nil }
func (s *serversScreen) Close()          { s.env.Tasks.CancelOwner(s.Key()) }
func (s *serversScreen) Init() tea.Cmd   { return refresh("fleet") }

func (s *serversScreen) selectMachine(name string) { s.want = name }

func (s *serversScreen) startAdd(args []string) {
	name, target := "", ""
	if len(args) > 0 {
		name = args[0]
	}
	if len(args) > 1 {
		target = args[1]
	}
	s.adding = newForm([]field{
		{key: "name", label: "Name", kind: fText, value: name, help: "e.g. prod-1"},
		{key: "target", label: "user@host", kind: fText, value: target, help: "e.g. deploy@10.0.0.5"},
		{key: "port", label: "SSH port", kind: fInt, value: "22"},
		{key: "mode", label: "Deploy mode", kind: fEnum, opts: []string{"push", "muster"}, value: "push", help: "push = pipe hooks over SSH · muster = remote runs muster"},
		{key: "transport", label: "Transport", kind: fEnum, opts: []string{"ssh", "cloud"}, value: "ssh"},
		{key: "path", label: "Remote project dir", kind: fText, help: "optional"},
		{key: "key", label: "SSH key file", kind: fText, help: "optional, e.g. ~/.ssh/id_ed25519"},
	})
	s.addErr = ""
}

type item struct {
	machine *fleet.Machine
	group   *fleet.Group
}

func (s *serversScreen) items() []item {
	inv := s.env.State.Fleet
	var out []item
	for i := range inv.Machines {
		out = append(out, item{machine: &inv.Machines[i]})
	}
	for i := range inv.Groups {
		out = append(out, item{group: &inv.Groups[i]})
	}
	return out
}

func (s *serversScreen) test(m fleet.Machine) tea.Cmd {
	s.results[m.Name] = &testResult{running: true}
	id, cmd := s.env.Tasks.Run("testing "+m.Name, s.Key(), false, func(ctx context.Context) tea.Msg {
		lat, err := fleet.Test(ctx, m)
		return sshTestMsg{m.Name, lat, err}
	})
	s.tasks[id] = m.Name
	return cmd
}

func (s *serversScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case fleetMsg:
		if s.want != "" {
			for i, it := range s.items() {
				if it.machine != nil && it.machine.Name == s.want {
					s.sel = i
				}
			}
			s.want = ""
		}
	case TaskMsg:
		if m.ID == s.healthID && s.healthID != 0 {
			if in, ok := m.Msg.(fleetStatusMsg); ok {
				s.healthErr = ""
				if in.err != nil {
					s.healthErr = in.err.Error()
					return s, nil
				}
				for _, ms := range in.st {
					s.health[ms.Machine] = ms
					delete(s.results, ms.Machine) // the newer answer wins
				}
			}
			return s, nil
		}
		if m.ID == s.addTask && s.addTask != 0 {
			switch in := m.Msg.(type) {
			case logEndMsg:
				if in.err != nil {
					s.addErr = in.err.Error()
					return s, nil
				}
				s.adding = nil
				return s, tea.Batch(toast(lvlOK, "Machine added"), refresh("fleet"))
			case taskErrMsg:
				s.addErr = in.Err.Error()
			}
			return s, nil
		}
		if _, ok := s.tasks[m.ID]; !ok {
			return s, nil
		}
		if in, ok := m.Msg.(sshTestMsg); ok {
			r := &testResult{ok: in.err == nil, latency: in.latency}
			if in.err != nil {
				r.err = in.err.Error()
			}
			s.results[in.name] = r
		}
	case TaskEndedMsg:
		delete(s.tasks, m.ID)
	}
	return s, nil
}

// checkHealth runs `muster fleet status --json` for every machine and shows
// the answers in the table.
func (s *serversScreen) checkHealth() tea.Cmd {
	eng := s.env.Eng()
	if eng == nil || s.env.Tasks.Running(s.healthID) {
		return nil
	}
	id, cmd := s.env.Tasks.Run("checking fleet health", s.Key(), false, func(ctx context.Context) tea.Msg {
		st, err := fleet.Status(ctx, eng, "")
		return fleetStatusMsg{st, err}
	})
	s.healthID = id
	return cmd
}

func (s *serversScreen) submitAdd() tea.Cmd {
	f := s.adding
	name, target := strings.TrimSpace(f.get("name")), strings.TrimSpace(f.get("target"))
	switch {
	case name == "":
		s.addErr = "Give the machine a name"
		return nil
	case !strings.Contains(target, "@"):
		s.addErr = "Use the form user@host"
		return nil
	}
	eng := s.env.Eng()
	if eng == nil {
		s.addErr = "The muster CLI is needed to add machines"
		return nil
	}
	args := []string{"fleet", "add", name, target, "--port", firstNonEmpty(f.get("port"), "22"), "--mode", f.get("mode"), "--transport", f.get("transport")}
	if p := f.get("path"); p != "" {
		args = append(args, "--path", p)
	}
	if k := f.get("key"); k != "" {
		args = append(args, "--key", k)
	}
	s.addErr = ""
	id, cmd := s.env.Tasks.Stream("adding "+name, s.Key(), true, func(ctx context.Context) (<-chan tea.Msg, error) {
		lines, wait, err := rawLines(ctx, eng.Command(ctx, args...))
		if err != nil {
			return nil, err
		}
		out := make(chan tea.Msg, 4)
		go func() {
			defer close(out)
			var all []string
			for l := range lines {
				all = append(all, l)
			}
			err := wait()
			if err != nil && len(all) > 0 {
				err = fmt.Errorf("%s", strings.TrimSpace(all[len(all)-1]))
			}
			out <- logEndMsg{err}
		}()
		return out, nil
	})
	s.addTask = id
	return cmd
}

func (s *serversScreen) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	if s.adding != nil {
		switch k.String() {
		case "esc":
			if !s.adding.editing {
				s.adding = nil
				return s, nil, true
			}
		case "ctrl+s":
			return s, s.submitAdd(), true
		case "enter":
			if !s.adding.editing && s.adding.sel == len(s.adding.fields)-1 {
				return s, s.submitAdd(), true
			}
		}
		s.adding.HandleKey(k)
		return s, nil, true
	}
	items := s.items()
	var cur item
	if s.sel < len(items) {
		cur = items[s.sel]
	}
	target := ""
	if cur.machine != nil {
		target = cur.machine.Name
	} else if cur.group != nil {
		target = cur.group.Name
	}
	switch k.String() {
	case "j", "down":
		s.sel = clamp(s.sel+1, 0, len(items)-1)
	case "k", "up":
		s.sel = clamp(s.sel-1, 0, len(items)-1)
	case "a":
		s.startAdd(nil)
	case "t":
		if cur.machine != nil {
			return s, s.test(*cur.machine), true
		}
		if cur.group != nil {
			var cmds []tea.Cmd
			for _, name := range cur.group.Machines {
				if m, ok := s.env.State.Fleet.Find(name); ok {
					cmds = append(cmds, s.test(m))
				}
			}
			return s, tea.Batch(cmds...), true
		}
	case "T":
		var cmds []tea.Cmd
		for _, m := range s.env.State.Fleet.Machines {
			cmds = append(cmds, s.test(m))
		}
		return s, tea.Batch(cmds...), true
	case "enter", "s":
		if cur.machine != nil {
			return s, runLine("ssh " + cur.machine.Name), true
		}
	case "d":
		if target != "" {
			return s, runLine("fleet deploy " + target), true
		}
	case "u":
		return s, runLine(strings.TrimSpace("fleet status " + target)), true
	case "U":
		return s, s.checkHealth(), true
	case "b":
		if target != "" {
			t := target
			return s, func() tea.Msg {
				return confirmMsg{title: "Roll back " + t + "?", body: []string{"Runs muster fleet rollback " + t + "."}, yes: "Roll back", onYes: runLine("fleet rollback " + t)}
			}, true
		}
	case "c":
		if !s.env.State.Cloud.LoggedIn {
			return s, runLine("cloud login"), true
		}
		return s, runLine("cloud status"), true
	case "R":
		return s, refresh("fleet"), true
	default:
		return s, nil, false
	}
	return s, nil, true
}

func (s *serversScreen) View(w, h int) string {
	st := s.env.State
	if s.adding != nil {
		lines := []string{section("Add a machine"), ""}
		lines = append(lines, s.adding.Lines(w, 20, true)...)
		lines = append(lines, "")
		if s.addErr != "" {
			lines = append(lines, theme.Err.Render(theme.GlyphFail+" ")+trunc(s.addErr, w-2))
		}
		if s.env.Tasks.Running(s.addTask) {
			lines = append(lines, theme.AccentFg.Render(s.env.Spin())+" Adding…")
		}
		lines = append(lines, keyHint("↑↓", "field", "Enter", "edit", "Ctrl+S", "add", "Esc", "cancel"))
		return block(lines, w, h)
	}
	cloud := theme.Dim.Render("cloud: not signed in · c")
	if st.Cloud.LoggedIn {
		cloud = theme.OK.Render("● ") + theme.Dim.Render("cloud: "+firstNonEmpty(st.Cloud.Name, st.Cloud.Org))
	}
	lines := []string{spread(section("Servers"), cloud, w), ""}
	items := s.items()
	if len(items) == 0 {
		if !st.FleetLoaded {
			lines = append(lines, theme.AccentFg.Render(s.env.Spin())+theme.Dim.Render(" loading fleet…"))
		} else {
			lines = append(lines, "No machines yet.", "")
			lines = append(lines, wrap("Press a to add one (name, user@host, SSH port), or type `fleet add prod-1 deploy@10.0.0.5`. muster deploys to them over SSH, or through Fleet Cloud when you're signed in.", w)...)
		}
		for len(lines) < h-2 {
			lines = append(lines, "")
		}
		lines = append(lines, rule(w), keyHint("a", "add machine", "c", "cloud login", "R", "refresh"))
		return block(lines, w, h)
	}
	nameW := 12
	lines = append(lines, theme.Dim.Render("  "+fit("MACHINE", nameW)+fit("VIA", 7)+fit("MODE", 8)+fit("TARGET", clamp(w-nameW-35, 10, 30))+" STATUS"))
	groupHeader := false
	for i, it := range items {
		cur := "  "
		if i == s.sel {
			cur = theme.AccentFg.Render(theme.GlyphCursor) + " "
		}
		if it.machine != nil {
			m := it.machine
			status := theme.Dim.Render("untested · t")
			glyph := theme.Dim.Render(theme.GlyphUnknown)
			if m.Paired != nil && !*m.Paired && m.Mode == "muster" {
				status = theme.Warning.Render("not paired") + theme.Dim.Render(" · muster fleet pair "+m.Name)
			}
			if hs, ok := s.health[m.Name]; ok {
				status, glyph = fleetHealthText(hs)
			}
			if s.env.Tasks.Running(s.healthID) {
				status = theme.AccentFg.Render(s.env.Spin() + " checking")
			}
			if r := s.results[m.Name]; r != nil {
				switch {
				case r.running:
					status, glyph = theme.AccentFg.Render(s.env.Spin()+" testing"), theme.AccentFg.Render(s.env.Spin())
				case r.ok:
					status, glyph = theme.OK.Render("reachable "+dur(r.latency)), theme.OK.Render(theme.GlyphHealthy)
				default:
					status, glyph = theme.Err.Render(trunc(r.err, 40)), theme.Err.Render(theme.GlyphUnhealthy)
				}
			}
			lines = append(lines, trunc(cur+glyph+" "+fit(m.Name, nameW-2)+fit(m.Transport, 7)+fit(m.Mode, 8)+fit(m.Target(), clamp(w-nameW-35, 10, 30))+" "+status, w))
			continue
		}
		if !groupHeader {
			lines = append(lines, "", theme.Title.Render("Groups"))
			groupHeader = true
		}
		g := it.group
		lines = append(lines, trunc(cur+fit(g.Name, nameW)+theme.Dim.Render(strings.Join(g.Machines, ", ")), w))
	}
	if s.healthErr != "" {
		lines = append(lines, "", theme.Err.Render(theme.GlyphFail+" ")+trunc("fleet status: "+s.healthErr, w-2))
	}
	for len(lines) < h-2 {
		lines = append(lines, "")
	}
	lines = append(lines, rule(w), keyHint("t", "test", "U", "health", "Enter", "ssh", "d", "deploy", "u", "status", "b", "rollback", "a", "add"))
	return block(lines, w, h)
}

// fleetHealthText renders one machine's `fleet status --json` answer.
func fleetHealthText(hs fleet.MachineStatus) (string, string) {
	if !hs.Reachable {
		return theme.Err.Render(firstNonEmpty(hs.Error, "unreachable")), theme.Err.Render(theme.GlyphUnhealthy)
	}
	if hs.Healthy != nil && hs.Total != nil && *hs.Total > 0 {
		txt := fmt.Sprintf("%d/%d healthy", *hs.Healthy, *hs.Total)
		if *hs.Healthy < *hs.Total {
			return theme.Warning.Render(txt), theme.Warning.Render(theme.GlyphWarn)
		}
		return theme.OK.Render(txt), theme.OK.Render(theme.GlyphHealthy)
	}
	if hs.Error != "" {
		return theme.Warning.Render("reachable · " + hs.Error), theme.OK.Render(theme.GlyphHealthy)
	}
	return theme.OK.Render("reachable"), theme.OK.Render(theme.GlyphHealthy)
}

func (s *serversScreen) Hint() string {
	if s.adding != nil {
		return keyHint("Enter", "edit field", "Ctrl+S", "add machine", "Esc", "cancel")
	}
	return ""
}

func (s *serversScreen) Keys() []KeyHelp {
	return []KeyHelp{{"j / k", "select"}, {"t / T", "test selected / all"}, {"U", "fleet health (muster fleet status)"}, {"Enter / s", "ssh (muster steps aside)"}, {"d", "fleet deploy"}, {"u", "fleet status"}, {"b", "fleet rollback"}, {"a", "add machine"}, {"c", "cloud login / status"}, {"R", "refresh"}}
}
