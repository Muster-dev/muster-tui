package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/config"
	"github.com/Muster-dev/muster-tui/internal/engine"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// approvalScreen lists hooks changed since this machine approved them, shows
// a coloured diff, and approves them. On muster ≥ 0.7 the list comes from
// `muster hooks status --json` and Approve (after a confirm) runs `muster
// hooks approve --yes --json`; older CLIs get the local manifest check and
// an interactive `muster hooks approve`.
type approvalScreen struct {
	env       *Env
	then      string // command to run after approval ("deploy api", "retry")
	changes   []config.HookChange
	sel       int
	scroll    int
	phase     int // 0 review, 1 approving, 2 failed
	out       []string
	err       error
	v07       bool
	loading   bool
	loadID    int
	approveID int
}

func newApproval(env *Env, then string) *approvalScreen {
	s := &approvalScreen{env: env, then: strings.TrimSpace(then), v07: env.V07()}
	switch {
	case s.v07:
		s.changes = env.State.HookChanges
		s.loading = true
	case env.State.Project != nil:
		s.changes = env.State.Project.HookChanges()
	}
	return s
}

func (s *approvalScreen) Key() string   { return "approval" }
func (s *approvalScreen) Title() string { return "Approve hooks" }
func (s *approvalScreen) Close()        { s.env.Tasks.CancelOwner(s.Key()) }

func (s *approvalScreen) Init() tea.Cmd {
	if !s.v07 {
		return nil
	}
	return s.reload()
}

// reload asks the CLI for the current approval state.
func (s *approvalScreen) reload() tea.Cmd {
	eng := s.env.Eng()
	if eng == nil {
		s.loading = false
		return nil
	}
	s.loading = true
	id, cmd := s.env.Tasks.Run("checking hooks", s.Key(), false, func(ctx context.Context) tea.Msg {
		hs, err := eng.HooksStatus(ctx)
		return hooksMsg{hs, err}
	})
	s.loadID = id
	return cmd
}

type (
	// approvalResultMsg arrives after an interactive `muster hooks approve`.
	approvalResultMsg struct{ err error }
	// approveConfirmedMsg: the user said yes in the confirm overlay.
	approveConfirmedMsg struct{}
	approveDoneMsg      struct {
		r   engine.ApproveResult
		err error
	}
)

func (s *approvalScreen) keys() []string {
	out := make([]string, len(s.changes))
	for i, c := range s.changes {
		out[i] = c.Label()
	}
	return out
}

// confirmApprove asks before approving (muster ≥ 0.7 approves without a
// password, so this confirm is the only gate).
func (s *approvalScreen) confirmApprove() tea.Cmd {
	n := len(s.changes)
	body := []string{approvedFilesNote(s.keys(), 6),
		"muster records the approval for this machine only (~/.muster/trust). Hooks run with your permissions."}
	for _, c := range s.changes {
		if len(c.Dangerous) > 0 {
			body = append(body, theme.GlyphWarn+" "+c.Label()+" contains a risky command: "+strings.Join(c.Dangerous, "; "))
		}
	}
	return func() tea.Msg {
		return confirmMsg{title: "Approve " + plural(n, "file") + " on this machine?", body: body, yes: "Approve",
			onYes: func() tea.Msg { return approveConfirmedMsg{} }}
	}
}

func (s *approvalScreen) approveNow() tea.Cmd {
	eng := s.env.Eng()
	s.phase, s.out, s.err = 1, nil, nil
	id, cmd := s.env.Tasks.Run("approving hooks", s.Key(), true, func(ctx context.Context) tea.Msg {
		r, err := eng.HooksApprove(ctx)
		return approveDoneMsg{r, err}
	})
	s.approveID = id
	return cmd
}

// approve starts the approval: a confirm on muster ≥ 0.7, otherwise the
// interactive CLI (it asks for your password before re-signing).
func (s *approvalScreen) approve() tea.Cmd {
	eng := s.env.Eng()
	if eng == nil {
		return toast(lvlErr, "The muster CLI is needed to approve hooks")
	}
	if s.v07 {
		return s.confirmApprove()
	}
	s.phase, s.out, s.err = 1, nil, nil
	cmd := eng.Interactive("hooks", "approve")
	return func() tea.Msg {
		return execMsg{cmd: cmd, after: func(err error) tea.Msg { return approvalResultMsg{err} }}
	}
}

func (s *approvalScreen) approved() tea.Cmd {
	cmds := []tea.Cmd{toast(lvlOK, "Hooks approved"), func() tea.Msg { return backMsg{} }}
	if s.then != "" {
		cmds = append(cmds, runLine(s.then))
	}
	return tea.Sequence(cmds...)
}

func (s *approvalScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case approveConfirmedMsg:
		return s, s.approveNow()
	case TaskMsg:
		switch {
		case m.ID == s.loadID && s.loadID != 0:
			if in, ok := m.Msg.(hooksMsg); ok {
				s.loading = false
				if in.err != nil {
					s.err = in.err
					return s, nil
				}
				s.changes = hookChangesFrom(s.env.State.Project, in.hs)
				s.env.State.Hooks, s.env.State.HookChanges = &in.hs, s.changes
				s.sel = clamp(s.sel, 0, max(len(s.changes)-1, 0))
			}
		case m.ID == s.approveID && s.approveID != 0:
			in, ok := m.Msg.(approveDoneMsg)
			if !ok {
				return s, nil
			}
			if in.err != nil {
				s.phase, s.err, s.out = 2, in.err, nil
				for _, f := range engine.DangerousFiles(in.err) {
					s.out = append(s.out, f.Key+": "+f.Issue)
				}
				return s, nil
			}
			s.env.State.HookChanges = nil
			return s, tea.Batch(s.approved(), refresh("hooks"))
		}
	case reloadConfigMsg:
		if s.v07 && s.phase == 0 {
			return s, s.reload()
		}
		if s.env.State.Project != nil && s.phase == 0 {
			s.changes = s.env.State.Project.HookChanges()
			s.sel = clamp(s.sel, 0, max(len(s.changes)-1, 0))
		}
	case approvalResultMsg:
		if s.env.State.Project == nil {
			return s, nil
		}
		left := s.env.State.Project.HookChanges()
		s.env.State.HookChanges = left
		if len(left) > 0 {
			s.phase, s.changes = 2, left
			s.err = fmt.Errorf("hooks are still not approved")
			if m.err != nil {
				s.err = fmt.Errorf("muster hooks approve failed: %v", m.err)
			}
			return s, nil
		}
		cmds := []tea.Cmd{toast(lvlOK, "Hooks approved"), func() tea.Msg { return backMsg{} }}
		if s.then != "" {
			cmds = append(cmds, runLine(s.then))
		}
		return s, tea.Sequence(cmds...)
	}
	return s, nil
}

func (s *approvalScreen) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	if s.phase == 1 {
		return s, nil, false
	}
	switch k.String() {
	case "j", "down":
		s.sel = clamp(s.sel+1, 0, len(s.changes)-1)
		s.scroll = 0
	case "k", "up":
		s.sel = clamp(s.sel-1, 0, len(s.changes)-1)
		s.scroll = 0
	case "pgdown", " ", "ctrl+d":
		s.scroll += max(s.env.MainH/2, 1)
	case "pgup", "ctrl+u":
		s.scroll = max(s.scroll-max(s.env.MainH/2, 1), 0)
	case "a", "enter", "y":
		if s.loading && len(s.changes) == 0 {
			return s, nil, true
		}
		if len(s.changes) == 0 {
			if s.then != "" {
				return s, s.approved(), true // nothing left to approve: carry on
			}
			return s, func() tea.Msg { return backMsg{} }, true
		}
		return s, s.approve(), true
	case "e":
		if s.sel < len(s.changes) {
			path := s.changes[s.sel].Path
			return s, editPath(path), true
		}
	case "esc", "n", "q":
		msg := "Hooks not approved"
		if strings.HasPrefix(s.then, "deploy") || s.then == "retry" {
			msg += " — deploy stopped"
		}
		return s, tea.Batch(toast(lvlWarn, msg), func() tea.Msg { return backMsg{} }), true
	default:
		return s, nil, false
	}
	return s, nil, true
}

func editPath(path string) tea.Cmd {
	return func() tea.Msg {
		editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
		parts := strings.Fields(editor)
		return execMsg{cmd: newExec(parts[0], append(parts[1:], path)...), after: func(err error) tea.Msg { return reloadConfigMsg{} }}
	}
}

// diffLines renders the selected change.
func (s *approvalScreen) diffLines(c config.HookChange) []string {
	read := func(p string) []string {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		return splitLines(string(data))
	}
	var lines []string
	switch c.Kind {
	case "new":
		lines = append(lines, theme.Dim.Render("New — not approved on this machine yet"))
		for _, l := range read(c.Path) {
			lines = append(lines, "+"+l)
		}
	case "removed":
		lines = append(lines, theme.Dim.Render("Removed since approval"))
		for _, l := range read(c.Approved) {
			lines = append(lines, "-"+l)
		}
	default:
		if c.Approved == "" {
			lines = append(lines, theme.Dim.Render("Changed. No approved copy to compare with (approved by an older muster) — current file:"))
			for _, l := range read(c.Path) {
				lines = append(lines, " "+l)
			}
		} else {
			lines = append(lines, unifiedDiff(read(c.Approved), read(c.Path), 3)...)
		}
	}
	return lines
}

func styleDiff(l string, w int) string {
	l = trunc(strings.ReplaceAll(l, "\t", "    "), w)
	switch {
	case strings.HasPrefix(l, "@@"):
		return theme.AccentFg.Render(l)
	case strings.HasPrefix(l, "+"):
		return theme.OK.Render(l)
	case strings.HasPrefix(l, "-"):
		return theme.Err.Render(l)
	}
	return theme.Base.Render(l)
}

func (s *approvalScreen) View(w, h int) string {
	var lines []string
	if len(s.changes) == 0 {
		lines = append(lines, "")
		switch {
		case s.loading:
			lines = append(lines, theme.AccentFg.Render(s.env.Spin())+" Asking muster which hooks need approval…")
		case s.err != nil && s.phase == 0:
			lines = append(lines, theme.Err.Render(theme.GlyphFail)+" Couldn't read the hook approvals")
			for _, l := range wrap(errText(s.err), w-2) {
				lines = append(lines, "  "+theme.Dim.Render(l))
			}
			lines = append(lines, "", theme.Dim.Render("Approve in a terminal instead: muster hooks approve"))
		default:
			lines = append(lines, theme.OK.Render(theme.GlyphOK)+" Every hook matches what you approved.")
			if s.then != "" {
				lines = append(lines, "", theme.Dim.Render("Press enter to continue with "+s.then+"."))
			}
		}
		return block(lines, w, h)
	}
	intro := fmt.Sprintf("%s changed since you approved them on this computer. Read the diff — hooks run with your permissions.", plural(len(s.changes), "file"))
	lines = append(lines, "")
	lines = append(lines, wrapDim(intro, w)...)
	lines = append(lines, "")
	listH := min(len(s.changes), 6)
	start := clamp(s.sel-listH+1, 0, max(len(s.changes)-listH, 0))
	for i := start; i < start+listH; i++ {
		c := s.changes[i]
		kind := map[string]string{"new": theme.OK.Render("new"), "changed": theme.Warning.Render("changed"), "removed": theme.Err.Render("removed")}[c.Kind]
		row := fit(c.Label(), 30) + " " + fit(kind, 8)
		if len(c.Dangerous) > 0 {
			row += " " + theme.Err.Render(theme.GlyphWarn+" risky command")
		}
		if i == s.sel {
			lines = append(lines, theme.AccentFg.Render(theme.GlyphCursor)+" "+theme.Title.Render(row))
		} else {
			lines = append(lines, "  "+row)
		}
	}
	if len(s.changes) > listH {
		lines = append(lines, theme.Dim.Render(fmt.Sprintf("  %d more · j/k", len(s.changes)-listH)))
	}
	lines = append(lines, rule(w))
	footer := []string{}
	switch s.phase {
	case 1:
		footer = append(footer, theme.AccentFg.Render(s.env.Spin())+" Waiting for muster hooks approve…")
	case 2:
		footer = append(footer, theme.Err.Render(theme.GlyphFail+" ")+trunc(errText(s.err), w-2))
		for _, l := range tail(s.out, 3) {
			footer = append(footer, theme.Dim.Render("  "+trunc(l, w-2)))
		}
		if len(s.out) > 0 {
			footer = append(footer, theme.Dim.Render("  Edit the hook (e), or approve it in a terminal: muster hooks approve"))
		}
	default:
		how := "a approve (asks for your password)"
		if s.v07 {
			how = "a approve (asks first)"
		}
		footer = append(footer, button("Approve", true)+" "+button("Abort", false)+"   "+theme.Dim.Render(how))
	}
	diffH := h - len(lines) - len(footer) - 1
	if s.sel < len(s.changes) && diffH > 0 {
		d := s.diffLines(s.changes[s.sel])
		if dg := s.changes[s.sel].Dangerous; len(dg) > 0 {
			var warn []string
			for _, x := range dg {
				warn = append(warn, theme.Err.Render(theme.GlyphWarn+" Risky: "+x))
			}
			d = append(warn, d...)
		}
		sc := clamp(s.scroll, 0, max(len(d)-diffH, 0))
		for _, l := range d[sc:min(len(d), sc+diffH)] {
			lines = append(lines, styleDiff(l, w))
		}
	}
	for len(lines) < h-len(footer) {
		lines = append(lines, "")
	}
	lines = append(lines, footer...)
	return block(lines, w, h)
}

func (s *approvalScreen) Hint() string { return "" }

// Status: what approving unlocks, on the right of the header.
func (s *approvalScreen) Status() string {
	if s.then != "" && len(s.changes) > 0 {
		return theme.Warning.Render(theme.GlyphWarn + " approve to continue with " + s.then)
	}
	if len(s.changes) > 0 {
		return theme.Dim.Render(plural(len(s.changes), "file") + " to review")
	}
	return ""
}

func (s *approvalScreen) Footer() []KeyHelp {
	switch {
	case s.phase == 1:
		return []KeyHelp{{"esc", "cancel"}}
	case len(s.changes) == 0 && s.then != "" && !s.loading:
		return []KeyHelp{{"enter", "continue"}, {"esc", "back"}}
	case len(s.changes) == 0:
		return []KeyHelp{{"esc", "back"}}
	}
	return []KeyHelp{{"a", "approve"}, {"e", "edit"}, {"j/k", "file"}, {"pgdown", "scroll diff"}, {"esc", "abort"}}
}

func (s *approvalScreen) Keys() []KeyHelp {
	return []KeyHelp{{"j / k", "next / previous file"}, {"PgUp PgDn", "scroll the diff"}, {"a / Enter", "approve all"}, {"e", "open file in $EDITOR"}, {"Esc", "abort"}}
}
