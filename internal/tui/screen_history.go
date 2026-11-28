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
	err       error
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
	switch m := msg.(type) {
	case TaskEndedMsg:
		if m.ID == s.taskID {
			s.loading = false
		}
	case TaskMsg:
		if hm, ok := m.Msg.(historyMsg); ok && m.ID == s.taskID {
			s.err = hm.err
		}
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
	f := s.Status()
	lines := []string{""}
	rows := s.rows()
	if len(rows) == 0 {
		switch {
		case s.loading:
			lines = append(lines, theme.AccentFg.Render(s.env.Spin())+" Loading history…")
		case s.err != nil:
			lines = append(lines, theme.Err.Render(theme.GlyphFail)+" Couldn't read the history")
			for _, l := range wrap(errText(s.err), w-2) {
				lines = append(lines, "  "+theme.Dim.Render(l))
			}
			lines = append(lines, "", theme.Dim.Render("Press R to try again."))
		case f != "":
			lines = append(lines, theme.Dim.Render("Nothing matches these filters — press c to clear them."))
		default:
			lines = append(lines, theme.Dim.Render("No deploys yet."), "", theme.Dim.Render("Deploys and rollbacks show up here once you run one."))
		}
		return block(lines, w, h)
	}
	svcW := clamp(w/6, 8, 18)
	lines = append(lines, theme.Dim.Render("  "+fit("When", 14)+fit("Service", svcW)+fit("Action", 10)+fit("Result", 12)+"Commit"))
	avail := h - len(lines)
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
		lines = append(lines, trunc(cur+" "+fit(when, 14)+fit(ev.Service, svcW)+fit(ev.Action, 10)+fit(res, 12)+theme.Dim.Render(shortSHA(ev.Commit)), w))
	}
	return block(lines, w, h)
}

// Status: active filters, on the right of the header.
func (s *historyScreen) Status() string {
	var f []string
	if s.svcFilter != "" {
		f = append(f, "service "+s.svcFilter)
	}
	for _, x := range []string{[]string{"", "ok only", "failures only"}[s.status], []string{"", "deploys", "rollbacks"}[s.action]} {
		if x != "" {
			f = append(f, x)
		}
	}
	return theme.Dim.Render(strings.Join(f, " · "))
}

func (s *historyScreen) Hint() string { return "" }

func (s *historyScreen) Footer() []KeyHelp {
	return []KeyHelp{{"enter", "open service"}, {"s", "service"}, {"f", "result"}, {"a", "action"}, {"c", "clear"}}
}
func (s *historyScreen) Keys() []KeyHelp {
	return []KeyHelp{{"j / k", "move"}, {"s", "filter by service"}, {"f", "filter by result"}, {"a", "filter by action"}, {"c", "clear filters"}, {"Enter", "open the service"}, {"R", "reload"}}
}

// Wide: tables and log streams use the whole width.
func (s *historyScreen) Wide() bool { return true }
