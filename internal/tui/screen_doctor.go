package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/engine"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// doctorScreen shows `muster doctor --json` with one-key "fix what you can".
type doctorScreen struct {
	env     *Env
	fix     bool
	result  *engine.DoctorResult
	before  *engine.DoctorResult
	err     error
	running bool
	taskID  int
	scroll  int
}

func newDoctorScreen(env *Env, fix bool) *doctorScreen {
	return &doctorScreen{env: env, fix: fix, result: env.State.Doctor}
}

func (s *doctorScreen) Key() string   { return "doctor" }
func (s *doctorScreen) Title() string { return "Doctor" }
func (s *doctorScreen) Close()        { s.env.Tasks.Cancel(s.taskID) }

func (s *doctorScreen) Init() tea.Cmd { return s.run(s.fix) }

func (s *doctorScreen) run(fix bool) tea.Cmd {
	eng := s.env.Eng()
	if eng == nil {
		return nil
	}
	s.running, s.err, s.fix = true, nil, fix
	if fix {
		s.before = s.result
	}
	label := "running doctor"
	if fix {
		label = "fixing what doctor can"
	}
	id, cmd := s.env.Tasks.Run(label, s.Key(), true, func(ctx context.Context) tea.Msg {
		r, err := eng.Doctor(ctx, fix)
		if fix && err == nil {
			// Re-check so the list shows what's left.
			r, err = eng.Doctor(ctx, false)
		}
		return doctorMsg{r, err, fix}
	})
	s.taskID = id
	return cmd
}

func (s *doctorScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case TaskMsg:
		if m.ID != s.taskID {
			return s, nil
		}
		if in, ok := m.Msg.(doctorMsg); ok {
			s.err = in.err
			if in.err == nil {
				r := in.r
				s.result = &r
				if in.fix && s.before != nil {
					fixed := (s.before.Failures + s.before.Warnings) - (r.Failures + r.Warnings)
					if fixed > 0 {
						return s, toast(lvlOK, fmt.Sprintf("Fixed %s", plural(fixed, "issue")))
					}
					return s, toast(lvlInfo, "Nothing more doctor can fix automatically")
				}
			}
		}
	case TaskEndedMsg:
		if m.ID == s.taskID {
			s.running = false
		}
	}
	return s, nil
}

func (s *doctorScreen) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	switch k.String() {
	case "f":
		if !s.running {
			return s, s.run(true), true
		}
	case "r", "R":
		if !s.running {
			return s, s.run(false), true
		}
	case "j", "down":
		s.scroll++
	case "k", "up":
		s.scroll = max(s.scroll-1, 0)
	case "pgdown":
		s.scroll += 10
	case "pgup":
		s.scroll = max(s.scroll-10, 0)
	default:
		return s, nil, false
	}
	return s, nil, true
}

// Status: the counts, or what's running.
func (s *doctorScreen) Status() string {
	if s.running {
		return ""
	}
	r := s.result
	if r == nil {
		return ""
	}
	if r.Warnings == 0 && r.Failures == 0 {
		return theme.OK.Render(theme.GlyphOK + " all fine")
	}
	var parts []string
	if r.Failures > 0 {
		parts = append(parts, theme.Err.Render(fmt.Sprintf("%d failing", r.Failures)))
	}
	if r.Warnings > 0 {
		parts = append(parts, theme.Warning.Render(fmt.Sprintf("%d to check", r.Warnings)))
	}
	return strings.Join(parts, theme.Dim.Render(" · "))
}

// View is a card: how the project is doing as the headline, what needs
// attention first, then what's fine.
func (s *doctorScreen) View(w, h int) string {
	iw := cardInner(w)
	again := []KeyHelp{{"r", "run again"}, {"esc", "back"}}
	if s.running && s.result == nil {
		what := "Checking the project…"
		if s.fix {
			what = "Fixing what doctor can…"
		}
		return card{icon: theme.AccentFg.Render(s.env.Spin()), headline: theme.Title.Render(what), actions: s.env.hints([]KeyHelp{{"esc", "cancel"}})}.view(w, h)
	}
	if s.err != nil && s.result == nil {
		return card{icon: theme.Err.Render(theme.GlyphFail), headline: theme.Err.Render("Doctor didn't finish"), body: wrapDim(errText(s.err), iw), actions: s.env.hints(again)}.view(w, h)
	}
	r := s.result
	if r == nil {
		return card{icon: theme.Dim.Render(theme.GlyphUnknown), headline: theme.Title.Render("No results yet"), actions: s.env.hints(again)}.view(w, h)
	}
	var bad, fine []engine.DoctorCheck
	failed := 0
	for _, c := range r.Checks {
		switch c.Status {
		case "pass":
			fine = append(fine, c)
		case "warn":
			bad = append(bad, c)
		default:
			failed++
			bad = append(bad, c)
		}
	}
	var body []string
	if s.err != nil {
		body = append(body, theme.Err.Render(theme.GlyphFail)+" "+trunc(errText(s.err), iw-2), "")
	}
	if len(bad) > 0 {
		body = append(body, section("Needs attention"))
		for _, c := range bad {
			g := theme.Warning.Render(theme.GlyphWarn)
			if c.Status != "warn" {
				g = theme.Err.Render(theme.GlyphFail)
			}
			body = append(body, trunc(g+" "+c.Name+theme.Dim.Render("  "+c.Category), iw))
			if c.Detail != "" {
				for _, l := range wrap(c.Detail, iw-2) {
					body = append(body, "  "+theme.Dim.Render(l))
				}
			}
		}
		body = append(body, "")
	}
	if len(fine) > 0 {
		body = append(body, section("Fine")+theme.Dim.Render("  "+plural(len(fine), "check")))
		for _, c := range fine {
			row := theme.OK.Render(theme.GlyphOK) + " " + c.Name
			if c.Detail != "" {
				row += theme.Dim.Render(" · " + c.Detail)
			}
			body = append(body, trunc(row, iw))
		}
	}
	c := card{body: body, scroll: s.scroll, sub: plural(len(r.Checks), "check")}
	switch {
	case s.running:
		c.icon, c.headline = theme.AccentFg.Render(s.env.Spin()), theme.Title.Render("Fixing what doctor can…")
	case failed > 0:
		c.icon, c.headline = theme.Err.Render(theme.GlyphFail), theme.Err.Render(plural(len(bad), "check")+" need attention")
	case len(bad) > 0:
		c.icon, c.headline = theme.Warning.Render(theme.GlyphWarn), theme.Warning.Render(plural(len(bad), "warning"))
	default:
		c.icon, c.headline = theme.OK.Render(theme.GlyphOK), theme.OK.Render("Everything looks fine")
	}
	if len(bad) == 1 && failed > 0 {
		c.headline = theme.Err.Render("1 check needs attention")
	}
	keys := []KeyHelp{}
	if len(bad) > 0 {
		keys = append(keys, KeyHelp{"f", "fix what you can"})
	}
	c.actions = s.env.hints(append(keys, again...))
	return c.view(w, h)
}

func (s *doctorScreen) Hint() string { return "" }

func (s *doctorScreen) Footer() []KeyHelp {
	if s.running {
		return []KeyHelp{{"esc", "cancel"}}
	}
	return []KeyHelp{{"f", "fix what you can"}, {"r", "run again"}, {"↑↓", "scroll"}, {"esc", "back"}}
}

func (s *doctorScreen) Keys() []KeyHelp {
	return []KeyHelp{{"f", "fix what doctor can (permissions, stale PIDs, old logs, .dockerignore)"}, {"r", "run again"}, {"↑ ↓", "scroll"}}
}
