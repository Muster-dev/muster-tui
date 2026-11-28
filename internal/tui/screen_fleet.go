package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/fleet"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// fleetResultScreen shows the answer of a one-shot fleet command — a
// connection test or a status check — as a native card instead of the
// CLI's text.
type fleetResultScreen struct {
	env    *Env
	kind   string // "test" or "status"
	target string // server, group or "" for every server
	task   int

	running bool
	err     error
	test    *fleet.TestReport
	status  []fleet.MachineStatus
}

type fleetTestMsg struct {
	r   fleet.TestReport
	err error
}

func newFleetResult(env *Env, kind, target string) *fleetResultScreen {
	return &fleetResultScreen{env: env, kind: kind, target: target}
}

func (s *fleetResultScreen) Key() string { return "fleet-" + s.kind + ":" + s.target }

func (s *fleetResultScreen) what() string {
	if s.kind == "status" {
		return "Status"
	}
	return "Connection test"
}

func (s *fleetResultScreen) Title() string {
	switch {
	case s.target == "":
		return "Servers › " + s.what()
	case s.isGroup():
		return "Servers › group " + s.target + " › " + s.what()
	}
	return "Servers › " + s.target + " › " + s.what()
}

func (s *fleetResultScreen) isGroup() bool {
	if _, ok := s.env.State.Fleet.Find(s.target); ok {
		return false
	}
	for _, g := range s.env.State.Fleet.Groups {
		if g.Name == s.target {
			return true
		}
	}
	return false
}

func (s *fleetResultScreen) Close() { s.env.Tasks.CancelOwner(s.Key()) }
func (s *fleetResultScreen) Hint() string {
	return ""
}

// doing is the spinner text: "Testing byricky…".
func (s *fleetResultScreen) doing() string {
	who := s.target
	if who == "" {
		who = "every server"
	} else if s.isGroup() {
		who = "the servers in " + who
	}
	if s.kind == "status" {
		return "Asking " + who + " how it's doing…"
	}
	return "Testing " + who + "…"
}

func (s *fleetResultScreen) Init() tea.Cmd {
	eng := s.env.Eng()
	if eng == nil {
		s.err = fmt.Errorf("the muster CLI isn't installed")
		return nil
	}
	s.running, s.err = true, nil
	target := s.target
	label := strings.TrimSuffix(strings.ToLower(s.doing()), "…")
	if s.kind == "status" {
		id, cmd := s.env.Tasks.Run(label, s.Key(), true, func(ctx context.Context) tea.Msg {
			st, err := fleet.Status(ctx, eng, target)
			return fleetHealthMsg{st, err}
		})
		s.task = id
		return cmd
	}
	useJSON := s.env.State.Caps.FleetTestJSON
	machines := s.env.State.Fleet.Machines
	id, cmd := s.env.Tasks.Run(label, s.Key(), true, func(ctx context.Context) tea.Msg {
		r, err := fleet.RunTest(ctx, eng, target, useJSON, machines)
		return fleetTestMsg{r, err}
	})
	s.task = id
	return cmd
}

func (s *fleetResultScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case TaskMsg:
		if m.ID != s.task || s.task == 0 {
			return s, nil
		}
		switch in := m.Msg.(type) {
		case fleetTestMsg:
			s.err = in.err
			if in.err == nil {
				r := in.r
				s.test = &r
			}
		case fleetHealthMsg:
			s.err = in.err
			if in.err == nil {
				s.status = in.st
			}
		case taskErrMsg:
			s.err = in.Err
		}
	case TaskEndedMsg:
		if m.ID == s.task {
			s.running = false
		}
	}
	return s, nil
}

func (s *fleetResultScreen) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	switch k.String() {
	case "enter", "left", "h":
		if s.running {
			return s, nil, true
		}
		return s, func() tea.Msg { return backMsg{} }, true
	case "esc":
		if s.running {
			s.env.Tasks.Cancel(s.task)
			s.running = false
			s.err = fmt.Errorf("cancelled")
			return s, nil, true
		}
		return s, func() tea.Msg { return backMsg{} }, true
	case "r":
		if s.running {
			return s, nil, true
		}
		s.test, s.status = nil, nil
		return s, s.Init(), true
	case "d":
		if s.running || !isMachineName(s.env, s.target) {
			return s, nil, false
		}
		return s, serverAction(s.env, "deploy", s.target), true
	case "o":
		if s.running || !isMachineName(s.env, s.target) {
			return s, nil, false
		}
		return s, serverAction(s.env, "open", s.target), true
	case "y":
		if s.running {
			return s, nil, true
		}
		text := stripANSI(strings.Join(s.lines(80), "\n"))
		return s, copyCmd(strings.TrimSpace(text), "Copied the result"), true
	}
	return s, nil, false
}

// Status is shown on the right of the header: only a failure to run, since
// the card's own summary line says how the servers did.
func (s *fleetResultScreen) Status() string {
	if !s.running && s.err != nil {
		return theme.Err.Render(theme.GlyphFail + " failed")
	}
	return ""
}

func (s *fleetResultScreen) pairing() int {
	n := 0
	if s.test != nil {
		for _, t := range s.test.Servers {
			if t.Pairing {
				n++
			}
		}
	}
	return n
}

// View is a card in the top third: the answer as a headline, a row per
// server, and what to do next.
func (s *fleetResultScreen) View(w, h int) string {
	return s.card(w).view(w, h)
}

func (s *fleetResultScreen) card(w int) card {
	iw := cardInner(w)
	again := []KeyHelp{{"r", "run again"}, {"enter", "back"}}
	switch {
	case s.running:
		return card{icon: theme.AccentFg.Render(s.env.Spin()), headline: theme.Title.Render(s.doing()), actions: s.env.hints([]KeyHelp{{"esc", "cancel"}})}
	case s.err != nil && s.err.Error() == "cancelled":
		return card{icon: theme.Dim.Render(theme.GlyphSkip), headline: theme.Title.Render("Cancelled"), actions: s.env.hints(again)}
	case s.err != nil:
		body := wrapDim(fleet.ServerWords(errText(s.err)), iw)
		body = append(append(body, ""), s.nextStep(iw)...)
		return card{icon: theme.Err.Render(theme.GlyphFail), headline: theme.Err.Render(s.failedWhat()), body: body, actions: s.env.hints(again)}
	case s.test != nil:
		c := card{body: s.testRows(iw), sub: s.subline(len(s.test.Servers), "tested")}
		r := s.test
		switch {
		case len(r.Servers) == 0:
			c.icon, c.headline = theme.Dim.Render(theme.GlyphUnknown), theme.Title.Render(s.testSummary())
		case r.Failed > 0:
			c.icon, c.headline = theme.Err.Render(theme.GlyphFail), theme.Err.Render(s.testSummary())
		case s.pairing() > 0:
			c.icon, c.headline = theme.Warning.Render(theme.GlyphWarn), theme.Warning.Render(s.testSummary())
			c.body = append(c.body, "", theme.Dim.Render("Open the server and press p to pair it."))
		default:
			c.icon, c.headline = theme.OK.Render(theme.GlyphOK), theme.OK.Render(s.testSummary())
		}
		c.actions = s.env.hints(s.nextKeys(r.Failed == 0 && s.pairing() == 0))
		return c
	case s.status != nil:
		c := card{body: s.statusRows(iw), sub: s.subline(len(s.status), "checked")}
		if sum := s.statusSummary(); s.allOnline() {
			c.icon, c.headline = theme.OK.Render(theme.GlyphOK), theme.OK.Render(sum)
		} else {
			c.icon, c.headline = theme.Warning.Render(theme.GlyphWarn), theme.Warning.Render(sum)
		}
		c.actions = s.env.hints(s.nextKeys(s.allOnline()))
		return c
	}
	return card{headline: theme.Dim.Render("Nothing to show yet"), actions: s.env.hints(again)}
}

// subline: "2 servers · tested just now".
func (s *fleetResultScreen) subline(n int, verb string) string {
	who := plural(n, "server")
	if s.target != "" && s.isGroup() {
		who = "group " + s.target + " · " + who
	}
	return who + " · " + verb + " " + s.env.Now().Format("15:04")
}

// nextKeys: run again, back, and deploy when one healthy server was tested.
func (s *fleetResultScreen) nextKeys(ok bool) []KeyHelp {
	out := []KeyHelp{{"r", "run again"}, {"enter", "back"}}
	switch {
	case !isMachineName(s.env, s.target):
	case ok:
		out = append(out, KeyHelp{"d", "deploy " + s.target})
	default:
		out = append(out, KeyHelp{"o", "open " + s.target})
	}
	return append(out, KeyHelp{"y", "copy"})
}

func isMachineName(env *Env, name string) bool {
	_, ok := env.State.Fleet.Find(name)
	return ok
}

func (s *fleetResultScreen) allOnline() bool {
	for _, ms := range s.status {
		if !ms.Reachable || ms.Error == "unpaired" {
			return false
		}
	}
	return true
}

func (s *fleetResultScreen) lines(w int) []string {
	lines := []string{""}
	switch {
	case s.running:
		return append(lines, theme.AccentFg.Render(s.env.Spin())+" "+s.doing())
	case s.err != nil && s.err.Error() == "cancelled":
		return append(lines, theme.Dim.Render("Cancelled."), "", theme.Dim.Render("Press r to run it again."))
	case s.err != nil:
		lines = append(lines, theme.Err.Render(theme.GlyphFail)+" "+s.failedWhat())
		for _, l := range wrap(fleet.ServerWords(errText(s.err)), w-2) {
			lines = append(lines, "  "+theme.Dim.Render(l))
		}
		lines = append(lines, "")
		return append(lines, s.nextStep(w)...)
	case s.test != nil:
		return append(append(lines, s.testSummary(), ""), s.testRows(w)...)
	case s.status != nil:
		return append(append(lines, s.statusSummary(), ""), s.statusRows(w)...)
	}
	return append(lines, theme.Dim.Render("Nothing to show yet — press r."))
}

func (s *fleetResultScreen) failedWhat() string {
	if s.kind == "status" {
		return "Couldn't check the servers"
	}
	return "Couldn't run the connection test"
}

func (s *fleetResultScreen) nextStep(w int) []string {
	msg := "Press r to try again."
	if !s.env.State.Cloud.LoggedIn && s.anyCloud() {
		msg = "Servers on Fleet Cloud need you signed in — run cloud login, then press r."
	}
	return wrapDim(msg, w)
}

func (s *fleetResultScreen) anyCloud() bool {
	for _, m := range s.env.State.Fleet.Machines {
		if m.Transport == "cloud" {
			return true
		}
	}
	return false
}

// testSummary: "All servers passed", "1 of 2 failed".
func (s *fleetResultScreen) testSummary() string {
	r := s.test
	total := len(r.Servers)
	bad := total - r.Passed
	switch {
	case total == 0:
		return "No servers to test"
	case bad == 0:
		return "All servers passed"
	case r.Failed == 0:
		return fmt.Sprintf("%d of %d %s pairing", bad, total, map[bool]string{true: "needs", false: "need"}[bad == 1])
	}
	return fmt.Sprintf("%d of %d failed", r.Failed, total)
}

func transportWord(t string) string {
	if t == "cloud" {
		return "Fleet Cloud"
	}
	if t == "" {
		return ""
	}
	return "SSH"
}

func (s *fleetResultScreen) testRows(w int) []string {
	r := s.test
	if len(r.Servers) == 0 {
		return wrapDim("The test didn't report any servers. Add one from Servers (a), then press r.", w)
	}
	nameW := 6
	for _, t := range r.Servers {
		nameW = max(nameW, width(t.Name))
	}
	nameW = min(nameW, 20)
	viaW := 11
	descW := max(w-nameW-viaW-6, 10)
	var out []string
	for _, t := range r.Servers {
		glyph := theme.OK.Render(theme.GlyphHealthy)
		desc := s.okDesc(t)
		style := theme.Dim
		switch {
		case t.Pairing:
			glyph, style = theme.Warning.Render(theme.GlyphWarn), theme.Warning
			desc = firstNonEmpty(t.Error, "Needs pairing")
		case !t.OK:
			glyph, style = theme.Err.Render(theme.GlyphUnhealthy), theme.Err
			desc = firstNonEmpty(fleet.ServerWords(t.Error), "Failed")
		}
		descLines := wrap(desc, descW)
		if len(descLines) == 0 {
			descLines = []string{""}
		}
		row := glyph + " " + fit(t.Name, nameW) + "  " + theme.Dim.Render(fit(transportWord(t.Transport), viaW)) + "  " + style.Render(descLines[0])
		out = append(out, s.env.mark("srv:"+t.Name, row))
		for _, more := range descLines[1:] {
			out = append(out, strings.Repeat(" ", nameW+viaW+6)+style.Render(more))
		}
	}
	return out
}

// okDesc: "Connected · muster OK · 120 ms".
func (s *fleetResultScreen) okDesc(t fleet.TestServer) string {
	parts := []string{"Connected"}
	switch t.Muster {
	case "ok":
		parts = append(parts, "muster OK")
	case "skip":
		if t.Mode == "push" {
			parts = append(parts, "hooks sent over SSH")
		}
	}
	if t.LatencyMS > 0 {
		parts = append(parts, fmt.Sprintf("%d ms", t.LatencyMS))
	}
	return strings.Join(parts, " · ")
}

func (s *fleetResultScreen) statusRows(w int) []string {
	if len(s.status) == 0 {
		return wrapDim("No servers answered. Add one from Servers (a), then press r.", w)
	}
	nameW := 6
	for _, ms := range s.status {
		nameW = max(nameW, width(ms.Machine))
	}
	nameW = min(nameW, 20)
	var out []string

	for i, ms := range s.status {
		if i > 0 {
			out = append(out, "")
		}
		var glyph, word string
		switch {
		case !ms.Reachable:
			glyph, word = theme.Err.Render(theme.GlyphUnhealthy), theme.Err.Render(srvOffline)
		case ms.Error == "unpaired":
			glyph, word = theme.Warning.Render(theme.GlyphWarn), theme.Warning.Render(srvPairing)
		default:

			glyph, word = theme.OK.Render(theme.GlyphHealthy), theme.OK.Render(srvOnline)
		}
		var meta []string
		if mc, ok := s.env.State.Fleet.Find(ms.Machine); ok {
			meta = append(meta, transportWord(mc.Transport))
		}
		if ms.Healthy != nil && ms.Total != nil && *ms.Total > 0 {
			meta = append(meta, fmt.Sprintf("%d/%d healthy", *ms.Healthy, *ms.Total))
		}
		if ms.MusterVersion != "" {
			meta = append(meta, "muster "+strings.TrimPrefix(ms.MusterVersion, "v"))
		}
		row := glyph + " " + fit(ms.Machine, nameW) + "  " + fit(word, 15) + theme.Dim.Render(strings.Join(meta, " · "))
		out = append(out, s.env.mark("srv:"+ms.Machine, trunc(row, w)))
		if ms.Error != "" && ms.Error != "unpaired" {
			for _, l := range wrap(fleet.ServerWords(ms.Error), w-4) {
				out = append(out, "  "+theme.Dim.Render(l))
			}
		}
		svcW := 6
		for _, sv := range ms.Services {
			svcW = max(svcW, width(sv.Service))
		}
		svcW = min(svcW, 18)
		for _, sv := range ms.Services {
			r := "  " + serviceGlyph(sv) + " " + fit(sv.Service, svcW) + "  " + serviceWord(sv)
			if sv.Detail != "" && strings.ToLower(sv.Status) != "healthy" {
				r += theme.Dim.Render(" · " + sv.Detail)
			}
			out = append(out, trunc(r, w))
		}
	}
	return out
}

// statusSummary: "All servers online", "2 of 3 online".
func (s *fleetResultScreen) statusSummary() string {
	online := 0
	for _, ms := range s.status {
		if ms.Reachable && ms.Error != "unpaired" {

		}
	}
	switch {
	case online == len(s.status) && online == 1:
		return "Online"
	case online == len(s.status):
		return "All servers online"
	}
	return fmt.Sprintf("%d of %d online", online, len(s.status))
}

func (s *fleetResultScreen) Click(id string, double bool) (Screen, tea.Cmd, bool) {
	if name, ok := zoneArg(id, "srv:"); ok && !s.running {
		if _, found := s.env.State.Fleet.Find(name); found && name != s.target {
			return s, nav(newServerScreen(s.env, name)), true
		}
	}
	return s, nil, false
}

func (s *fleetResultScreen) Footer() []KeyHelp {
	if s.running {
		return []KeyHelp{{"esc", "cancel"}}
	}
	return []KeyHelp{{"enter/esc", "back"}, {"r", "run again"}, {"y", "copy"}}
}

func (s *fleetResultScreen) Keys() []KeyHelp {
	return []KeyHelp{{"enter / esc", "back"}, {"r", "run again"}, {"y", "copy the result"}, {"esc", "cancel while running"}}
}
