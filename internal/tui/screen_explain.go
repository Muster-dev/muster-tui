package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/engine"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Fix runner (shared by the explain screen and the deploy failure view) ──

type (
	// fixConfirmedMsg is sent by the confirm overlay; the owner starts the fix.
	fixConfirmedMsg struct{ owner, svc string }
	explainMsg      struct {
		svc string
		ex  engine.Explanation
		err error
	}
)

// confirmFix asks before `muster explain <svc> --fix --yes` runs anything.
func confirmFix(owner, svc string, h *engine.Hint) tea.Cmd {
	body := []string{"Runs muster explain " + svc + " --fix --yes."}
	if h.Fix.Internal {
		body = append(body, "It edits muster.json. Approve the change before the next deploy.")
	} else {
		body = append(body, "It runs: "+h.Fix.Command)
	}
	return func() tea.Msg {
		return confirmMsg{
			title: h.Fix.Label + "?",
			body:  body,
			yes:   "Apply fix",
			onYes: func() tea.Msg { return fixConfirmedMsg{owner: owner, svc: svc} },
		}
	}
}

// startFix streams the fix's output as logBatchMsg / logEndMsg.
func startFix(env *Env, owner, svc string) (int, tea.Cmd) {
	eng := env.Eng()
	return env.Tasks.Stream("applying fix for "+svc, owner, true, func(ctx context.Context) (<-chan tea.Msg, error) {
		lines, wait, err := eng.ExplainFix(ctx, svc)
		if err != nil {
			return nil, err
		}
		out := make(chan tea.Msg, 8)
		go func() {
			defer close(out)
			for l := range lines {
				if strings.TrimSpace(l) != "" {
					out <- logBatchMsg{[]string{strings.TrimSpace(l)}}
				}
			}
			out <- logEndMsg{wait()}
		}()
		return out, nil
	})
}

// loadExplain runs `muster explain <svc> --json` for a screen.
func loadExplain(env *Env, owner, svc string) (int, tea.Cmd) {
	eng := env.Eng()
	return env.Tasks.Run("explaining "+firstNonEmpty(svc, "the last failure"), owner, false, func(ctx context.Context) tea.Msg {
		ex, err := eng.Explain(ctx, svc)
		return explainMsg{svc, ex, err}
	})
}

// hintLines renders "Likely cause / Try" (shared look).
func hintLines(h *engine.Hint, w int) []string {
	var out []string
	if h.Title != "" {
		for i, l := range wrap("Likely cause: "+h.Title, w) {
			if i == 0 {
				l = theme.Title.Render("Likely cause:") + strings.TrimPrefix(l, "Likely cause:")
			}
			out = append(out, l)
		}
	}
	if h.Try != "" {
		for i, l := range wrap("Try: "+h.Try, w) {
			if i == 0 {
				l = theme.Title.Render("Try:") + strings.TrimPrefix(l, "Try:")
			}
			out = append(out, l)
		}
	}
	return out
}

// ── Explain screen ──

// explainScreen shows `muster explain [svc] --json`: the likely cause of the
// last failure, what to try, and a one-key fix when muster has one.
type explainScreen struct {
	env     *Env
	svc     string
	loadID  int
	fixID   int
	loaded  bool
	err     error
	ex      engine.Explanation
	fixOut  []string
	fixErr  error
	fixDone bool
}

func newExplainScreen(env *Env, svc string) *explainScreen { return &explainScreen{env: env, svc: svc} }

func (s *explainScreen) Key() string   { return "explain" }
func (s *explainScreen) Title() string { return "Explain" }
func (s *explainScreen) Close()        { s.env.Tasks.CancelOwner(s.Key()) }

func (s *explainScreen) Init() tea.Cmd {
	id, cmd := loadExplain(s.env, s.Key(), s.svc)
	s.loadID = id
	return cmd
}

func (s *explainScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case fixConfirmedMsg:
		if m.owner != s.Key() {
			return s, nil
		}
		s.fixOut, s.fixErr, s.fixDone = nil, nil, false
		id, cmd := startFix(s.env, s.Key(), m.svc)
		s.fixID = id
		return s, cmd
	case TaskMsg:
		switch {
		case m.ID == s.loadID:
			if in, ok := m.Msg.(explainMsg); ok {
				s.loaded, s.ex, s.err = true, in.ex, in.err
			}
		case m.ID == s.fixID && s.fixID != 0:
			switch in := m.Msg.(type) {
			case logBatchMsg:
				s.fixOut = append(s.fixOut, in.lines...)
			case logEndMsg:
				s.fixErr, s.fixDone = in.err, true
				return s, tea.Batch(func() tea.Msg { return reloadConfigMsg{} }, refresh("hooks"))
			case taskErrMsg:
				s.fixErr, s.fixDone = in.Err, true
			}
		}
	}
	return s, nil
}

func (s *explainScreen) target() string { return firstNonEmpty(s.ex.Service, s.svc) }

func (s *explainScreen) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	switch k.String() {
	case "f":
		if h := s.ex.Hint; h != nil && h.Fix != nil && !s.env.Tasks.Running(s.fixID) {
			return s, confirmFix(s.Key(), s.target(), h), true
		}
	case "l":
		if t := s.target(); t != "" {
			return s, runLine("logs " + t), true
		}
	case "d":
		if t := s.target(); t != "" {
			return s, runLine("deploy " + t), true
		}
	case "esc", "enter":
		return s, func() tea.Msg { return backMsg{} }, true
	default:
		return s, nil, false
	}
	return s, nil, true
}

func exitCodeOf(ex engine.Explanation) int {
	if ex.ExitCode != nil {
		return *ex.ExitCode
	}
	return 0
}

func (s *explainScreen) View(w, h int) string {
	lines := []string{section("Explain")}
	switch {
	case !s.loaded:
		lines = append(lines, "", theme.AccentFg.Render(s.env.Spin())+theme.Dim.Render(" reading the log…"))
		return block(lines, w, h)
	case s.err != nil:
		lines = append(lines, "", theme.Err.Render(theme.GlyphFail+" ")+errText(s.err))
		return block(lines, w, h)
	case s.ex.Reason == "no_logs" || (s.ex.Log == "" && !s.ex.Matched):
		what := "no failed deploys recorded in this project"
		if s.svc != "" {
			what = s.svc + " has no deploy logs"
		}
		lines = append(lines, "", "Nothing to explain yet — "+what+".", theme.Dim.Render("When a deploy fails, explain shows why."))
		return block(append(lines, "", keyHint("Esc", "back")), w, h)
	}
	ex := s.ex
	head := ex.Service + " — " + firstNonEmpty(ex.Action, "deploy") + " failed"
	if ex.Action == "health" {
		head = ex.Service + " — health check failed"
	}
	if c := exitCodeOf(ex); c != 0 {
		head += fmt.Sprintf(" (exit %d)", c)
	}
	lines[0] = spread(section("Explain · "+head), "", w)
	if ex.Log != "" {
		lines = append(lines, theme.Dim.Render("Log: "+relPath(s.env, ex.Log)))
	}
	lines = append(lines, "")
	if !ex.Matched || ex.Hint == nil {
		lines = append(lines, theme.Warning.Render(theme.GlyphWarn+" No known problem pattern matched."),
			theme.Dim.Render("Read the log (l), run doctor, or ask the assistant."))
	} else {
		lines = append(lines, hintLines(ex.Hint, w)...)
		if ex.Hint.Line != "" {
			lines = append(lines, theme.Dim.Render(trunc("Matched: "+ex.Hint.Line, w)))
		}
		if len(ex.Hints) > 1 {
			var ids []string
			for _, x := range ex.Hints[1:] {
				ids = append(ids, x.ID)
			}
			lines = append(lines, theme.Dim.Render(trunc("Also seen in the log: "+strings.Join(ids, ", "), w)))
		}
		if ex.Hint.Docs != "" {
			lines = append(lines, theme.Dim.Render(trunc("More: "+ex.Hint.Docs, w)))
		}
		if ex.Hint.Fix != nil {
			lines = append(lines, "", theme.OK.Render("Fix available: ")+ex.Hint.Fix.Label)
		}
	}
	if len(s.fixOut) > 0 || s.env.Tasks.Running(s.fixID) {
		lines = append(lines, "", rule(w))
		for _, l := range tail(s.fixOut, 6) {
			lines = append(lines, theme.Dim.Render(trunc(l, w)))
		}
		switch {
		case s.env.Tasks.Running(s.fixID):
			lines = append(lines, theme.AccentFg.Render(s.env.Spin())+" Applying the fix…")
		case s.fixErr != nil:
			lines = append(lines, theme.Err.Render(theme.GlyphFail+" ")+errText(s.fixErr))
		case s.fixDone:
			lines = append(lines, theme.OK.Render(theme.GlyphOK+" Fix applied")+theme.Dim.Render(" — deploy again with d"))
		}
	}
	for len(lines) < h-1 {
		lines = append(lines, "")
	}
	lines = append(lines, s.footer())
	return block(lines, w, h)
}

func (s *explainScreen) footer() string {
	var pairs []string
	if h := s.ex.Hint; h != nil && h.Fix != nil {
		pairs = append(pairs, "f", "fix")
	}
	pairs = append(pairs, "l", "logs", "d", "deploy", "Esc", "back")
	return keyHint(pairs...)
}

func (s *explainScreen) Hint() string { return "" }

func (s *explainScreen) Keys() []KeyHelp {
	return []KeyHelp{{"f", "apply the fix (asks first)"}, {"l", "logs"}, {"d", "deploy"}, {"Esc", "back"}}
}
