package tui

import (
	"strings"

	"github.com/Muster-dev/muster-tui/internal/theme"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Layout: content gets a comfortable column on wide terminals instead of
// stretching to 200 cells, and panels are filled surfaces (no box borders).
const (
	// contentMax is the widest a reading column gets; wider terminals centre it.
	contentMax = 112
	// splitMin is the width where list screens show master-detail.
	splitMin = 110
	// cardMax is the widest a result card gets.
	cardMax = 92
)

// Wide screens use the whole body width (tables, log streams, two panes);
// the others are drawn in a centred column at most contentMax wide.
type Wide interface{ Wide() bool }

// bgSeq is the escape sequence that sets c as the background ("" in the
// ASCII profile, so surfaces are plain padding there).
func bgSeq(c lipgloss.TerminalColor) string {
	r := lipgloss.NewStyle().Background(c).Render("x")
	if i := strings.Index(r, "x"); i > 0 {
		return r[:i]
	}
	return ""
}

// paint fills one line of exactly w cells with the background seq,
// re-applying it after every reset inside the line so styled spans keep it.
func paint(line string, w int, seq string) string {
	line = fit(line, w)
	if seq == "" {
		return line
	}
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+seq)
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+seq)
	return seq + line + "\x1b[0m"
}

// panel lays lines on a raised surface w cells wide, padX cells in from each
// side and padY blank rows above and below.
func panel(lines []string, w, padX, padY int) []string {
	return panelOn(lines, w, padX, padY, theme.Surface)
}

func panelOn(lines []string, w, padX, padY int, c lipgloss.TerminalColor) []string {
	seq := bgSeq(c)
	inner := max(w-2*padX, 1)
	pad := strings.Repeat(" ", padX)
	out := make([]string, 0, len(lines)+2*padY)
	for i := 0; i < padY; i++ {
		out = append(out, paint("", w, seq))
	}
	for _, l := range lines {
		out = append(out, paint(pad+fit(l, inner)+pad, w, seq))
	}
	for i := 0; i < padY; i++ {
		out = append(out, paint("", w, seq))
	}
	return out
}

// band paints a full-width selected row on a surface (the palette's and
// pickers' selection), marked with ▸ so it reads without colour too.
func band(s string, w int) string {
	return paint(theme.AccentFg.Render(theme.GlyphCursor)+" "+s, w, bgSeq(theme.Element))
}

// splice draws s over line starting at cell x (line is w cells wide).
func splice(line string, x int, s string, w int) string {
	line = fit(line, w)
	left := ansi.Truncate(line, x, "")
	left += strings.Repeat(" ", max(x-width(left), 0))
	right := ansi.TruncateLeft(line, x+width(s), "")
	return left + "\x1b[0m" + s + "\x1b[0m" + right
}

// column centres a w-wide view of lines inside total cells.
func column(lines []string, cw, total int) []string {
	lp := max((total-cw)/2, 0)
	pad := strings.Repeat(" ", lp)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = pad + fit(l, cw)
	}
	return out
}

// card is a result card: a headline, rows and the next actions.
type card struct {
	icon     string // coloured glyph before the headline
	headline string // already styled
	sub      string // a dim line under the headline ("" = none)
	body     []string
	actions  string // hint row (clickable), "" = none
}

// cardWidth is how wide a card is drawn in a w-cell body.
func cardWidth(w int) int { return min(w, cardMax) }

// cardPadX is a card's side padding for a card cw wide.
func cardPadX(cw int) int {
	if cw >= 70 {
		return 3
	}
	return 2
}

// cardInner is the text width inside a card drawn in a w-cell body.
func cardInner(w int) int {
	cw := cardWidth(w)
	return cw - 2*cardPadX(cw)
}

// view renders the card centred in w×h, in the top third.
func (c card) view(w, h int) string {
	cw := cardWidth(w)
	var lines []string
	head := c.headline
	if c.icon != "" {
		head = c.icon + "  " + head
	}
	lines = append(lines, head)
	if c.sub != "" {
		lines = append(lines, theme.Dim.Render(c.sub))
	}
	if len(c.body) > 0 {
		lines = append(lines, "")
		lines = append(lines, c.body...)
	}
	if c.actions != "" {
		lines = append(lines, "", c.actions)
	}
	padY := 1
	if h < 20 {
		padY = 0
	}
	box := panel(lines, cw, cardPadX(cw), padY)
	top := 1
	if free := h - len(box); free > 6 {
		top = free / 3
	}
	if len(box)+top > h {
		top = max(h-len(box), 0)
	}
	out := make([]string, 0, h)
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	out = append(out, column(box, cw, w)...)
	return block(out, w, h)
}
