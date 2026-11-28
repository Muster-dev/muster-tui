package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/fleet"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// serversScreen is the list of fleet servers (and groups) with live status.
// It is also the home screen when the folder has no muster.json.
type serversScreen struct {
	env     *Env
	key     string // "servers", or "dashboard" when it is the home screen
	sel     int
	adding  *form
	addErr  string
	addTask int
	want    string
	// Master-detail on wide terminals: the selected server's detail is the
	// right pane; focusDetail sends keys to it instead of the list.
	detail      *serverScreen
	focusDetail bool
}

func newServers(env *Env) *serversScreen {
	return &serversScreen{env: env, key: "servers"}
}

func (s *serversScreen) Key() string     { return s.key }
func (s *serversScreen) Title() string   { return "Servers" }
func (s *serversScreen) Capturing() bool { return s.adding != nil }
func (s *serversScreen) Close()          { s.env.Tasks.CancelOwner(s.Key()) }
func (s *serversScreen) Init() tea.Cmd {
	if s.key != "servers" {
		return nil
	}
	return tea.Batch(refresh("fleet"), s.env.loadFleetHealth())
}

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
		{key: "name", label: "Name", kind: fText, value: name, help: "what you'll call it, e.g. prod-1"},
		{key: "target", label: "user@host", kind: fText, value: target, help: "e.g. deploy@10.0.0.5"},
		{key: "transport", label: "Connect with", kind: fEnum, opts: []string{"ssh", "cloud"}, value: "ssh", help: "ssh = straight from this computer · cloud = through Fleet Cloud"},
		{key: "mode", label: "On the server", kind: fEnum, opts: []string{"push", "muster"}, value: "push", help: "push = send hooks over SSH · muster = the server runs muster itself"},
		{key: "port", label: "SSH port", kind: fInt, value: "22"},
		{key: "path", label: "Project folder", kind: fText, help: "on the server, optional"},
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
		if len(inv.Groups[i].Machines) == 0 {
			continue
		}
		out = append(out, item{group: &inv.Groups[i]})
	}
	return out
}

func (s *serversScreen) current() item {
	items := s.items()
	if s.sel >= 0 && s.sel < len(items) {
		return items[s.sel]
	}
	return item{}
}

func (s *serversScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	var detailCmd tea.Cmd
	if s.detail != nil {
		_, detailCmd = s.detail.Update(msg)
	}
	ns, cmd := s.update(msg)
	return ns, tea.Batch(detailCmd, cmd)
}

func (s *serversScreen) update(msg tea.Msg) (Screen, tea.Cmd) {
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
		s.sel = clamp(s.sel, 0, max(len(s.items())-1, 0))
	case TaskMsg:
		if m.ID == s.addTask && s.addTask != 0 {
			switch in := m.Msg.(type) {
			case logEndMsg:
				if in.err != nil {
					s.addErr = in.err.Error()
					return s, nil
				}
				s.adding = nil
				return s, tea.Batch(toast(lvlOK, "Server added"), refresh("fleet"))
			case taskErrMsg:
				s.addErr = in.Err.Error()
			}
		}
	}
	return s, nil
}

func (s *serversScreen) submitAdd() tea.Cmd {
	f := s.adding
	name, target := strings.TrimSpace(f.get("name")), strings.TrimSpace(f.get("target"))
	switch {
	case name == "":
		s.addErr = "Give the server a name"
		return nil
	case !strings.Contains(target, "@"):
		s.addErr = "Use the form user@host"
		return nil
	}
	eng := s.env.Eng()
	if eng == nil {
		s.addErr = "The muster CLI is needed to add servers"
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
		return runQuiet(ctx, eng.Command(ctx, args...))
	})
	s.addTask = id
	return cmd
}

// runQuiet runs a command and reports only how it ended (with its last
// output line as the error).
func runQuiet(ctx context.Context, c *exec.Cmd) (<-chan tea.Msg, error) {
	lines, wait, err := rawLines(ctx, c)
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
}

// serverAction runs one action on a server or group (shared by the list,
// the detail screen and the palette).
func serverAction(env *Env, action, target string) tea.Cmd {
	mc, isMachine := env.State.Fleet.Find(target)
	switch action {
	case "open":
		if isMachine {
			return nav(newServerScreen(env, target))
		}
		return runLine("fleet status " + target)
	case "deploy":
		body := []string{"Runs muster fleet deploy " + target + "."}
		if isMachine {
			body = append(body, runsHow(mc)+" · "+connection(mc))
		}
		return func() tea.Msg {
			return confirmMsg{title: "Deploy to " + target + "?", body: body, yes: "Deploy", onYes: runLine("fleet deploy " + target)}
		}
	case "rollback":
		return func() tea.Msg {
			return confirmMsg{title: "Roll back " + target + "?", body: []string{"Runs muster fleet rollback " + target + ": every service there goes back to its previous version."}, yes: "Roll back", onYes: runLine("fleet rollback " + target)}
		}
	case "status":
		return runLine("fleet status " + target)
	case "test":
		return runLine("fleet test " + target)
	case "shell":
		return runLine("ssh " + target)
	case "pair":
		return runLine("pair " + target)
	}
	return nil
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
	if s.split() {
		if ns, cmd, ok := s.splitKey(k); ok {
			return ns, cmd, true
		}
	}
	items := s.items()
	cur := s.current()
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
	case "home", "g":
		s.sel = 0
	case "end", "G":
		s.sel = max(len(items)-1, 0)
	case "a":
		s.startAdd(nil)
	case "enter", "right", "l":
		if target != "" {
			return s, serverAction(s.env, "open", target), true
		}
	case "d":
		if target != "" {
			return s, serverAction(s.env, "deploy", target), true
		}
	case "s":
		if target != "" {
			return s, serverAction(s.env, "status", target), true
		}
	case "t":
		if target != "" {
			return s, serverAction(s.env, "test", target), true
		}
	case "b":
		if target != "" {
			return s, serverAction(s.env, "rollback", target), true
		}
	case "p":
		if cur.machine != nil {
			return s, serverAction(s.env, "pair", target), true
		}
	case "c":
		if !s.env.State.Cloud.LoggedIn {
			return s, runLine("cloud login"), true
		}
		return s, runLine("cloud status"), true
	case "r", "R":
		return s, tea.Batch(refresh("fleet"), s.env.loadFleetHealth()), true
	default:
		return s, nil, false
	}
	return s, nil, true
}

func (s *serversScreen) Click(id string, double bool) (Screen, tea.Cmd, bool) {
	if d := s.paneDetail(); d != nil && (strings.HasPrefix(id, "act:") || strings.HasPrefix(id, "pick:")) {
		s.focusDetail, d.blurred = true, false
		_, cmd, ok := d.Click(id, double)
		return s, cmd, ok
	}
	if arg, ok := zoneArg(id, "srv:row:"); ok {
		i, _ := strconv.Atoi(arg)
		s.focusDetail = false
		if s.split() && i < len(s.items()) && s.items()[i].machine != nil {
			// The right pane already shows it; a second click moves into it.
			if i == s.sel || double {
				s.focusDetail = true
			}
			s.sel = i
			return s, nil, true
		}
		if i == s.sel || double {
			s.sel = i
			ns, cmd, _ := s.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})
			return ns, cmd, true
		}
		s.sel = i
		return s, nil, true
	}
	if arg, ok := zoneArg(id, "srv:do:"); ok {
		return s, runLine(arg), true
	}
	return s, nil, false
}

// serverColumns are the list's column widths for a pane w cells wide.
type serverColumns struct{ name, status, conn, svc int }

func serverCols(w int, names []string) serverColumns {
	c := serverColumns{name: 6, status: 15, svc: 12}
	for _, n := range names {
		c.name = max(c.name, width(n))
	}
	c.name = min(c.name, clamp(w/4, 8, 22))
	// 2 cursor + 2 gaps between each column
	c.conn = w - 2 - c.name - c.status - c.svc - 6
	if c.conn < 12 {
		c.svc = 0
		c.conn = w - 2 - c.name - c.status - 4
	}
	c.conn = clamp(c.conn, 6, 40)
	return c
}

// listLines renders the server list (shared with the project dashboard).
func (s *serversScreen) listLines(w int, compact bool) []string {
	st := s.env.State
	items := s.items()
	var names []string
	for _, it := range items {
		if it.machine != nil {
			names = append(names, it.machine.Name)
		}
	}
	c := serverCols(w, names)
	head := "  " + fit("Server", c.name) + "  " + fit("Status", c.status) + "  " + fit("Connection", c.conn)
	if c.svc > 0 {
		head += "  " + "Services"
	}
	lines := []string{theme.Dim.Render(head)}
	groupHeader := false
	for i, it := range items {
		selected := i == s.sel && !compact
		cur := "  "
		if selected {
			cur = theme.AccentFg.Render(theme.GlyphCursor) + " "
		}
		if it.machine != nil {
			mc := *it.machine
			ss := s.env.serverStatus(mc)
			svc := ss.Services
			if svc == "" {
				svc = theme.Dim.Render("—")
				if ss.Pairing {
					svc = theme.Dim.Render("press p")
				}
			}
			row := cur + fit(mc.Name, c.name) + "  " + fit(ss.Glyph+" "+ss.Word, c.status) + "  " + fit(theme.Dim.Render(connection(mc)), c.conn)
			if c.svc > 0 {
				row += "  " + fit(svc, c.svc)
			}
			lines = append(lines, s.env.mark("srv:row:"+strconv.Itoa(i), fit(row, w)))
			continue
		}
		if compact {
			continue
		}
		if !groupHeader {
			lines = append(lines, "", theme.Dim.Render("  Groups"))
			groupHeader = true
		}
		g := it.group
		row := cur + fit(g.Name, c.name) + "  " + theme.Dim.Render(trunc(strings.Join(g.Machines, ", "), w-c.name-4))
		lines = append(lines, s.env.mark("srv:row:"+strconv.Itoa(i), fit(row, w)))
	}
	if st.FleetHealthErr != nil {
		lines = append(lines, "", theme.Err.Render(theme.GlyphFail+" ")+trunc("Couldn't check servers: "+st.FleetHealthErr.Error(), w-2))
	}
	return lines
}

func (s *serversScreen) statusLine() string {
	st := s.env.State
	now := s.env.Now()
	if !st.FleetHealthAt.IsZero() {
		return theme.Dim.Render("checked " + ago(st.FleetHealthAt, now))
	}
	return ""
}

func (s *serversScreen) View(w, h int) string {
	st := s.env.State
	if s.adding != nil {
		lines := []string{section("Add a server"), ""}
		lines = append(lines, s.adding.Lines(w, 16, true)...)
		lines = append(lines, "")
		if s.addErr != "" {
			lines = append(lines, theme.Err.Render(theme.GlyphFail+" ")+trunc(s.addErr, w-2))
		}
		if s.env.Tasks.Running(s.addTask) {
			lines = append(lines, theme.AccentFg.Render(s.env.Spin())+" Adding…")
		}
		return block(lines, w, h)
	}
	// Home has no breadcrumb, so it names the list; the Servers screen's
	// header already does.
	lines := []string{""}
	if s.key == "dashboard" {
		lines = []string{spread(section("Servers"), s.statusLine(), w), ""}
	}
	items := s.items()
	if len(items) == 0 {
		if !st.FleetLoaded {
			lines = append(lines, theme.AccentFg.Render(s.env.Spin())+" Loading servers…")
			return block(lines, w, h)
		}
		lines = append(lines, "No servers yet.", "")
		lines = append(lines, wrap("Add one and muster can deploy to it over SSH, or through Fleet Cloud when you're signed in.", w)...)
		lines = append(lines, "", "  "+s.env.mark("srv:do:fleet add", button("Add a server", true))+"   "+theme.Dim.Render("a"))
		if !st.Cloud.LoggedIn {
			lines = append(lines, "  "+s.env.mark("srv:do:cloud login", button("Sign in to Fleet Cloud", false))+" "+theme.Dim.Render("c"))
		}
		return block(lines, w, h)
	}
	if s.split() {
		return s.splitView(w, h)
	}
	lines = append(lines, s.listLines(w, false)...)
	if mc := s.current().machine; mc != nil {
		if d := s.env.serverStatus(*mc).Detail; d != "" {
			lines = append(lines, "", theme.Dim.Render(trunc(mc.Name+": "+d, w)))
		}
	}
	if !st.Cloud.LoggedIn && hasCloudServer(st.Fleet) {
		lines = append(lines, "", theme.Warning.Render(theme.GlyphWarn+" ")+"Not signed in to Fleet Cloud"+theme.Dim.Render(" — press c to sign in"))
	}
	return block(lines, w, h)
}

func hasCloudServer(inv fleet.Inventory) bool {
	for _, m := range inv.Machines {
		if m.Transport == "cloud" {
			return true
		}
	}
	return false
}

func (s *serversScreen) Hint() string { return "" }

// Status is shown on the right of the header (Servers screen only).
func (s *serversScreen) Status() string {
	if s.key == "dashboard" || s.adding != nil {
		return ""
	}
	return s.statusLine()
}

func (s *serversScreen) Footer() []KeyHelp {
	if s.adding != nil {
		return []KeyHelp{{"enter", "edit"}, {"ctrl+s", "add server"}, {"esc", "cancel"}}
	}
	if len(s.items()) == 0 {
		return []KeyHelp{{"a", "add server"}, {"c", "cloud login"}}
	}
	if d := s.paneDetail(); d != nil {
		if s.focusDetail {
			out := d.Footer()
			if n := len(out); n > 0 && out[n-1].Key == "esc" {
				out[n-1].Desc = "servers"
			}
			return out
		}
		out := []KeyHelp{{"enter", "actions"}, {"d", "deploy"}, {"t", "test"}}
		if s.env.serverStatus(*s.current().machine).Pairing {
			out = append(out, KeyHelp{"p", "pair"})
		}
		return append(out, KeyHelp{"a", "add server"}, KeyHelp{"r", "refresh"})
	}
	out := []KeyHelp{{"enter", "open"}, {"d", "deploy"}}
	if mc := s.current().machine; mc != nil && s.env.serverStatus(*mc).Pairing {
		out = append(out, KeyHelp{"p", "pair"})
	}
	return append(out, KeyHelp{"a", "add server"}, KeyHelp{"r", "refresh"})
}

func (s *serversScreen) Keys() []KeyHelp {
	return []KeyHelp{{"↑ ↓ / j k", "select"}, {"enter", "open the server"}, {"d", "deploy (asks first)"}, {"s", "status"}, {"t", "test the connection"}, {"b", "roll back (asks first)"}, {"p", "pair a server that runs muster"}, {"a", "add a server"}, {"c", "Fleet Cloud sign in / status"}, {"r", "check every server now"}}
}

// ── Master-detail (wide terminals) ──

// Wide: the servers list goes master-detail at splitMin columns.
func (s *serversScreen) Wide() bool { return s.split() }

func (s *serversScreen) split() bool {
	return s.adding == nil && s.env.MainW >= splitMin && len(s.items()) > 0
}

// paneDetail is the right pane's server screen (nil when not split or a
// group is selected).
func (s *serversScreen) paneDetail() *serverScreen {
	if !s.split() {
		return nil
	}
	mc := s.current().machine
	if mc == nil {
		s.focusDetail = false
		return nil
	}
	if s.detail == nil || s.detail.name != mc.Name {
		s.detail = newServerScreen(s.env, mc.Name)
		s.detail.embedded = true
	}
	s.detail.blurred = !s.focusDetail
	return s.detail
}

// splitKey handles the keys that differ in master-detail.
func (s *serversScreen) splitKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	d := s.paneDetail()
	if d == nil {
		return s, nil, false
	}
	key := k.String()
	if s.focusDetail {
		if d.pick == "" {
			switch key {
			case "esc", "left", "h", "shift+tab":
				s.focusDetail, d.blurred = false, true
				return s, nil, true
			}
		}
		_, cmd, ok := d.HandleKey(k)
		return s, cmd, ok
	}
	switch key {
	case "enter", "right", "l", "tab":
		s.focusDetail, d.blurred = true, false
		return s, nil, true
	case "o", "R":
		// Detail-only actions work from the list too.
		_, cmd, ok := d.HandleKey(k)
		return s, cmd, ok
	}
	return s, nil, false
}

// splitView: the list as a raised sidebar on the left, the selected
// server's detail on the right; the pair is centred on very wide terminals.
func (s *serversScreen) splitView(w, h int) string {
	total := min(w, 172)
	listW := clamp(total*30/100, 40, 50)
	gap := 4
	detW := total - listW - gap

	var left []string
	if s.key == "dashboard" {
		left = append(left, spread(section("Servers"), s.statusLine(), listW-4), "")
	}
	left = append(left, s.sidebarLines(listW-4)...)
	if !s.env.State.Cloud.LoggedIn && hasCloudServer(s.env.State.Fleet) {
		left = append(left, "")
		left = append(left, wrapDim("Sign in to Fleet Cloud: press c", listW-4)...)
	}
	side := panel(left, listW, 2, 1)
	for len(side) < h {
		side = append(side, paint("", listW, bgSeq(theme.Surface)))
	}

	right := []string{""} // level with the sidebar's padding
	if d := s.paneDetail(); d != nil {
		right = append(right, d.lines(detW)...)
	} else if g := s.current().group; g != nil {
		right = append(right, s.groupLines(*g, detW)...)
	}
	lp := strings.Repeat(" ", max((w-total)/2, 0))
	out := make([]string, h)
	for i := 0; i < h; i++ {
		r := ""
		if i < len(right) {
			r = right[i]
		}
		out[i] = lp + side[i] + strings.Repeat(" ", gap) + fit(r, detW)
	}
	return strings.Join(out, "\n")
}

// sidebarLines: one row per server (dot, name, how it's doing), then groups.
func (s *serversScreen) sidebarLines(w int) []string {
	var out []string
	groupHeader := false
	for i, it := range s.items() {
		selected := i == s.sel
		var left, right string
		if it.machine != nil {
			ss := s.env.serverStatus(*it.machine)
			left = ss.Glyph + " " + theme.Base.Render(it.machine.Name)
			right = ss.Services
			if right == "" || ss.Pairing || ss.Offline {
				right = ss.Word
			}
		} else {
			if !groupHeader {
				out = append(out, "", theme.Dim.Render("Groups"))
				groupHeader = true
			}
			g := it.group
			left = theme.Dim.Render("◇") + " " + theme.Base.Render(g.Name)
			right = theme.Dim.Render(plural(len(g.Machines), "server"))
		}
		row := spread(left, right, w-2)
		if selected {
			if s.focusDetail {
				row = theme.Dim.Render(theme.GlyphCursor) + " " + row
				row = paint(row, w, bgSeq(theme.Surface))
			} else {
				row = band(row, w)
			}
		} else {
			row = "  " + row
		}
		out = append(out, s.env.mark("srv:row:"+strconv.Itoa(i), row))
	}
	if err := s.env.State.FleetHealthErr; err != nil {
		out = append(out, "")
		out = append(out, wrapDim("Couldn't check servers: "+err.Error(), w)...)
	}
	return out
}

// groupLines is the right pane for a group: its servers and how they're doing.
func (s *serversScreen) groupLines(g fleet.Group, w int) []string {
	lines := []string{spread(theme.Title.Render(g.Name), theme.Dim.Render("group · "+plural(len(g.Machines), "server")), w), ""}
	for _, name := range g.Machines {
		row := theme.Dim.Render(theme.GlyphUnknown) + " " + name
		if mc, ok := s.env.State.Fleet.Find(name); ok {
			ss := s.env.serverStatus(mc)
			row = ss.Glyph + " " + fit(name, 18) + "  " + fit(ss.Word, 15) + "  " + theme.Dim.Render(connection(mc))
		}
		lines = append(lines, trunc(row, w))
	}
	lines = append(lines, "", theme.Dim.Render("Group actions"))
	lines = append(lines, "  "+keyHint("d", "deploy to every server", "s", "status", "t", "test", "b", "roll back"))
	return lines
}
