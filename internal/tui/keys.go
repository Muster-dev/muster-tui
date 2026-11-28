package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *Model) handleKey(k tea.KeyMsg) tea.Cmd {
	key := k.String()

	// ctrl+c: close the popup → cancel the task → arm quit → quit.
	if key == "ctrl+c" {
		if m.overlay != nil {
			m.overlay = nil
			return nil
		}
		if m.quitArmed {
			m.env.Tasks.CancelAll()
			return tea.Quit
		}
		if t := m.env.Tasks.Foreground(); t != nil {
			m.env.Tasks.Cancel(t.ID)
			return m.setToast(lvlWarn, "Cancelled: "+t.Label)
		}
		m.quitArmed = true
		return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return quitResetMsg{} })
	}
	m.quitArmed = false

	if m.overlay != nil {
		ov, cmd := m.overlay.HandleKey(k)
		m.overlay = ov
		return cmd
	}

	if key == "ctrl+k" {
		return m.openPalette("")
	}

	// Screens in text-entry mode get everything else.
	if c, ok := m.activeScreen().(Capturer); ok && c.Capturing() {
		return m.toScreen(k)
	}

	switch key {
	case "ctrl+o":
		m.env.Fullscreen = !m.env.Fullscreen
		return nil
	case "ctrl+l":
		m.env.State.Toast = ""
		return tea.ClearScreen
	}

	s, cmd, handled := m.activeScreen().HandleKey(k)
	m.screens[m.active] = s
	if handled {
		return cmd
	}
	switch key {
	case "/", ":":
		return m.openPalette("")
	case "?":
		m.overlay = newHelp(m.env, m.activeScreen())
		return nil
	case "esc":
		return m.escFallback()
	case "q":
		if m.active == "dashboard" {
			return m.execute("quit", false)
		}
		return m.back()
	}
	return cmd
}

// openPalette shows the command palette, optionally with text typed in.
func (m *Model) openPalette(text string) tea.Cmd {
	m.overlay = newPalette(m.env, m.activeScreen(), m.hist, text)
	return nil
}

// escFallback: foreground task → full screen → previous screen.
func (m *Model) escFallback() tea.Cmd {
	// Esc only cancels what you're looking at; a deploy running in the
	// background keeps going (ctrl+c or `abort` stops it).
	if t := m.env.Tasks.ForegroundOf(m.active, ""); t != nil {
		m.env.Tasks.Cancel(t.ID)
		return m.setToast(lvlWarn, "Cancelled: "+t.Label)
	}
	if m.env.Fullscreen {
		m.env.Fullscreen = false
		return nil
	}
	if m.active != "dashboard" {
		return m.back()
	}
	return nil
}

func (m *Model) toScreen(k tea.KeyMsg) tea.Cmd {
	s, cmd, _ := m.activeScreen().HandleKey(k)
	m.screens[m.active] = s
	return cmd
}

// doubleClick is how close two clicks on the same thing must be.
const doubleClick = 400 * time.Millisecond

func (m *Model) handleMouse(ms tea.MouseMsg) tea.Cmd {
	if m.env.NoMouse || ms.Action != tea.MouseActionPress {
		return nil
	}
	switch ms.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		delta := 1
		key := tea.KeyMsg{Type: tea.KeyDown}
		if ms.Button == tea.MouseButtonWheelUp {
			delta, key = -1, tea.KeyMsg{Type: tea.KeyUp}
		}
		if m.overlay != nil {
			if w, ok := m.overlay.(overlayWheel); ok {
				m.overlay = w.Wheel(delta)
			}
			return nil
		}
		if c, ok := m.activeScreen().(Capturer); ok && c.Capturing() {
			return nil
		}
		return m.toScreen(key)
	case tea.MouseButtonLeft:
	default:
		return nil
	}

	id, _ := m.env.Zones.hit(ms.X, ms.Y)
	now := m.env.Now()
	double := id != "" && id == m.click.id && now.Sub(m.click.at) < doubleClick
	m.click.id, m.click.at = id, now
	if double {
		m.click.id = "" // a third click starts over
	}

	if m.overlay != nil {
		if !m.ovRect.contains(ms.X, ms.Y) {
			m.overlay = nil
			return nil
		}
		if c, ok := m.overlay.(overlayClicker); ok && id != "" {
			ov, cmd := c.Click(id)
			m.overlay = ov
			return cmd
		}
		return nil
	}
	if id == "" {
		return nil
	}
	if k, ok := zoneArg(id, "key:"); ok {
		switch k {
		case "ctrl+k":
			return m.openPalette("")
		case "?":
			m.overlay = newHelp(m.env, m.activeScreen())
			return nil
		}
		return m.handleKey(keyFromString(k))
	}
	if id == "hdr:home" {
		return m.open(m.screens["dashboard"])
	}
	if c, ok := m.activeScreen().(Clicker); ok {
		s, cmd, _ := c.Click(id, double)
		m.screens[m.active] = s
		return cmd
	}
	return nil
}
