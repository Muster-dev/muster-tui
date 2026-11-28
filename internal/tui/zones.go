package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"
)

// clickZones tracks the clickable regions of the last frame. Screens wrap
// clickable text with Env.mark(id, text) while rendering; the root View
// scans the finished frame (which strips the markers) and the mouse handler
// asks which region a click landed in.
type clickZones struct {
	m   *zone.Manager
	ids map[string]bool
}

func newClickZones() *clickZones {
	return &clickZones{m: zone.New(), ids: map[string]bool{}}
}

// begin starts a new frame.
func (z *clickZones) begin() {
	if z == nil {
		return
	}
	z.ids = map[string]bool{}
}

func (z *clickZones) mark(id, s string) string {
	if z == nil || id == "" || s == "" {
		return s
	}
	z.ids[id] = true
	return z.m.Mark(id, s)
}

// scan records the regions in frame and returns it without markers.
func (z *clickZones) scan(frame string) string {
	if z == nil {
		return frame
	}
	return z.m.Scan(frame)
}

// hit returns the smallest region containing (x, y).
func (z *clickZones) hit(x, y int) (string, bool) {
	if z == nil {
		return "", false
	}
	ev := tea.MouseMsg{X: x, Y: y}
	best, bestArea := "", -1
	for id := range z.ids {
		info := z.m.Get(id)
		if info == nil || !info.InBounds(ev) {
			continue
		}
		area := (info.EndX - info.StartX + 1) * (info.EndY - info.StartY + 1)
		if bestArea < 0 || area < bestArea || (area == bestArea && id < best) {
			best, bestArea = id, area
		}
	}
	return best, bestArea >= 0
}

// mark wraps s as a clickable region called id.
func (e *Env) mark(id, s string) string { return e.Zones.mark(id, s) }

// Clicker screens react to clicks on regions they marked. double is true
// for a second click on the same region within a short time.
type Clicker interface {
	Click(id string, double bool) (Screen, tea.Cmd, bool)
}

// zoneArg returns the part of id after prefix ("row:3" → "3").
func zoneArg(id, prefix string) (string, bool) { return strings.CutPrefix(id, prefix) }
