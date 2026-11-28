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
func (s *serverScreen) Update(tea.Msg) (Screen, tea.Cmd) { return s, nil }
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
		serverAct{id: "logs", label: "Logs", key: "l", why: "not available yet — muster has no fleet logs command"},
		serverAct{id: "restart", label: "Restart", key: "R", why: "not available yet — muster has no fleet restart command"},
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

func (s *serverScreen) run(a serverAct) tea.Cmd {
	if a.why != "" {
		return toast(lvlInfo, a.label+" is "+a.why)
	}
	switch a.id {
	case "ask":
		return func() tea.Msg { return prefillMsg{text: "ask about server " + s.name + ": "} }
	}
	return serverAction(s.env, a.id, s.name)
}

func (s *serverScreen) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
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
		lines := []string{section(s.name), ""}
		if !s.env.State.FleetLoaded {
			lines = append(lines, theme.AccentFg.Render(s.env.Spin())+theme.Dim.Render(" loading…"))
		} else {
			lines = append(lines, "There's no server called "+s.name+" any more.")
		}
		return block(lines, w, h)
	}
	ss := s.env.serverStatus(mc)
	right := ss.Glyph + " " + ss.Word
	if ss.Services != "" {
		right += theme.Dim.Render(" · ") + ss.Services
	}
	lines := []string{spread(theme.Brand.Render(s.name), right, w)}
	conn := connection(mc)
	if mc.Transport == "cloud" {
		conn += theme.Dim.Render(" · agent " + mc.Target())
	}
	lines = append(lines, conn)
	meta := runsHow(mc)
	if mc.Path != "" {
		meta += " · " + mc.Path
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
	actLines := s.actionLines(w)
	if w >= 110 {
		// Services on the left, actions on the right.
		colW := w/2 - 2
		left := append([]string{"", section("Services")}, svcLines...)
		rightCol := append([]string{"", section("Actions")}, s.actionLines(w-colW-4)...)
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
	lines = append(lines, "", section("Actions"))
	lines = append(lines, actLines...)
	return block(lines, w, h)
}

func (s *serverScreen) serviceLines(w int, ss serverState) []string {
	if ss.Status == nil {
		if s.env.CheckingServers() {
			return []string{theme.AccentFg.Render(s.env.Spin()) + theme.Dim.Render(" asking the server…")}
		}
		if ss.Pairing {
			return []string{theme.Dim.Render("Pair the server to see its services.")}
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
