package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *Model) handleKey(k tea.KeyMsg) tea.Cmd {
	key := k.String()
	ctx := m.env.State.PromptContext()

	// Ctrl+C: clear input → cancel task → arm quit → quit.
	if key == "ctrl+c" {
		if m.prompt.searching {
			m.prompt.searching = false
			return nil
		}
		if m.prompt.Value() != "" {
			m.prompt.Clear()
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

	// Screens in text-entry mode get everything.
	if c, ok := m.activeScreen().(Capturer); ok && c.Capturing() && m.focus != focusPrompt {
		return m.toScreen(k)
	}

	if m.prompt.searching {
		m.prompt.searchKey(k)
		return nil
	}

	// Global keys.
	switch key {
	case "ctrl+k":
		m.overlay = newPalette(m.env, m.activeScreen())
		return nil
	case "ctrl+o":
		m.env.Fullscreen = !m.env.Fullscreen
		m.layout()
		return nil
	case "ctrl+r":
		if m.focus == focusPrompt {
			m.prompt.startSearch()
			return nil
		}
	case "ctrl+l":
		m.env.State.Toast = ""
		return tea.ClearScreen
	}

	switch m.focus {
	case focusRail:
		return m.railKey(k)
	case focusMain:
		return m.mainKey(k)
	}
	return m.promptKey(k, ctx)
}

func (m *Model) promptKey(k tea.KeyMsg, ctx promptCtx) tea.Cmd {
	empty := m.prompt.Value() == ""
	switch k.String() {
	case "esc":
		if m.prompt.popup {
			m.prompt.popup = false
			return nil
		}
		if !empty {
			m.prompt.Clear()
			return nil
		}
		return m.escFallback(k)
	case "tab":
		if m.prompt.errInput != "" && m.prompt.suggestion != "" && m.prompt.Value() == m.prompt.errInput {
			m.prompt.SetValue(m.prompt.suggestion)
			m.prompt.errInput, m.prompt.suggestion = "", ""
			m.prompt.refresh(ctx)
			return nil
		}
		if m.prompt.accept(ctx) {
			return nil
		}
		if empty {
			m.focus = focusRail
		}
		return nil
	case "shift+tab":
		m.focus = focusMain
		return nil
	case "up", "ctrl+p":
		if m.prompt.popup {
			m.prompt.move(-1)
			return nil
		}
		if v, ok := m.prompt.hist.Prev(m.prompt.Value()); ok {
			m.prompt.SetValue(v)
		}
		return nil
	case "down", "ctrl+n":
		if m.prompt.popup {
			m.prompt.move(1)
			return nil
		}
		if v, ok := m.prompt.hist.Next(); ok {
			m.prompt.SetValue(v)
		}
		return nil
	case "pgup", "pgdown":
		return m.toScreen(k)
	case "enter":
		if m.prompt.popup && m.prompt.sel >= 0 {
			m.prompt.accept(ctx)
			if needsArg(m.prompt.Value()) {
				return nil
			}
		}
		line := m.prompt.Value()
		if line == "" {
			return m.toScreen(k)
		}
		return m.execute(line, true)
	case "?":
		if empty {
			m.overlay = newHelp(m.env, m.activeScreen())
			return nil
		}
	}
	var cmd tea.Cmd
	m.prompt.input, cmd = m.prompt.input.Update(k)
	m.prompt.errInput, m.prompt.suggestion = "", ""
	m.prompt.refresh(ctx)
	return cmd
}

// escFallback: screen → foreground task → previous screen.
func (m *Model) escFallback(k tea.KeyMsg) tea.Cmd {
	if s, cmd, handled := m.activeScreen().HandleKey(k); handled {
		m.screens[m.active] = s
		return cmd
	}
	// Esc only cancels what you're looking at; a deploy running in the
	// background keeps going (Ctrl+C or `abort` stops it).
	if t := m.env.Tasks.ForegroundOf(m.active, ""); t != nil {
		m.env.Tasks.Cancel(t.ID)
		return m.setToast(lvlWarn, "Cancelled: "+t.Label)
	}
	if m.env.Fullscreen {
		m.env.Fullscreen = false
		m.layout()
		return nil
	}
	if m.focus != focusPrompt {
		m.focus = focusPrompt
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

func (m *Model) mainKey(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "tab":
		m.focus = focusPrompt
		return nil
	case "shift+tab":
		m.focus = focusRail
		return nil
	}
	s, cmd, handled := m.activeScreen().HandleKey(k)
	m.screens[m.active] = s
	if handled {
		return cmd
	}
	switch k.String() {
	case "esc":
		return m.escFallback(k)
	case "?":
		m.overlay = newHelp(m.env, m.activeScreen())
		return nil
	case "q":
		return m.back()
	}
	// Printable keys the screen didn't want go to the prompt.
	if k.Type == tea.KeyRunes || k.Type == tea.KeySpace {
		m.focus = focusPrompt
		return m.promptKey(k, m.env.State.PromptContext())
	}
	return cmd
}

func (m *Model) railKey(k tea.KeyMsg) tea.Cmd {
	items := m.railItems()
	switch k.String() {
	case "up", "k":
		m.railSel = clamp(m.railSel-1, 0, len(items)-1)
		return nil
	case "down", "j":
		m.railSel = clamp(m.railSel+1, 0, len(items)-1)
		return nil
	case "tab":
		m.focus = focusMain
		return nil
	case "shift+tab", "esc":
		m.focus = focusPrompt
		return nil
	case "enter", "right", "l":
		if m.railSel < len(items) {
			return m.openRailItem(items[m.railSel])
		}
		return nil
	case "?":
		m.overlay = newHelp(m.env, m.activeScreen())
		return nil
	}
	if k.Type == tea.KeyRunes || k.Type == tea.KeySpace {
		m.focus = focusPrompt
		return m.promptKey(k, m.env.State.PromptContext())
	}
	return nil
}

func (m *Model) openRailItem(it railItem) tea.Cmd {
	switch it.kind {
	case "service":
		return m.open(newServiceScreen(m.env, it.id))
	case "machine":
		s := newServers(m.env)
		s.selectMachine(it.id)
		return m.open(s)
	case "docker":
		return m.open(newDockerScreen(m.env, 0))
	case "setup":
		return m.open(newOnboarding(m.env, m.env.Cwd))
	}
	return nil
}

func (m *Model) handleMouse(ms tea.MouseMsg) tea.Cmd {
	if ms.Action != tea.MouseActionPress {
		return nil
	}
	switch ms.Button {
	case tea.MouseButtonWheelUp:
		return m.toScreen(tea.KeyMsg{Type: tea.KeyUp})
	case tea.MouseButtonWheelDown:
		return m.toScreen(tea.KeyMsg{Type: tea.KeyDown})
	case tea.MouseButtonLeft:
		railW := m.railWidth()
		bodyTop := 2
		row := ms.Y - bodyTop
		if railW > 0 && ms.X < railW && row >= 0 {
			if idx, ok := m.railRowToItem(row); ok {
				m.railSel = idx
				m.focus = focusRail
				return m.openRailItem(m.railItems()[idx])
			}
		}
		if ms.Y == m.h-1 {
			m.focus = focusPrompt
		} else if ms.X >= railW && row >= 0 && row < m.env.MainH {
			m.focus = focusMain
		}
	}
	return nil
}
