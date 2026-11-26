package tui

import (
	"context"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/engine"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// historyScreen is the deploy/rollback timeline with filters.
type historyScreen struct {
	env       *Env
	svcFilter string
	status    int // 0 all, 1 ok, 2 failed
	action    int // 0 all, 1 deploy, 2 rollback
	sel       int
	taskID    int
	loading   bool
}

func newHistoryScreen(env *Env) *historyScreen { return &historyScreen{env: env} }

func (s *historyScreen) Key() string   { return "history" }
func (s *historyScreen) Title() string { return "History" }

func (s *historyScreen) Init() tea.Cmd {
	eng := s.env.Eng()
	if eng == nil || s.env.State.Project == nil {
		return nil
	}
	s.loading = true
	id, cmd := s.env.Tasks.Run("loading history", s.Key(), false, func(ctx context.Context) tea.Msg {
		evs, err := eng.History(ctx, true, "")
		return historyMsg{evs, err}
	})
	s.taskID = id
	return cmd
}

func (s *historyScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if m, ok := msg.(TaskEndedMsg); ok && m.ID == s.taskID {
		s.loading = false
	}
	return s, nil
}

func (s *historyScreen) rows() []engine.HistoryEvent {
	var out []engine.HistoryEvent
	evs := s.env.State.History
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		if s.svcFilter != "" && ev.Service != s.svcFilter {
			continue
		}
		if s.status == 1 && !ev.OK() || s.status == 2 && ev.OK() {
			continue
		}
		if s.action == 1 && ev.Action != "deploy" || s.action == 2 && ev.Action != "rollback" {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func (s *historyScreen) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	n := len(s.rows())
	switch k.String() {
	case "j", "down":
		s.sel = clamp(s.sel+1, 0, n-1)
	case "k", "up":
		s.sel = clamp(s.sel-1, 0, n-1)
	case "pgdown":
		s.sel = clamp(s.sel+10, 0, n-1)
	case "pgup":
		s.sel = clamp(s.sel-10, 0, n-1)
	case "s":
		keys := append([]string{""}, s.env.State.ServiceKeys()...)
		s.svcFilter = cycle(keys, s.svcFilter)
		s.sel = 0
	case "f":
		s.status = (s.status + 1) % 3
		s.sel = 0
	case "a":
		s.action = (s.action + 1) % 3
		s.sel = 0
	case "c":
		s.svcFilter, s.status, s.action, s.sel = "", 0, 0, 0
	case "enter":
		if rows := s.rows(); s.sel < len(rows) {
			return s, runLine("open " + rows[s.sel].Service), true
		}
	case "R":
		return s, s.Init(), true
	default:
		return s, nil, false
	}
	return s, nil, true
}

func (s *historyScreen) View(w, h int) string {
	now := s.env.Now()
	var filters []string
	if s.svcFilter != "" {
		filters = append(filters, "service "+s.svcFilter)
	}
	filters = append(filters, []string{"", "ok only", "failures only"}[s.status], []string{"", "deploys", "rollbacks"}[s.action])
	var f []string
	for _, x := range filters {
		if x != "" {
			f = append(f, x)
		}
	}
	right := theme.Dim.Render(strings.Join(f, " · "))
	if s.loading {
		right = theme.AccentFg.Render(s.env.Spin()) + " " + right
	}
	lines := []string{spread(section("History"), right, w), ""}
	rows := s.rows()
	if len(rows) == 0 {
		msg := "No deploys yet."
		if len(f) > 0 {
			msg = "Nothing matches these filters · c clears them"
		}
		lines = append(lines, theme.Dim.Render(msg))
		return block(lines, w, h)
	}
	lines = append(lines, theme.Dim.Render("  "+fit("WHEN", 14)+fit("SERVICE", 14)+fit("ACTION", 10)+fit("RESULT", 10)+"COMMIT"))
	avail := h - len(lines) - 2
	start := clamp(s.sel-avail+1, 0, max(len(rows)-avail, 0))
	for i := start; i < len(rows) && i < start+avail; i++ {
		ev := rows[i]
		res := theme.OK.Render(theme.GlyphOK + " ok")
		if !ev.OK() {
			res = theme.Err.Render(theme.GlyphFail + " " + firstNonEmpty(ev.Status, "failed"))
		}
		when := ago(ev.Time(), now)
		if t := ev.Time(); !t.IsZero() && now.Sub(t).Hours() > 20 {
			when = t.Format("Jan 2 15:04")
		}
		cur := " "
		if i == s.sel {
			cur = theme.AccentFg.Render(theme.GlyphCursor)
		}
		lines = append(lines, cur+" "+fit(when, 14)+fit(ev.Service, 14)+fit(ev.Action, 10)+fit(res, 10)+theme.Dim.Render(shortSHA(ev.Commit)))
	}
	for len(lines) < h-2 {
		lines = append(lines, "")
	}
	lines = append(lines, rule(w), keyHint("s", "service", "f", "result", "a", "action", "c", "clear", "Enter", "open service"))
	return block(lines, w, h)
}

func (s *historyScreen) Hint() string { return "" }
func (s *historyScreen) Keys() []KeyHelp {
	return []KeyHelp{{"j / k", "move"}, {"s", "filter by service"}, {"f", "filter by result"}, {"a", "filter by action"}, {"c", "clear filters"}, {"Enter", "open the service"}, {"R", "reload"}}
}
