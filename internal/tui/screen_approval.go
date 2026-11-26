package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/config"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// approvalScreen lists hooks changed since this machine approved them, shows
// a coloured diff, and runs `muster hooks approve` on Approve.
type approvalScreen struct {
	env     *Env
	then    string // command to run after approval ("deploy api", "retry")
	changes []config.HookChange
	sel     int
	scroll  int
	phase   int // 0 review, 1 approving, 2 failed
	out     []string
	err     error
}

func newApproval(env *Env, then string) *approvalScreen {
	s := &approvalScreen{env: env, then: strings.TrimSpace(then)}
	if env.State.Project != nil {
		s.changes = env.State.Project.HookChanges()
	}
	return s
}

func (s *approvalScreen) Key() string   { return "approval" }
func (s *approvalScreen) Title() string { return "Approve hooks" }
func (s *approvalScreen) Init() tea.Cmd { return nil }

// approvalResultMsg arrives after `muster hooks approve` returns.
type approvalResultMsg struct{ err error }

// approve suspends the TUI and runs `muster hooks approve`: the CLI asks for
// your password (sudo) before re-signing, so it needs the terminal.
func (s *approvalScreen) approve() tea.Cmd {
	eng := s.env.Eng()
	if eng == nil {
		return toast(lvlErr, "The muster CLI is needed to approve hooks")
	}
	s.phase, s.out, s.err = 1, nil, nil
	cmd := eng.Interactive("hooks", "approve")
	return func() tea.Msg {
		return execMsg{cmd: cmd, after: func(err error) tea.Msg { return approvalResultMsg{err} }}
	}
}

func (s *approvalScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case reloadConfigMsg:
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
		if len(s.changes) == 0 {
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
		lines = append(lines, section("Approve hooks"), "", theme.OK.Render(theme.GlyphOK)+" Every hook matches what you approved.", "", keyHint("Esc", "back"))
		return block(lines, w, h)
	}
	intro := fmt.Sprintf("%s changed since you approved them on this machine. Read the diff — hooks run with your permissions.", plural(len(s.changes), "file"))
	lines = append(lines, section("Approve hooks"))
	lines = append(lines, wrap(intro, w)...)
	lines = append(lines, "")
	listH := min(len(s.changes), 6)
	start := clamp(s.sel-listH+1, 0, max(len(s.changes)-listH, 0))
	for i := start; i < start+listH; i++ {
		c := s.changes[i]
		kind := map[string]string{"new": theme.OK.Render("new"), "changed": theme.Warning.Render("changed"), "removed": theme.Err.Render("removed")}[c.Kind]
		row := fit(c.Label(), 30) + " " + kind
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
		footer = append(footer, theme.Err.Render(theme.GlyphFail+" ")+errText(s.err))
		for _, l := range tail(s.out, 2) {
			footer = append(footer, theme.Dim.Render("  "+trunc(l, w-2)))
		}
	default:
		footer = append(footer, button("Approve", true)+" "+button("Abort", false)+"   "+theme.Dim.Render("a approve (asks for your password) · Esc abort · e edit"))
	}
	diffH := h - len(lines) - len(footer) - 1
	if s.sel < len(s.changes) && diffH > 0 {
		d := s.diffLines(s.changes[s.sel])
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

func (s *approvalScreen) Hint() string {
	if s.phase == 1 {
		return theme.AccentFg.Render(s.env.Spin()) + " Approving hooks…"
	}
	if s.then != "" {
		return theme.Warning.Render(theme.GlyphWarn+" ") + "Approve to continue with " + code(s.then) + theme.Dim.Render(" · ") + keyHint("a", "approve", "Esc", "abort")
	}
	return keyHint("j/k", "file", "PgDn", "scroll diff", "a", "approve", "e", "edit", "Esc", "abort")
}

func (s *approvalScreen) Keys() []KeyHelp {
	return []KeyHelp{{"j / k", "next / previous file"}, {"PgUp PgDn", "scroll the diff"}, {"a / Enter", "approve all"}, {"e", "open file in $EDITOR"}, {"Esc", "abort"}}
}
