package tui

import (
	"strings"

	"github.com/Muster-dev/muster-tui/internal/prompt"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// promptBox is the command line: input, completion popup, history and
// reverse history search.
type promptBox struct {
	input lineInput
	hist  *prompt.History
	comp  prompt.Completion
	sel   int  // selected popup item; -1 = none chosen yet
	popup bool // popup visible

	searching bool
	query     string
	hitIdx    int
	hit       string

	errInput   string // input that failed to parse
	errMsg     string
	suggestion string // corrected command for that input
}

const popupMax = 6

func newPromptBox(hist *prompt.History) promptBox {
	ti := newLineInput()
	ti.Placeholder = "type a command — deploy, logs api, docker, help"
	ti.Focus()
	return promptBox{input: ti, hist: hist, sel: -1}
}

func (p *promptBox) Value() string { return p.input.Value() }

func (p *promptBox) SetValue(s string) {
	p.input.SetValue(s)
	p.input.CursorEnd()
}

func (p *promptBox) Clear() {
	p.SetValue("")
	p.popup = false
	p.sel = -1
	p.errInput, p.suggestion = "", ""
	p.hist.Reset()
}

func (p *promptBox) refresh(ctx prompt.Context) {
	v := p.input.Value()
	p.sel = -1
	if strings.TrimSpace(v) == "" && v != "/" {
		p.popup = false
		return
	}
	p.comp = prompt.Complete(v, ctx)
	items := p.comp.Items
	p.popup = len(items) > 0
	// Hide when the word is already exactly the only candidate.
	if len(items) == 1 && strings.TrimSpace(p.comp.Apply(v, 0)) == strings.TrimSpace(v) {
		p.popup = false
	}
}

// accept applies the selected (or first) completion. Returns true if the
// input changed.
func (p *promptBox) accept(ctx prompt.Context) bool {
	if !p.popup || len(p.comp.Items) == 0 {
		return false
	}
	i := p.sel
	if i < 0 {
		i = 0
	}
	before := p.input.Value()
	p.SetValue(p.comp.Apply(before, i))
	p.refresh(ctx)
	return p.input.Value() != before
}

func (p *promptBox) move(delta int) {
	n := len(p.comp.Items)
	if n == 0 {
		return
	}
	if p.sel < 0 {
		if delta > 0 {
			p.sel = 0
		} else {
			p.sel = n - 1
		}
		return
	}
	p.sel = (p.sel + delta + n) % n
}

// startSearch enters Ctrl+R mode.
func (p *promptBox) startSearch() {
	p.searching = true
	p.query = ""
	p.hitIdx = len(p.hist.Items())
	p.hit = ""
	p.popup = false
}

// searchKey handles a key in search mode; returns done=true when the search
// ended (accept or cancel).
func (p *promptBox) searchKey(k tea.KeyMsg) {
	switch k.Type {
	case tea.KeyCtrlR:
		if i, s := p.hist.Search(p.query, p.hitIdx); i >= 0 {
			p.hitIdx, p.hit = i, s
		}
	case tea.KeyEsc, tea.KeyCtrlC, tea.KeyCtrlG:
		p.searching = false
	case tea.KeyEnter, tea.KeyTab, tea.KeyRight:
		p.searching = false
		if p.hit != "" {
			p.SetValue(p.hit)
		}
	case tea.KeyBackspace:
		if len(p.query) > 0 {
			r := []rune(p.query)
			p.query = string(r[:len(r)-1])
		}
		p.hitIdx = len(p.hist.Items())
		p.hitIdx, p.hit = p.hist.Search(p.query, p.hitIdx)
		if p.hitIdx < 0 {
			p.hitIdx = len(p.hist.Items())
		}
	case tea.KeyRunes, tea.KeySpace:
		p.query += string(k.Runes)
		if k.Type == tea.KeySpace {
			p.query += " "
		}
		idx, s := p.hist.Search(p.query, len(p.hist.Items()))
		if idx >= 0 {
			p.hitIdx, p.hit = idx, s
		} else {
			p.hit = ""
		}
	}
}

// View renders the prompt line.
func (p *promptBox) View(w int, focused bool) string {
	glyph := theme.AccentFg.Render(theme.GlyphPrompt)
	if !focused {
		glyph = theme.Dim.Render(theme.GlyphPrompt)
	}
	if p.searching {
		left := " " + theme.Dim.Render("history search ") + theme.Base.Render(p.query) + theme.AccentFg.Render("▏")
		right := theme.Dim.Render("Enter use · Ctrl+R older · Esc cancel")
		if p.hit != "" {
			left += "  " + theme.Code.Render(p.hit)
		} else if p.query != "" {
			left += "  " + theme.Dim.Render("no match")
		}
		return spread(left, right+" ", w)
	}
	right := theme.Dim.Render("/ commands · ? help ")
	if p.input.Value() != "" {
		right = theme.Dim.Render("Tab complete · Enter run ")
	}
	iw := w - 4 - width(right)
	if iw < 10 {
		iw = w - 4
		right = ""
	}
	p.input.Width = iw
	if !focused {
		p.input.Blur()
	} else {
		p.input.Focus()
	}
	return spread(" "+glyph+" "+p.input.View(), right, w)
}

// PopupLines renders the completion popup (bottom-aligned, top first).
func (p *promptBox) PopupLines(w int) []string {
	if !p.popup || p.searching {
		return nil
	}
	items := p.comp.Items
	start := 0
	if p.sel >= popupMax {
		start = p.sel - popupMax + 1
	}
	end := min(len(items), start+popupMax)
	labelW := 0
	for _, it := range items[start:end] {
		labelW = max(labelW, width(it.Label))
	}
	labelW = min(labelW, max(12, w/3))
	var out []string
	for i := start; i < end; i++ {
		it := items[i]
		line := "   " + fit(it.Label, labelW) + "  " + theme.Dim.Render(it.Desc)
		if i == p.sel {
			line = " " + theme.AccentFg.Render(theme.GlyphCursor) + theme.Selected.Render(fit(" "+fit(it.Label, labelW)+"  "+it.Desc, w-2))
		}
		out = append(out, fit(line, w))
	}
	if len(items) > end {
		out = append(out, theme.Dim.Render(fit("   … "+plural(len(items)-end, "more"), w)))
	}
	return out
}
