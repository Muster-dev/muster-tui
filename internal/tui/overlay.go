package tui

import (
	"strings"

	"github.com/Muster-dev/muster-tui/internal/prompt"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// overlay is a modal drawn over the main pane.
type overlay interface {
	HandleKey(k tea.KeyMsg) (overlay, tea.Cmd) // nil overlay = close
	View(w, h int) string
	Hint() string
}

// ── Confirm ──

type confirm struct{ msg confirmMsg }

func newConfirm(m confirmMsg) *confirm { return &confirm{m} }

func (c *confirm) HandleKey(k tea.KeyMsg) (overlay, tea.Cmd) {
	switch k.String() {
	case "enter", "y":
		return nil, c.msg.onYes
	case "esc", "n", "q":
		return nil, nil
	}
	return c, nil
}

func (c *confirm) View(w, h int) string {
	var lines []string
	lines = append(lines, "", " "+theme.Title.Render(c.msg.title), "")
	for _, b := range c.msg.body {
		for _, l := range wrap(b, w-4) {
			lines = append(lines, "  "+l)
		}
	}
	yes := c.msg.yes
	if yes == "" {
		yes = "Confirm"
	}
	lines = append(lines, "", " "+button(yes, true)+" "+button("Cancel", false))
	return block(lines, w, h)
}

func (c *confirm) Hint() string {
	yes := c.msg.yes
	if yes == "" {
		yes = "confirm"
	}
	return keyHint("Enter", strings.ToLower(yes), "Esc", "cancel")
}

// ── Command palette (Ctrl+K) ──

type palette struct {
	env    *Env
	input  lineInput
	items  []paletteItem
	shown  []paletteItem
	sel    int
	screen Screen
}

type paletteItem struct {
	label, desc, keys, line string
	needsArg                bool
}

func newPalette(env *Env, active Screen) *palette {
	ti := newLineInput()
	ti.Placeholder = "search actions"
	ti.Focus()
	p := &palette{env: env, input: ti, screen: active}
	if active != nil {
		for _, k := range active.Keys() {
			p.items = append(p.items, paletteItem{label: k.Desc, keys: k.Key, desc: active.Title()})
		}
	}
	for _, c := range prompt.Commands {
		if c.Hidden {
			continue
		}
		it := paletteItem{label: c.Name, desc: c.Summary, keys: c.Keys, line: c.Name}
		switch c.Args {
		case prompt.ArgService, prompt.ArgMachine, prompt.ArgContainer, prompt.ArgText:
			it.needsArg = c.Name != "history" && c.Name != "settings" && c.Name != "prune"
		}
		p.items = append(p.items, it)
	}
	p.filter()
	return p
}

func (p *palette) filter() {
	q := strings.ToLower(strings.TrimSpace(p.input.Value()))
	p.shown = p.shown[:0]
	for _, it := range p.items {
		if q == "" || strings.Contains(strings.ToLower(it.label+" "+it.desc+" "+it.keys), q) {
			p.shown = append(p.shown, it)
		}
	}
	p.sel = clamp(p.sel, 0, max(len(p.shown)-1, 0))
}

func (p *palette) HandleKey(k tea.KeyMsg) (overlay, tea.Cmd) {
	switch k.String() {
	case "esc", "ctrl+k":
		return nil, nil
	case "up", "ctrl+p":
		p.sel = clamp(p.sel-1, 0, len(p.shown)-1)
		return p, nil
	case "down", "ctrl+n":
		p.sel = clamp(p.sel+1, 0, len(p.shown)-1)
		return p, nil
	case "enter":
		if p.sel >= len(p.shown) {
			return nil, nil
		}
		it := p.shown[p.sel]
		if it.line == "" {
			// A screen key: replay it on the active screen.
			return nil, func() tea.Msg { return replayKeyMsg{key: it.keys} }
		}
		if it.needsArg {
			return nil, func() tea.Msg { return prefillMsg{text: it.line + " "} }
		}
		return nil, runLine(it.line)
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(k)
	p.filter()
	return p, cmd
}

// replayKeyMsg asks the app to send a key to the active screen.
type replayKeyMsg struct{ key string }

func (p *palette) View(w, h int) string {
	lines := []string{" " + theme.Title.Render("Commands") + theme.Dim.Render("  type to filter"), " " + theme.AccentFg.Render(theme.GlyphPrompt) + " " + p.inputView(w-4), ""}
	rows := h - len(lines)
	start := 0
	if p.sel >= rows {
		start = p.sel - rows + 1
	}
	keyW := 10
	labelW := clamp(w/3, 12, 22)
	for i := start; i < len(p.shown) && len(lines) < h; i++ {
		it := p.shown[i]
		line := "  " + fit(it.label, labelW) + " " + fit(theme.Key.Render(it.keys), keyW) + " " + theme.Dim.Render(it.desc)
		if i == p.sel {
			line = theme.AccentFg.Render(theme.GlyphCursor) + theme.Selected.Render(fit(" "+fit(it.label, labelW)+" "+fit(it.keys, keyW)+" "+it.desc, w-1))
		}
		lines = append(lines, line)
	}
	if len(p.shown) == 0 {
		lines = append(lines, theme.Dim.Render(" nothing matches"))
	}
	return block(lines, w, h)
}

func (p *palette) inputView(w int) string {
	p.input.Width = w
	return p.input.View()
}

func (p *palette) Hint() string {
	return keyHint("↑↓", "select", "Enter", "run", "Esc", "close")
}

// ── Help (?) ──

type help struct {
	env    *Env
	screen Screen
}

func newHelp(env *Env, s Screen) *help { return &help{env, s} }

func (hp *help) HandleKey(k tea.KeyMsg) (overlay, tea.Cmd) {
	switch k.String() {
	case "esc", "?", "q", "enter":
		return nil, nil
	}
	return hp, nil
}

var globalKeys = []KeyHelp{
	{"/", "start a command (Tab completes)"},
	{"↑ ↓", "command history"},
	{"Ctrl+R", "search history"},
	{"Ctrl+K", "command palette"},
	{"Tab", "move focus: prompt → services → main"},
	{"Esc", "cancel task / back"},
	{"Ctrl+O", "full-screen main pane"},
	{"Ctrl+C", "clear · cancel · twice to quit"},
}

func (hp *help) View(w, h int) string {
	var left, right []string
	title := "Help"
	if hp.screen != nil {
		title = "Help · " + hp.screen.Title()
		if keys := hp.screen.Keys(); len(keys) > 0 {
			left = append(left, section("This screen"))
			for _, k := range keys {
				left = append(left, fit(theme.Key.Render(k.Key), 10)+" "+k.Desc)
			}
			left = append(left, "")
		}
	}
	left = append(left, section("Everywhere"))
	for _, k := range globalKeys {
		left = append(left, fit(theme.Key.Render(k.Key), 10)+" "+k.Desc)
	}
	right = append(right, section("Top commands"))
	for _, name := range []string{"deploy", "dry-run", "logs", "rollback", "restart", "status", "docker", "servers", "settings", "doctor", "history", "setup"} {
		c, _ := prompt.Lookup(name)
		uw := 26
		if w >= 120 {
			uw = 30
		}
		right = append(right, fit(code(c.Usage), uw)+" "+theme.Dim.Render(c.Summary))
	}
	lines := []string{" " + theme.Title.Render(title), ""}
	if w >= 100 {
		colW := w/2 - 2
		n := max(len(left), len(right))
		for i := 0; i < n; i++ {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			lines = append(lines, " "+fit(l, colW)+"  "+r)
		}
	} else {
		for _, l := range left {
			lines = append(lines, " "+l)
		}
		lines = append(lines, "")
		for _, r := range right {
			lines = append(lines, " "+r)
		}
	}
	return block(lines, w, h)
}

func (hp *help) Hint() string { return keyHint("Esc", "close", "Ctrl+K", "all actions") }
