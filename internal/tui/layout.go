package tui

import (
	"fmt"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/docker"
	"github.com/Muster-dev/muster-tui/internal/prompt"
	"github.com/Muster-dev/muster-tui/internal/theme"
	"github.com/charmbracelet/x/ansi"
)

type promptCtx = prompt.Context

const (
	minW = 60
	minH = 16
	// chrome: one header line and one footer line.
	chromeH = 2
)

func spinnerFrame(n int) string { return theme.Spinner(n) }

func (m *Model) layout() {
	m.env.MainW = max(m.w-2, 1)
	m.env.MainH = max(m.h-chromeH, 1)
}

// rect is a screen region in cells.
type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// View implements tea.Model.
func (m Model) View() string {
	if m.w == 0 || m.h == 0 {
		return ""
	}
	if m.w < minW || m.h < minH {
		return m.tooSmall()
	}
	m.layout()
	m.env.Zones.begin()
	bodyH := m.h - chromeH
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteByte('\n')
	b.WriteString(m.bodyView(bodyH))
	b.WriteByte('\n')
	b.WriteString(m.footerView())
	return m.env.Zones.scan(b.String())
}

func (m Model) tooSmall() string {
	lines := []string{
		theme.Brand.Render("muster"),
		"",
		"Terminal too small",
		theme.Dim.Render(fmt.Sprintf("%d×%d — need at least %d×%d", m.w, m.h, minW, minH)),
	}
	var out []string
	top := max((m.h-len(lines))/2, 0)
	for i := 0; i < m.h; i++ {
		j := i - top
		if j >= 0 && j < len(lines) {
			out = append(out, fit(center(lines[j], m.w), m.w))
		} else {
			out = append(out, strings.Repeat(" ", m.w))
		}
	}
	return strings.Join(out, "\n")
}

func (m Model) headerView() string {
	st := m.env.State
	left := " " + m.env.mark("hdr:home", theme.Brand.Render("muster"))
	if st.Project != nil {
		left += "  " + theme.Title.Render(st.Project.Name())
	}
	if st.Branch != "" {
		left += "  " + theme.Dim.Render("⎇ "+st.Branch)
	}
	if t := m.activeScreen().Title(); m.active != "dashboard" && t != "" {
		left += theme.Dim.Render("  ›  ") + theme.Base.Render(t)
	}
	centerTxt := m.summaryText()
	var right []string
	if sr, ok := m.activeScreen().(Statuser); ok && m.overlay == nil {
		if st := sr.Status(); st != "" {
			right = append(right, st)
			centerTxt = ""
		}
	}
	// Background work; the active screen shows its own tasks in its body.
	var others []*Task
	for _, t := range m.env.Tasks.List() {
		if t.Owner == "" || t.Owner != m.active {
			others = append(others, t)
		}
	}
	if n := len(others); n > 0 {
		label := others[0].Label
		if n > 1 {
			label += fmt.Sprintf(" +%d", n-1)
		}
		right = append(right, theme.AccentFg.Render(m.env.Spin())+" "+theme.Dim.Render(trunc(label, 24)))
	}
	if st.Cloud.LoggedIn {
		right = append(right, theme.OK.Render("●")+theme.Dim.Render(" Fleet Cloud"))
	}
	r := strings.Join(right, "   ") + " "
	lw, cw, rw := width(left), width(centerTxt), width(r)
	if centerTxt == "" || lw+cw+rw+6 > m.w {
		return spread(left, r, m.w)
	}
	gap := m.w - lw - cw - rw
	lpad := gap / 2
	if mid := (m.w-cw)/2 - lw; mid > 2 && mid < gap-2 {
		lpad = mid
	}
	return left + strings.Repeat(" ", lpad) + centerTxt + strings.Repeat(" ", gap-lpad) + r
}

func (m Model) summaryText() string {
	st := m.env.State
	if st.Project == nil {
		return ""
	}
	if !st.StatusLoaded && st.AuthErr == nil {
		if m.env.Engine == nil {
			return theme.Dim.Render("muster CLI not found")
		}
		return ""
	}
	if st.AuthErr != nil {
		return theme.Warning.Render("not paired")
	}
	h, u, d, _ := st.Counts()
	var parts []string
	if h > 0 {
		parts = append(parts, theme.OK.Render(fmt.Sprintf("%d healthy", h)))
	}
	if u > 0 {
		parts = append(parts, theme.Err.Render(fmt.Sprintf("%d unhealthy", u)))
	}
	if d > 0 {
		parts = append(parts, theme.AccentFg.Render(fmt.Sprintf("%d deploying", d)))
	}
	if len(parts) == 0 {
		return theme.Dim.Render(plural(len(st.ServiceKeys()), "service"))
	}
	return strings.Join(parts, theme.Dim.Render(" · "))
}

func (m Model) bodyView(h int) string {
	mainW := m.env.MainW
	mainLines := strings.Split(m.activeScreen().View(mainW, h), "\n")
	out := make([]string, h)
	for i := 0; i < h; i++ {
		ml := ""
		if i < len(mainLines) {
			ml = mainLines[i]
		}
		out[i] = " " + fit(ml, mainW) + " "
	}
	if m.overlay != nil {
		m.drawOverlay(out)
	}
	return strings.Join(out, "\n")
}

// drawOverlay dims the screen and draws the overlay in a centred box.
func (m Model) drawOverlay(out []string) {
	h := len(out)
	iw, ih := m.overlay.Size(m.w, h)
	iw = clamp(iw, 1, m.w-2)
	ih = clamp(ih, 1, h-2)
	bw, bh := iw+2, ih+2
	x0 := (m.w - bw) / 2
	y0 := max((h-bh)/3, 0)
	if _, ok := m.overlay.(*palette); ok {
		y0 = min(2, max(h-bh, 0))
	}
	*m.ovRect = rect{x: x0, y: y0 + 1, w: bw, h: bh} // +1: the header line
	for i := range out {
		out[i] = theme.Rule.Render(ansi.Strip(out[i]))
	}
	content := strings.Split(m.overlay.View(iw, ih), "\n")
	border := theme.Rule
	if _, ok := m.overlay.(*palette); ok {
		border = theme.AccentFg
	}
	title := m.overlay.Title()
	top := strings.Repeat("─", iw)
	if title != "" && iw > len(title)+4 {
		top = "─ " + theme.Title.Render(title) + border.Render(" "+strings.Repeat("─", iw-width(title)-3))
	}
	box := make([]string, 0, bh)
	box = append(box, border.Render("╭")+border.Render(top)+border.Render("╮"))
	for i := 0; i < ih; i++ {
		l := ""
		if i < len(content) {
			l = content[i]
		}
		box = append(box, border.Render("│")+fit(l, iw)+border.Render("│"))
	}
	box = append(box, border.Render("╰"+strings.Repeat("─", iw)+"╯"))
	for i, bl := range box {
		y := y0 + i
		if y >= h {
			break
		}
		bg := ansi.Strip(out[y])
		left := ansi.Truncate(bg, x0, "")
		left += strings.Repeat(" ", max(x0-width(left), 0))
		right := ansi.TruncateLeft(bg, x0+bw, "")
		out[y] = theme.Rule.Render(left) + bl + theme.Rule.Render(right)
	}
}

// footerView is the bottom line: what you can do here (clickable), or a
// passing message, with ctrl+k on the right.
func (m Model) footerView() string {
	right := m.env.mark("key:ctrl+k", theme.Key.Render("ctrl+k")+theme.Dim.Render(" commands")) + " "
	if m.w >= 90 {
		right = m.env.mark("key:?", theme.Key.Render("?")+theme.Dim.Render(" help")) + theme.Dim.Render("  ") + right
	}
	return spread(" "+m.footerLeft(), right, m.w)
}

func (m Model) footerLeft() string {
	st := m.env.State
	if m.quitArmed {
		return theme.Warning.Render("Press ctrl+c again to quit")
	}
	if st.Toast != "" && m.env.Now().Before(st.ToastUntil) {
		style := theme.Base
		glyph := ""
		switch st.ToastLevel {
		case lvlOK:
			style, glyph = theme.OK, theme.GlyphOK+" "
		case lvlWarn:
			style, glyph = theme.Warning, theme.GlyphWarn+" "
		case lvlErr:
			style, glyph = theme.Err, theme.GlyphFail+" "
		}
		return style.Render(glyph) + st.Toast
	}
	if m.overlay != nil {
		return m.overlay.Hint()
	}
	s := m.activeScreen()
	if f, ok := s.(Footerer); ok {
		if keys := f.Footer(); len(keys) > 0 {
			return m.hintRow(keys)
		}
	}
	if h := s.Hint(); h != "" {
		return h
	}
	if m.active != "dashboard" {
		// A few of the screen's own keys, and the way back.
		var keys []KeyHelp
		for _, k := range s.Keys() {
			if len(keys) == 4 {
				break
			}
			if replayable(k.Key) && k.Key != "?" {
				keys = append(keys, KeyHelp{k.Key, shortDesc(k.Desc)})
			}
		}
		return m.hintRow(append(keys, KeyHelp{"esc", "back"}))
	}
	return stateHint(m.env)
}

// hintRow renders "key label · key label", each one clickable.
func (m Model) hintRow(keys []KeyHelp) string {
	var parts []string
	for _, k := range keys {
		// "enter/esc" replays its first key.
		id := k.Key
		if i := strings.Index(id, "/"); i > 0 {
			id = id[:i]
		}
		parts = append(parts, m.env.mark("key:"+id, theme.Key.Render(k.Key)+" "+theme.Dim.Render(k.Desc)))
	}
	return strings.Join(parts, theme.Dim.Render(" · "))
}

// Statuser screens show a short status on the right of the header line
// ("✓ done", "● following"), so they never repeat their title in the body.
type Statuser interface{ Status() string }

// Footerer screens list their footer hints (at most ~5, most useful first).
type Footerer interface{ Footer() []KeyHelp }

func healthGlyph(h string) string {
	switch h {
	case "healthy":
		return theme.OK.Render(theme.GlyphHealthy)
	case "unhealthy":
		return theme.Err.Render(theme.GlyphUnhealthy)
	case "disabled", "no_health_check", "not_deployed":
		return theme.Dim.Render(theme.GlyphDisabled)
	}
	return theme.Dim.Render("◌")
}

func healthWord(h string) string {
	switch h {
	case "healthy":
		return theme.OK.Render("healthy")
	case "unhealthy":
		return theme.Err.Render("unhealthy")
	case "disabled":
		return theme.Dim.Render("check off")
	case "no_health_check":
		return theme.Dim.Render("no check")
	case "not_deployed":
		return theme.Dim.Render("not deployed")
	}
	return theme.Dim.Render("unknown")
}

func (m Model) dockerLine() string {
	st := m.env.State
	switch {
	case !st.DockerLoaded:
		return theme.Dim.Render("…")
	case st.DockerErr == docker.ErrNotInstalled:
		return theme.Dim.Render("not installed")
	case st.DockerErr != nil:
		return theme.Warning.Render("not running")
	}
	s := fmt.Sprintf("%d/%d running", st.Docker.Running, st.Docker.Total)
	if st.Docker.ImagesSize != "" {
		s += " · " + st.Docker.ImagesSize
	}
	return theme.Dim.Render(s)
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// shortDesc keeps the first few words of a help line for the footer.
func shortDesc(s string) string {
	if i := strings.IndexAny(s, "(—·,"); i > 0 {
		s = s[:i]
	}
	f := strings.Fields(s)
	if len(f) > 3 {
		f = f[:3]
	}
	return strings.Join(f, " ")
}
