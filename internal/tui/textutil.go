package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/Muster-dev/muster-tui/internal/theme"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// width is the printable width of s (ANSI aware).
func width(s string) int { return lipgloss.Width(s) }

// trunc cuts s to w cells, adding an ellipsis when it had to cut.
func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return ansi.Truncate(s, w-1, "") + "…"
}

// fit truncates or right-pads s to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = trunc(s, w)
	if pad := w - width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// fitLeft right-aligns s in w cells.
func fitLeft(s string, w int) string {
	s = trunc(s, w)
	if pad := w - width(s); pad > 0 {
		s = strings.Repeat(" ", pad) + s
	}
	return s
}

// spread puts left and right on one line of width w, truncating left first.
func spread(left, right string, w int) string {
	if w <= 0 {
		return ""
	}
	rw := width(right)
	if rw >= w {
		return trunc(right, w)
	}
	lw := w - rw - 1
	l := trunc(left, lw)
	return l + strings.Repeat(" ", w-width(l)-rw) + right
}

// center pads s to the middle of w.
func center(s string, w int) string {
	s = trunc(s, w)
	pad := (w - width(s)) / 2
	if pad <= 0 {
		return s
	}
	return strings.Repeat(" ", pad) + s
}

// block renders lines into exactly h rows of width w.
func block(lines []string, w, h int) string {
	if h <= 0 || w <= 0 {
		return ""
	}
	out := make([]string, h)
	for i := 0; i < h; i++ {
		if i < len(lines) {
			out[i] = fit(lines[i], w)
		} else {
			out[i] = strings.Repeat(" ", w)
		}
	}
	return strings.Join(out, "\n")
}

// wrap word-wraps text to w cells.
func wrap(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(ansi.Wrap(para, w, " "), "\n")...)
	}
	return out
}

// tail returns the last n items.
func tail[T any](xs []T, n int) []T {
	if n <= 0 {
		return nil
	}
	if len(xs) > n {
		return xs[len(xs)-n:]
	}
	return xs
}

// clamp limits v to [lo, hi].
func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// dur renders a duration compactly: 850ms, 4.2s, 42s, 1m05s, 2h3m.
func dur(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// ago renders "3m ago" relative to now.
func ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < 45*time.Second:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()+0.5))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("Jan 2")
}

// keyHint renders "key action · key action".
func keyHint(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, theme.Key.Render(pairs[i])+" "+theme.Dim.Render(pairs[i+1]))
	}
	return strings.Join(parts, theme.Dim.Render(" · "))
}

// code renders an inline command like `logs api`.
func code(s string) string { return theme.Code.Render(s) }

// section renders a pane heading.
func section(s string) string { return theme.Title.Render(s) }

// rule draws a faint horizontal line.
func rule(w int) string {
	if w <= 0 {
		return ""
	}
	return theme.Rule.Render(strings.Repeat("─", w))
}

// plural renders "1 service" / "3 services".
func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// button renders a choice; the selected one is bracketed so it stays visible
// without colour (NO_COLOR).
func button(label string, selected bool) string {
	if selected {
		return theme.Selected.Render("[" + label + "]")
	}
	return " " + theme.Base.Render(label) + " "
}
