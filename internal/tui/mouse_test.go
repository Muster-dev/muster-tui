package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Muster-dev/muster-tui/internal/fleet"
	tea "github.com/charmbracelet/bubbletea"
)

func serverDeps(t *testing.T) Deps {
	d := baseDeps(t)
	d.Project = nil
	d.Cwd = t.TempDir()
	return d
}

var testFleet = fleetMsg{inv: fleet.Inventory{Machines: []fleet.Machine{
	{Name: "cloud-1", Host: "agent-east", User: "deploy", Transport: "cloud", Mode: "muster"},
	{Name: "prod-1", Host: "10.0.0.5", User: "deploy", Transport: "ssh", Mode: "push"},
}}}

// clickOn renders a frame, then clicks the middle of region id.
func clickOn(t *testing.T, m Model, id string) Model {
	t.Helper()
	_ = m.View()
	time.Sleep(30 * time.Millisecond) // zones are recorded asynchronously
	z := m.env.Zones.m.Get(id)
	if z == nil {
		t.Fatalf("no region %q on screen", id)
	}
	x, y := (z.StartX+z.EndX)/2, z.StartY
	return feed(m, tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
}

func TestServersHomeClickSelectsThenOpens(t *testing.T) {
	m := New(serverDeps(t))
	m = feed(m, tea.WindowSizeMsg{Width: 100, Height: 30}, testFleet)
	m = clickOn(t, m, "srv:row:1")
	if d := m.screens["dashboard"].(*dashboard); d.srv.sel != 1 || m.active != "dashboard" {
		t.Fatalf("first click selects (sel %d, active %s)", d.srv.sel, m.active)
	}
	// Clicking the selected row opens it (a nav command).
	z := m.env.Zones.m.Get("srv:row:1")
	m = feedRun(m, tea.MouseMsg{X: z.StartX + 3, Y: z.StartY, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.active != "server:prod-1" {
		t.Fatalf("second click should open prod-1, active %s", m.active)
	}
}

func TestServerDetailHasNoShellForCloud(t *testing.T) {
	m := New(serverDeps(t))
	m = feed(m, tea.WindowSizeMsg{Width: 100, Height: 30}, testFleet, runLineMsg{"server cloud-1"})
	if m.active != "server:cloud-1" {
		t.Fatalf("active %s", m.active)
	}
	v := plain(m.View())
	if contains(v, "Open shell") || !contains(v, "Fleet Cloud") || !contains(v, "no fleet logs command") {
		t.Fatalf("\n%s", v)
	}
	m = feed(m, runLineMsg{"ssh cloud-1"})
	if !contains(m.env.State.Toast, "no SSH shell") {
		t.Fatalf("ssh to a cloud server should be refused: %q", m.env.State.Toast)
	}
	m = feed(m, runLineMsg{"server prod-1"})
	if v := plain(m.View()); !contains(v, "Open shell") || !contains(v, "SSH · deploy@10.0.0.5") {
		t.Fatalf("\n%s", v)
	}
}

func TestClickOutsidePaletteClosesItAndFooterHintsClick(t *testing.T) {
	m := New(serverDeps(t))
	m = feed(m, tea.WindowSizeMsg{Width: 100, Height: 30}, testFleet)
	m = clickOn(t, m, "key:ctrl+k")
	if _, ok := m.overlay.(*palette); !ok {
		t.Fatal("clicking ctrl+k in the footer opens the palette")
	}
	_ = m.View()
	m = feed(m, tea.MouseMsg{X: 0, Y: 29, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.overlay != nil {
		t.Fatal("a click outside the palette closes it")
	}
	m = clickOn(t, m, "key:a")
	if d := m.screens["dashboard"].(*dashboard); d.srv.adding == nil {
		t.Fatal("clicking the a hint starts adding a server")
	}
}

func TestPaletteClickRunsRowAndWheelMoves(t *testing.T) {
	m := New(serverDeps(t))
	m = feed(m, tea.WindowSizeMsg{Width: 100, Height: 30}, testFleet, tea.KeyMsg{Type: tea.KeyCtrlK})
	m = feed(m, keys("go prod")...)
	p := m.overlay.(*palette)
	before := p.sel
	m = feed(m, tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if len(p.shown) > 1 && p.sel == before {
		t.Fatal("wheel should move the selection")
	}
	idx := -1
	for i, it := range p.shown {
		if it.line == "server prod-1" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("Go to prod-1 not found: %+v", p.shown)
	}
	_ = m.View()
	time.Sleep(30 * time.Millisecond)
	z := m.env.Zones.m.Get("ov:pal:" + itoa(idx))
	if z == nil {
		t.Fatal("palette row not clickable")
	}
	m = feedRun(m, tea.MouseMsg{X: z.StartX + 2, Y: z.StartY, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.active != "server:prod-1" {
		t.Fatalf("clicking Go to prod-1 should open it, active %s", m.active)
	}
}

func TestNoMouseIgnoresClicks(t *testing.T) {
	d := serverDeps(t)
	d.NoMouse = true
	m := New(d)
	m = feed(m, tea.WindowSizeMsg{Width: 100, Height: 30}, testFleet)
	m = clickOn(t, m, "key:ctrl+k")
	if m.overlay != nil {
		t.Fatal("mouse is off")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func itoa(i int) string { return fmt.Sprint(i) }
