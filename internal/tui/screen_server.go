package tui

import (
	"context"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/fleet"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// serverScreen is one server: how it connects, its services and what you
// can do with it.
type serverScreen struct {
	env  *Env
	name string
	sel  int
	// pick: choosing a service for "logs" or "restart" ("" = not picking).
	pick    string
	pickSel int
	// restarting: a fleet restart is running (task id).
	restartTask int
}

func newServerScreen(env *Env, name string) *serverScreen {
	return &serverScreen{env: env, name: name}
}

func (s *serverScreen) Key() string   { return "server:" + s.name }
func (s *serverScreen) Title() string { return "Servers › " + s.name }
func (s *serverScreen) Init() tea.Cmd {
	if s.env.State.FleetHealthAt.IsZero() {
		return s.env.loadFleetHealth()
	}
	return nil
}
func (s *serverScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if g, ok := msg.(restartGoMsg); ok && g.server == s.name {
		return s, s.doRestart(g.svc)
	}
	m, ok := msg.(TaskMsg)
	if !ok || m.ID != s.restartTask || s.restartTask == 0 {
		return s, nil
	}
	switch in := m.Msg.(type) {
	case fleetRestartMsg:
		return s, tea.Batch(restartToast(s.name, in.r, in.err), refreshFleetHealth())
	case taskErrMsg:
		return s, toast(lvlErr, "Restart on "+s.name+" failed: "+errText(in.Err))
	}
	return s, nil
}

// restartToast sums up a fleet restart in one line.
func restartToast(server string, r fleet.RestartResult, err error) tea.Cmd {
	var ok, bad []string
	reason := ""
	for _, x := range r.Results {
		if x.OK {
			ok = append(ok, x.Service)
		} else {
			bad = append(bad, x.Service)
			reason = firstNonEmpty(reason, x.Error)
		}
	}
	switch {
	case len(bad) > 0:
		msg := "Couldn't restart " + strings.Join(bad, ", ") + " on " + server
		if reason != "" {
			msg += ": " + reason
		}
		return toast(lvlErr, msg)
	case err != nil:
		return toast(lvlErr, "Restart on "+server+" failed: "+errText(err))
	case len(ok) == 0:
		return toast(lvlWarn, "Nothing was restarted on "+server)
	}
	return toast(lvlOK, "Restarted "+strings.Join(ok, ", ")+" on "+server)
}
func (s *serverScreen) Hint() string                     { return "" }

func (s *serverScreen) machine() (fleet.Machine, bool) { return s.env.State.Fleet.Find(s.name) }

// serverAct is one action on the detail screen. Disabled actions say why.
type serverAct struct {
	id, label, key, what, why string
}

func (s *serverScreen) actions() []serverAct {
	mc, _ := s.machine()
	ss := s.env.serverStatus(mc)
	var acts []serverAct
	if ss.Pairing {
		acts = append(acts, serverAct{id: "pair", label: "Pair", key: "p", what: "paste a token from the server so muster trusts this computer"})
	}
	acts = append(acts,
		serverAct{id: "deploy", label: "Deploy", key: "d", what: "muster fleet deploy " + s.name},
		serverAct{id: "status", label: "Status", key: "s", what: "muster fleet status " + s.name},
		serverAct{id: "test", label: "Test connection", key: "t", what: "muster fleet test " + s.name},
		serverAct{id: "rollback", label: "Rollback", key: "b", what: "muster fleet rollback " + s.name},
		s.logsAct(ss),
		s.restartAct(ss),
	)
	if canShell(mc) {
		acts = append(acts, serverAct{id: "shell", label: "Open shell", key: "o", what: "ssh " + mc.Target()})
	}
	ask := serverAct{id: "ask", label: "Ask assistant", key: "a", what: "Muster Assistant, about this server"}
	switch {
	case !s.env.HasProject():
		ask.why = "needs a project folder — muster ask reads its muster.json"
	case !s.env.State.Cloud.HasToken:
		ask.why = "needs Fleet Cloud — sign in with cloud login"
	}
	return append(acts, ask)
}

// remoteServices lists the services the server reported.
func (s *serverScreen) remoteServices(ss serverState) []string {
	if ss.Status == nil {
		return nil
	}
	var out []string
	for _, sv := range ss.Status.Services {
		out = append(out, sv.Service)
	}
	return out
}

func (s *serverScreen) logsAct(ss serverState) serverAct {
	a := serverAct{id: "logs", label: "Logs", key: "l", what: "recent log lines from a service on the server"}
	switch {
	case !s.env.State.Caps.FleetLogs:
		a.why = "needs a newer muster (fleet logs) — run muster update"
	case ss.Pairing:
		a.why = "pair the server first"
	case ss.Offline:
		a.why = "the server is offline"
	case ss.Status == nil:
		a.why = "check the server first (r) to see its services"
	case len(ss.Status.Services) == 0:
		a.why = "the server reported no services"
	}
	return a
}

func (s *serverScreen) restartAct(ss serverState) serverAct {
	a := serverAct{id: "restart", label: "Restart", key: "R", what: "restart services on the server (asks first)"}
	switch {
	case !s.env.State.Caps.FleetRestart:
		a.why = "needs a newer muster (fleet restart) — run muster update"
	case ss.Pairing:
		a.why = "pair the server first"
	case ss.Offline:
		a.why = "the server is offline"
	case s.env.Tasks.Running(s.restartTask):
		a.why = "already restarting…"
	case ss.Status == nil:
		a.why = "check the server first (r) to see its services"
	case len(ss.Status.Services) == 0:
		a.why = "the server reported no services"
	}
	return a
}

func (s *serverScreen) run(a serverAct) tea.Cmd {
	if a.why != "" {
		return toast(lvlInfo, a.label+": "+a.why)
	}
	switch a.id {
	case "ask":
		return func() tea.Msg { return prefillMsg{text: "ask about server " + s.name + ": "} }
	case "logs", "restart":
		mc, _ := s.machine()
		svcs := s.remoteServices(s.env.serverStatus(mc))
		if a.id == "logs" && len(svcs) == 1 {
			return nav(newFleetLogs(s.env, s.name, svcs[0]))
		}
		s.pick, s.pickSel = a.id, 0
		return nil
	}
	return serverAction(s.env, a.id, s.name)
}

// pickOptions: the services, plus "All services" first for restart.
func (s *serverScreen) pickOptions() []string {
	mc, _ := s.machine()
	svcs := s.remoteServices(s.env.serverStatus(mc))
	if s.pick == "restart" && len(svcs) > 1 {
		return append([]string{""}, svcs...)
	}
	return svcs
}

func (s *serverScreen) choose(svc string) tea.Cmd {
	what := s.pick
	s.pick = ""
	if what == "logs" {
		return nav(newFleetLogs(s.env, s.name, svc))
	}
	return s.confirmRestart(svc)
}

func (s *serverScreen) confirmRestart(svc string) tea.Cmd {
	name := s.name
	which := svc + " on " + name
	if svc == "" {
		which = "every service on " + name
	}
	cmdline := "muster fleet restart " + name
	if svc != "" {
		cmdline += " " + svc
	}
	run := s.startRestart(svc)
	return func() tea.Msg {
		return confirmMsg{
			title: "Restart " + which + "?",
			body:  []string{"Runs " + cmdline + ". The service stops briefly; nothing is redeployed."},
			yes:   "Restart",
			onYes: run,
		}
	}
}

type fleetRestartMsg struct {
	r   fleet.RestartResult
	err error
}

// restartGoMsg comes back from the confirm's "Restart" button; the screen
// starts the task when it sees it (tasks only start inside Update).
type restartGoMsg struct{ server, svc string }

func (s *serverScreen) startRestart(svc string) tea.Cmd {
	name := s.name
	return func() tea.Msg { return restartGoMsg{name, svc} }
}

func (s *serverScreen) doRestart(svc string) tea.Cmd {
	eng := s.env.Eng()
	if eng == nil {
		return toast(lvlErr, "The muster CLI is needed to restart")
	}
	name := s.name
	var svcs []string
	if svc != "" {
		svcs = []string{svc}
	}
	id, cmd := s.env.Tasks.Run("restarting on "+name, s.Key(), true, func(ctx context.Context) tea.Msg {
		r, err := fleet.Restart(ctx, eng, name, svcs...)
		return fleetRestartMsg{r, err}
	})
	s.restartTask = id
	return cmd
}

func (s *serverScreen) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	if s.pick != "" {
		opts := s.pickOptions()
		switch k.String() {
		case "j", "down", "tab":
			s.pickSel = clamp(s.pickSel+1, 0, len(opts)-1)
		case "k", "up", "shift+tab":
			s.pickSel = clamp(s.pickSel-1, 0, len(opts)-1)
		case "enter":
			if s.pickSel < len(opts) {
				return s, s.choose(opts[s.pickSel]), true
			}
		case "esc", "left", "h":
			s.pick = ""
		}
		return s, nil, true
	}
	acts := s.actions()
	key := k.String()
	switch key {
	case "j", "down", "tab":
		s.sel = clamp(s.sel+1, 0, len(acts)-1)
		return s, nil, true
	case "k", "up", "shift+tab":
		s.sel = clamp(s.sel-1, 0, len(acts)-1)
		return s, nil, true
	case "enter":
		if s.sel < len(acts) {
			return s, s.run(acts[s.sel]), true
		}
		return s, nil, true
	case "r":
		return s, tea.Batch(refresh("fleet"), s.env.loadFleetHealth()), true
	case "left", "h":
		return s, func() tea.Msg { return backMsg{} }, true
	}
	for i, a := range acts {
		if a.key == key {
			s.sel = i
			return s, s.run(a), true
		}
	}
	return s, nil, false
}

func (s *serverScreen) Click(id string, double bool) (Screen, tea.Cmd, bool) {
	if arg, ok := zoneArg(id, "pick:"); ok && s.pick != "" {
		return s, s.choose(arg), true
	}
	if arg, ok := zoneArg(id, "act:"); ok {
		for i, a := range s.actions() {
			if a.id == arg {
				s.sel = i
				return s, s.run(a), true
			}
		}
	}
	if id == "srv:back" {
		return s, func() tea.Msg { return backMsg{} }, true
	}
	return s, nil, false
}

func (s *serverScreen) View(w, h int) string {
	mc, ok := s.machine()
	if !ok {
		lines := []string{""}
		if !s.env.State.FleetLoaded {
			lines = append(lines, theme.AccentFg.Render(s.env.Spin())+theme.Dim.Render(" loading…"))
		} else {
			lines = append(lines, "There's no server called "+s.name+" any more.")
		}
		return block(lines, w, h)
	}
	ss := s.env.serverStatus(mc)
	lines := []string{""}
	conn := connection(mc)
	if mc.Transport == "cloud" {
		conn += theme.Dim.Render(" · agent " + mc.Target())
	}
	lines = append(lines, conn)
	meta := runsHow(mc)
	if mc.Path != "" {
		meta += " · " + mc.Path
	}
	if ss.Status != nil && ss.Status.MusterVersion != "" {
		meta += " · muster " + strings.TrimPrefix(ss.Status.MusterVersion, "v")
	}
	lines = append(lines, theme.Dim.Render(trunc(meta, w)))
	if g := strings.TrimSpace(mc.Group); g != "" && g != "default" {
		lines = append(lines, theme.Dim.Render("Group "+g))
	}
	if ss.Detail != "" {
		style := theme.Dim
		if ss.Offline {
			style = theme.Err
		} else if ss.Pairing {
			style = theme.Warning
		}
		for _, l := range wrap(ss.Detail, w-2) {
			lines = append(lines, style.Render(l))
		}
	}

	svcLines := s.serviceLines(w, ss)
	actTitle, actLines := s.actionsSection(w)
	if w >= 110 {
		// Services on the left, actions on the right.
		colW := w/2 - 2
		left := append([]string{"", section("Services")}, svcLines...)
		t, al := s.actionsSection(w - colW - 4)
		rightCol := append([]string{"", section(t)}, al...)
		for i := 0; i < max(len(left), len(rightCol)); i++ {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(rightCol) {
				r = rightCol[i]
			}
			lines = append(lines, fit(l, colW)+"    "+r)
		}
		return block(lines, w, h)
	}
	lines = append(lines, "", section("Services"))
	lines = append(lines, svcLines...)
	lines = append(lines, "", section(actTitle))
	lines = append(lines, actLines...)
	return block(lines, w, h)
}

func (s *serverScreen) serviceLines(w int, ss serverState) []string {
	if ss.Pairing {
		return []string{theme.Dim.Render("Pair the server to see its services.")}
	}
	if ss.Status == nil {
		if s.env.CheckingServers() {
			return []string{theme.AccentFg.Render(s.env.Spin()) + theme.Dim.Render(" asking the server…")}
		}
		return []string{theme.Dim.Render("Not checked yet — press r")}
	}
	if len(ss.Status.Services) == 0 {
		if mc, _ := s.machine(); mc.Mode == "push" {
			return wrapDim("No services reported — hooks for this server are sent from a project folder on this computer.", w)
		}
		return []string{theme.Dim.Render("No services reported.")}
	}
	nameW := 6
	for _, sv := range ss.Status.Services {
		nameW = max(nameW, width(sv.Service))
	}
	nameW = min(nameW, 16)
	var out []string
	for _, sv := range ss.Status.Services {
		row := serviceGlyph(sv) + " " + fit(sv.Service, nameW) + "  " + fit(serviceWord(sv), 10)
		if ld := sv.LastDeploy; ld != nil && ld.TS != "" {
			row += "  " + theme.Dim.Render("deployed "+ld.TS)
		}
		out = append(out, trunc(row, w))
		if sv.Detail != "" && strings.ToLower(sv.Status) != "healthy" {
			out = append(out, theme.Dim.Render(trunc("  "+sv.Detail, w)))
		}
	}
	return out
}

// actionsSection is the Actions list, or the service picker while choosing
// one for Logs / Restart.
func (s *serverScreen) actionsSection(w int) (string, []string) {
	if s.pick == "" {
		return "Actions", s.actionLines(w)
	}
	title := "Logs for which service?"
	if s.pick == "restart" {
		title = "Restart which service?"
	}
	var out []string
	for i, o := range s.pickOptions() {
		label := o
		if o == "" {
			label = "All services"
		}
		row := "  " + theme.Base.Render(label)
		if i == s.pickSel {
			row = theme.AccentFg.Render(theme.GlyphCursor) + " " + theme.Selected.Render(label)
		}
		out = append(out, s.env.mark("pick:"+o, trunc(row, w)))
	}
	return title, out
}

// Status is the server's state, on the right of the header.
func (s *serverScreen) Status() string {
	mc, ok := s.machine()
	if !ok {
		return ""
	}
	ss := s.env.serverStatus(mc)
	right := ss.Glyph + " " + ss.Word
	if ss.Services != "" {
		right += theme.Dim.Render(" · ") + ss.Services
	}
	return right
}

func (s *serverScreen) actionLines(w int) []string {
	var out []string
	labelW := 16
	for i, a := range s.actions() {
		label := fit(a.label, labelW)
		desc := theme.Dim.Render(a.what)
		var btn string
		if a.why != "" {
			btn = "  " + theme.Rule.Render(label)
			desc = theme.Dim.Render(a.why)
		} else if i == s.sel {
			btn = theme.AccentFg.Render(theme.GlyphCursor) + " " + theme.Selected.Render(label)
		} else {
			btn = "  " + theme.Base.Render(label)
		}
		if i == s.sel && a.why != "" {
			btn = theme.AccentFg.Render(theme.GlyphCursor) + " " + theme.Rule.Render(label)
		}
		row := btn + " " + fit(theme.Key.Render(a.key), 3) + " " + desc
		out = append(out, s.env.mark("act:"+a.id, trunc(row, w)))
	}
	return out
}

func (s *serverScreen) Footer() []KeyHelp {
	if s.pick != "" {
		return []KeyHelp{{"↑↓", "choose"}, {"enter", "open"}, {"esc", "cancel"}}
	}
	mc, _ := s.machine()
	out := []KeyHelp{{"enter", "run"}, {"d", "deploy"}, {"s", "status"}}
	if canShell(mc) {
		out = append(out, KeyHelp{"o", "shell"})
	}
	return append(out, KeyHelp{"esc", "back"})
}

func (s *serverScreen) Keys() []KeyHelp {
	var out []KeyHelp
	for _, a := range s.actions() {
		d := a.label
		if a.why != "" {
			d += " (" + a.why + ")"
		}
		out = append(out, KeyHelp{a.key, d})
	}
	return append(out, KeyHelp{"r", "check the server again"}, KeyHelp{"esc", "back to servers"})
}

// ── Pair a server ──

// pairScreen stores a token for a server that runs muster
// (`muster fleet pair <name> --token <token>`).
type pairScreen struct {
	env  *Env
	name string
	form *form
	err  string
	task int
}

func newPairScreen(env *Env, name string) *pairScreen {
	return &pairScreen{env: env, name: name, form: newForm([]field{
		{key: "token", label: "Token", kind: fText, help: "from muster auth create on the server"},
	})}
}

func (p *pairScreen) Key() string     { return "pair:" + p.name }
func (p *pairScreen) Title() string   { return "Pair " + p.name }
func (p *pairScreen) Init() tea.Cmd   { return nil }
func (p *pairScreen) Hint() string    { return "" }
func (p *pairScreen) Capturing() bool { return true }
func (p *pairScreen) Close()          { p.env.Tasks.CancelOwner(p.Key()) }

func (p *pairScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if m, ok := msg.(TaskMsg); ok && m.ID == p.task && p.task != 0 {
		switch in := m.Msg.(type) {
		case logEndMsg:
			if in.err != nil {
				p.err = in.err.Error()
				return p, nil
			}
			return p, tea.Batch(toast(lvlOK, p.name+" is paired"), refresh("fleet"), func() tea.Msg { return backMsg{} }, refreshFleetHealth())
		case taskErrMsg:
			p.err = in.Err.Error()
		}
	}
	return p, nil
}

func (p *pairScreen) submit() tea.Cmd {
	tok := strings.TrimSpace(p.form.get("token"))
	if tok == "" {
		p.err = "Paste the token first"
		return nil
	}
	eng := p.env.Eng()
	if eng == nil {
		p.err = "The muster CLI is needed to pair"
		return nil
	}
	p.err = ""
	name := p.name
	id, cmd := p.env.Tasks.Stream("pairing "+name, p.Key(), true, func(ctx context.Context) (<-chan tea.Msg, error) {
		return runQuiet(ctx, eng.Command(ctx, "fleet", "pair", name, "--token", tok))
	})
	p.task = id
	return cmd
}

func (p *pairScreen) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	switch k.String() {
	case "esc":
		if !p.form.editing {
			return p, func() tea.Msg { return backMsg{} }, true
		}
	case "ctrl+s":
		return p, p.submit(), true
	case "enter":
		if p.form.editing {
			p.form.HandleKey(k)
			return p, p.submit(), true
		}
	}
	p.form.HandleKey(k)
	return p, nil, true
}

func (p *pairScreen) View(w, h int) string {
	lines := []string{section("Pair " + p.name), ""}
	lines = append(lines, wrap(p.name+" runs muster itself, so it needs to know this computer is allowed to deploy. On the server, run:", w)...)
	lines = append(lines, "", "  "+code("muster auth create fleet-$(hostname) --scope deploy"), "")
	lines = append(lines, wrap("Then paste the token it prints below and press Enter.", w)...)
	lines = append(lines, "")
	lines = append(lines, p.form.Lines(w, 10, true)...)
	if p.err != "" {
		lines = append(lines, "", theme.Err.Render(theme.GlyphFail+" ")+trunc(p.err, w-2))
	}
	if p.env.Tasks.Running(p.task) {
		lines = append(lines, "", theme.AccentFg.Render(p.env.Spin())+" Pairing…")
	}
	return block(lines, w, h)
}

func (p *pairScreen) Footer() []KeyHelp {
	return []KeyHelp{{"enter", "edit / pair"}, {"ctrl+s", "pair"}, {"esc", "cancel"}}
}

func (p *pairScreen) Keys() []KeyHelp { return p.Footer() }

// refreshFleetHealth asks the app to check every server again.
func refreshFleetHealth() tea.Cmd { return refresh("fleet-health") }

func wrapDim(text string, w int) []string {
	var out []string
	for _, l := range wrap(text, w) {
		out = append(out, theme.Dim.Render(l))
	}
	return out
}
