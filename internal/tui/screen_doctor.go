package tui

import (
	"context"
	"fmt"

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

func (s *doctorScreen) View(w, h int) string {
	right := ""
	if s.running {
		right = theme.AccentFg.Render(s.env.Spin()) + theme.Dim.Render(" checking")
		if s.fix {
			right = theme.AccentFg.Render(s.env.Spin()) + theme.Dim.Render(" fixing")
		}
	} else if r := s.result; r != nil {
		right = theme.OK.Render(fmt.Sprintf("%d pass", r.Pass)) + theme.Dim.Render(" · ") + theme.Warning.Render(fmt.Sprintf("%d warn", r.Warnings)) + theme.Dim.Render(" · ") + theme.Err.Render(fmt.Sprintf("%d fail", r.Failures))
	}
	lines := []string{spread(section("Doctor"), right, w), ""}
	if s.err != nil {
		lines = append(lines, theme.Err.Render(theme.GlyphFail+" ")+trunc(s.err.Error(), w-2))
	}
	var body []string
	if r := s.result; r != nil {
		cat := ""
		for _, c := range r.Checks {
			if c.Category != cat {
				cat = c.Category
				if len(body) > 0 {
					body = append(body, "")
				}
				body = append(body, theme.Title.Render(firstNonEmpty(cat, "Checks")))
			}
			var tag string
			switch c.Status {
			case "pass":
				tag = theme.OK.Render("PASS")
			case "warn":
				tag = theme.Warning.Render("WARN")
			default:
				tag = theme.Err.Render("FAIL")
			}
			line := " " + tag + "  " + c.Name
			if c.Detail != "" {
				line += theme.Dim.Render(" — " + c.Detail)
			}
			body = append(body, trunc(line, w))
		}
	} else if !s.running {
		body = append(body, theme.Dim.Render("No results yet · r to run"))
	}
	avail := h - len(lines) - 2
	sc := clamp(s.scroll, 0, max(len(body)-avail, 0))
	for i := sc; i < len(body) && i < sc+avail; i++ {
		lines = append(lines, body[i])
	}
	for len(lines) < h-2 {
		lines = append(lines, "")
	}
	lines = append(lines, rule(w), keyHint("f", "fix what you can", "r", "run again", "↑↓", "scroll"))
	return block(lines, w, h)
}

func (s *doctorScreen) Hint() string {
	if s.running {
		return theme.AccentFg.Render(s.env.Spin()) + " Running doctor — " + keyHint("Esc", "cancel")
	}
	return ""
}

func (s *doctorScreen) Keys() []KeyHelp {
	return []KeyHelp{{"f", "fix what doctor can (permissions, stale PIDs, old logs, .dockerignore)"}, {"r", "run again"}, {"↑ ↓", "scroll"}}
}
