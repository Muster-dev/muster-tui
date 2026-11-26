package tui

import (
	"fmt"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/docker"
	"github.com/Muster-dev/muster-tui/internal/prompt"
	"github.com/Muster-dev/muster-tui/internal/theme"
)

type promptCtx = prompt.Context

const (
	minW = 60
	minH = 16
)

func spinnerFrame(n int) string { return theme.Spinner(n) }

func (m *Model) railWidth() int {
	if m.env.Fullscreen || m.w < 72 {
		return 0
	}
	return clamp(m.w/4, 24, 32)
}

func (m *Model) layout() {
	rw := m.railWidth()
	if rw > 0 {
		m.env.MainW = m.w - rw - 3
	} else {
		m.env.MainW = m.w - 2
	}
	m.env.MainH = max(m.h-5, 1)
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
	bodyH := m.h - 5
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteByte('\n')
	b.WriteString(rule(m.w))
	b.WriteByte('\n')
	b.WriteString(m.bodyView(bodyH))
	b.WriteByte('\n')
	b.WriteString(rule(m.w))
	b.WriteByte('\n')
	b.WriteString(fit(m.suggestionView(), m.w))
	b.WriteByte('\n')
	b.WriteString(fit(m.prompt.View(m.w, m.focus == focusPrompt && m.overlay == nil), m.w))
	return b.String()
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
	left := " " + theme.Brand.Render("muster")
	if st.Project != nil {
		left += "  " + theme.Title.Render(st.Project.Name())
	} else {
		left += "  " + theme.Dim.Render("no project")
	}
	if st.Branch != "" {
		left += "  " + theme.Dim.Render(st.Branch)
	}
	centerTxt := m.summaryText()
	var right []string
	if n := m.env.Tasks.Len(); n > 0 {
		label := ""
		if t := m.env.Tasks.List(); len(t) > 0 {
			label = t[len(t)-1].Label
		}
		right = append(right, theme.AccentFg.Render(m.env.Spin())+" "+theme.Dim.Render(trunc(label, 22)))
	}
	if st.Cloud.LoggedIn {
		right = append(right, theme.Dim.Render("relay ")+theme.OK.Render("●"))
	}
	r := strings.Join(right, "  ") + " "
	lw, cw, rw := width(left), width(centerTxt), width(r)
	if lw+cw+rw+4 > m.w {
		return spread(left, r, m.w)
	}
	gap := m.w - lw - cw - rw
	lpad := gap / 2
	if mid := (m.w-cw)/2 - lw; mid > 1 && mid < gap-1 {
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
		return theme.Dim.Render("checking…")
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
	var mainView string
	if m.overlay != nil {
		mainView = m.overlay.View(mainW, h)
	} else {
		mainView = m.activeScreen().View(mainW, h)
	}
	mainLines := strings.Split(mainView, "\n")
	rw := m.railWidth()
	var railLines []string
	if rw > 0 {
		railLines = m.railLines(rw, h)
	}
	sep := theme.Rule.Render("│")
	popup := m.prompt.PopupLines(m.w - 2)
	if m.overlay != nil || m.focus != focusPrompt {
		popup = nil
	}
	out := make([]string, h)
	for i := 0; i < h; i++ {
		ml := ""
		if i < len(mainLines) {
			ml = mainLines[i]
		}
		if rw > 0 {
			rl := ""
			if i < len(railLines) {
				rl = railLines[i]
			}
			out[i] = fit(rl, rw) + " " + sep + " " + fit(ml, mainW)
		} else {
			out[i] = " " + fit(ml, mainW) + " "
		}
	}
	if len(popup) > 0 {
		start := max(h-len(popup), 0)
		for i, pl := range popup {
			if start+i < h {
				out[start+i] = " " + fit(pl, m.w-2) + " "
			}
		}
	}
	return strings.Join(out, "\n")
}

// railItem is a selectable rail row.
type railItem struct {
	kind string // service, machine, docker, setup
	id   string
	row  int
}

func (m Model) railItems() []railItem {
	var items []railItem
	row := 1
	st := m.env.State
	if st.Project == nil {
		items = append(items, railItem{kind: "setup", row: 2})
		row = 4
	} else {
		for _, k := range st.ServiceKeys() {
			items = append(items, railItem{kind: "service", id: k, row: row})
			row++
		}
		if len(st.ServiceKeys()) == 0 {
			row++
		}
	}
	row += 2 // blank + "Servers"
	if len(st.Fleet.Machines) == 0 {
		row++
	}
	for _, mc := range st.Fleet.Machines {
		items = append(items, railItem{kind: "machine", id: mc.Name, row: row})
		row++
	}
	row += 2
	items = append(items, railItem{kind: "docker", row: row})
	return items
}

func (m Model) railRowToItem(row int) (int, bool) {
	for i, it := range m.railItems() {
		if it.row == row {
			return i, true
		}
	}
	return 0, false
}

func (m Model) railLines(w, h int) []string {
	st := m.env.State
	items := m.railItems()
	selRow := -1
	if m.focus == focusRail && m.railSel < len(items) {
		selRow = items[m.railSel].row
	}
	var lines []string
	add := func(s string) { lines = append(lines, s) }
	add(" " + section("Services"))
	if st.Project == nil {
		add(theme.Dim.Render(" No project in this folder"))
		add(" " + code("setup") + theme.Dim.Render(" to create one"))
		add("")
	} else {
		for _, k := range st.ServiceKeys() {
			add(m.railService(k, w))
		}
		if len(st.ServiceKeys()) == 0 {
			add(theme.Dim.Render(" none configured"))
		}
	}
	add("")
	add(" " + section("Servers"))
	if len(st.Fleet.Machines) == 0 {
		if st.FleetLoaded {
			add(theme.Dim.Render(" none · ") + code("servers"))
		} else {
			add(theme.Dim.Render(" …"))
		}
	}
	for _, mc := range st.Fleet.Machines {
		glyph := theme.Dim.Render(theme.GlyphUnknown)
		add(fit(" "+glyph+" "+fit(mc.Name, 9)+" "+theme.Dim.Render(mc.Transport+" "+mc.Mode), w))
	}
	add("")
	add(" " + section("Docker"))
	add(" " + m.dockerLine())
	if st.Cloud.LoggedIn {
		add("")
		add(" " + section("Cloud"))
		add(" " + theme.OK.Render("●") + " " + theme.Dim.Render(trunc(firstNonEmpty(st.Cloud.Name, st.Cloud.Org), w-4)))
	}
	for i := range lines {
		if i == selRow {
			lines[i] = theme.AccentFg.Render(theme.GlyphCursor) + theme.Selected.Render(fit(strings.TrimPrefix(lines[i], " "), w-1))
		}
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return lines
}

func (m Model) railService(key string, w int) string {
	st := m.env.State
	s := st.Project.Service(key)
	ss := st.Svc(key)
	glyph := healthGlyph(ss.Health)
	if ss.Deploying {
		glyph = theme.AccentFg.Render(m.env.Spin())
	}
	nameW := clamp(w-14, 6, 12)
	meta := s.HealthLabel()
	if s.SkipDeploy {
		meta = "skip"
	}
	line := " " + glyph + " " + fit(key, nameW) + " " + theme.Dim.Render(meta)
	if ss.Deploying {
		line = " " + glyph + " " + fit(key, nameW) + " " + theme.AccentFg.Render("deploying")
	}
	return fit(line, w)
}

func healthGlyph(h string) string {
	switch h {
	case "healthy":
		return theme.OK.Render(theme.GlyphHealthy)
	case "unhealthy":
		return theme.Err.Render(theme.GlyphUnhealthy)
	case "disabled", "no_health_check":
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
