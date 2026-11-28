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

// headerView is the top line: where you are (muster › Servers › byricky) on
// the left, the screen's status and background work on the right.
func (m Model) headerView() string {
	left := " " + m.env.mark("hdr:home", theme.Brand.Render("muster"))
	if t := m.activeScreen().Title(); m.active != "dashboard" && t != "" {
		parts := strings.Split(t, " › ")
		left += theme.Rule.Render("  ›  ")
		for i, p := range parts {
			if i == len(parts)-1 {
				left += theme.Base.Render(p)
			} else {
				left += theme.Dim.Render(p + " › ")
			}
		}
	}
	var right []string
	status := ""
	if sr, ok := m.activeScreen().(Statuser); ok && m.overlay == nil {
		status = sr.Status()
	}
	if status == "" {
		status = m.summaryText()
	}
	if status != "" {
		right = append(right, status)
	}
	// Background work; the active screen shows its own tasks in its body.
	var others []*Task
	for _, t := range m.env.Tasks.List() {
		if t.Owner == "" || t.Owner != m.active {
			others = append(others, t)
		}
	}
	taskAt := -1
	if n := len(others); n > 0 {
		label := others[0].Label
		if n > 1 {
			label += fmt.Sprintf(" +%d", n-1)
		}
		taskAt = len(right)
		right = append(right, theme.AccentFg.Render(m.env.Spin())+" "+theme.Dim.Render(trunc(label, 24)))
	}
	if ctx := m.contextText(); ctx != "" && !m.contextInFooter() {
		right = append(right, ctx)
	}
	r := strings.Join(right, "   ") + " "
	if taskAt >= 0 && width(left)+width(r)+1 > m.w {
		// The breadcrumb matters more than what the background is doing.
		right[taskAt] = theme.AccentFg.Render(m.env.Spin())
		r = strings.Join(right, "   ") + " "
	}
	return spread(left, r, m.w)
}

// contextText is where muster is pointed: project, branch, Fleet Cloud.
func (m Model) contextText() string {
	st := m.env.State
	var parts []string
	if st.Project != nil {
		parts = append(parts, theme.Title.Render(st.Project.Name()))
	}
	if st.Branch != "" {
		parts = append(parts, theme.Dim.Render("⎇ "+st.Branch))
	}
	if st.Cloud.LoggedIn {
		parts = append(parts, theme.OK.Render("●")+theme.Dim.Render(" Fleet Cloud"))
	}
	return strings.Join(parts, "  ")
}

// contextInFooter: the context sits on the left of the status bar when it
// fits beside the hints; otherwise it moves up to the header.
func (m Model) contextInFooter() bool {
	ctx := m.contextText()
	if ctx == "" {
		return false
	}
	return width(ctx)+width(m.footerRight())+6 <= m.w
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
	s := m.activeScreen()
	cw, lp := mainW, 0
	if wd, ok := s.(Wide); (!ok || !wd.Wide()) && mainW > contentMax {
		cw = contentMax
		lp = (mainW - cw) / 2
	}
	mainLines := strings.Split(s.View(cw, h), "\n")
	pad := strings.Repeat(" ", lp)
	out := make([]string, h)
	for i := 0; i < h; i++ {
		ml := ""
		if i < len(mainLines) {
			ml = mainLines[i]
		}
		out[i] = " " + fit(pad+fit(ml, cw), mainW) + " "
	}
	if m.overlay != nil {
		m.drawOverlay(out)
	}
	m.drawToast(out)
	return strings.Join(out, "\n")
}

// drawOverlay dims the screen and draws the overlay as a raised panel a
// quarter of the way down: title row, then its content, padded, no border.
func (m Model) drawOverlay(out []string) {
	h := len(out)
	iw, ih := m.overlay.Size(m.w, h)
	padX := 3
	if m.w < 70 {
		padX = 2
	}
	iw = clamp(iw, 1, m.w-2*padX-2)
	title := m.overlay.Title()
	head := 0
	if title != "" {
		head = 2
	}
	ih = clamp(ih, 1, h-2-head)
	lines := make([]string, 0, ih+head)
	if title != "" {
		lines = append(lines, spread(theme.Title.Render(title), m.env.mark("key:esc", theme.Dim.Render("esc")), iw), "")
	}
	lines = append(lines, strings.Split(m.overlay.View(iw, ih), "\n")...)
	for len(lines) < ih+head {
		lines = append(lines, "")
	}
	box := panel(lines[:ih+head], iw+2*padX, padX, 1)
	bw, bh := iw+2*padX, len(box)
	x0 := (m.w - bw) / 2
	y0 := clamp(h/4-1, 0, max(h-bh, 0))
	*m.ovRect = rect{x: x0, y: y0 + 1, w: bw, h: bh} // +1: the header line
	for i := range out {
		out[i] = theme.Rule.Render(ansi.Strip(out[i]))
	}
	for i, bl := range box {
		if y := y0 + i; y < h {
			out[y] = splice(out[y], x0, bl, m.w)
		}
	}
}

// drawToast floats the passing message in a small raised panel at the top
// right of the body, with a bar in its colour.
func (m Model) drawToast(out []string) {
	st := m.env.State
	if st.Toast == "" || !m.env.Now().Before(st.ToastUntil) || len(out) < 4 {
		return
	}
	bar, glyph := theme.Dim, ""
	switch st.ToastLevel {
	case lvlOK:
		bar, glyph = theme.OK, theme.GlyphOK+" "
	case lvlWarn:
		bar, glyph = theme.Warning, theme.GlyphWarn+" "
	case lvlErr:
		bar, glyph = theme.Err, theme.GlyphFail+" "
	}
	maxW := min(60, m.w-6)
	text := wrap(glyph+st.Toast, maxW-4)
	if len(text) > 3 {
		text = append(text[:2], trunc(strings.Join(text[2:], " "), maxW-4))
	}
	tw := 0
	for _, l := range text {
		tw = max(tw, width(l))
	}
	bw := tw + 4
	seq := bgSeq(theme.Element)
	x0 := m.w - bw - 2
	for i, l := range text {
		if i+1 >= len(out) {
			break
		}
		line := bar.Render("┃") + paint(" "+l+"  ", bw-1, seq)
		out[i] = splice(out[i], x0, line, m.w)
	}
}

// footerView is the status bar: context on the left (project, branch,
// Fleet Cloud), what you can do here on the right, ending with help and
// ctrl+k. A passing "press ctrl+c again" takes the left side.
func (m Model) footerView() string {
	left := ""
	switch {
	case m.quitArmed:
		left = theme.Warning.Render("Press ctrl+c again to quit")
	case m.contextInFooter():
		left = m.contextText()
	}
	return spread(" "+left, m.footerRight()+" ", m.w)
}

// footerRight is the hints and, when there's room, ? help and ctrl+k.
func (m Model) footerRight() string {
	hints := m.footerHints()
	right := m.env.mark("key:ctrl+k", theme.Key.Render("ctrl+k")+theme.Dim.Render(" commands"))
	if m.w >= 90 {
		right = m.env.mark("key:?", theme.Key.Render("?")+theme.Dim.Render(" help")) + theme.Dim.Render("  ") + right
	}
	if hints == "" {
		return right
	}
	if width(hints)+width(right)+5 > m.w {
		return hints
	}
	return hints + "    " + right
}

func (m Model) footerHints() string {
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
