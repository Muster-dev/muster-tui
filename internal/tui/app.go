// Package tui is muster-tui's Bubble Tea application: a header, a left rail
// (services, servers, docker), a main pane showing the active screen, a
// suggestion bar and a prompt that understands commands.
package tui

import (
	"context"
	"os/exec"
	"time"

	"github.com/Muster-dev/muster-tui/internal/auth"
	"github.com/Muster-dev/muster-tui/internal/config"
	"github.com/Muster-dev/muster-tui/internal/engine"
	"github.com/Muster-dev/muster-tui/internal/prompt"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// Model is the root Bubble Tea model.
type Model struct {
	env     *Env
	screens map[string]Screen
	active  string
	stack   []string
	focus   focusZone
	railSel int
	prompt  promptBox
	overlay overlay
	w, h    int

	quitArmed   bool
	ticking     bool
	repaired    *bool // re-pair attempted after an auth failure
	doctorAsked *bool // background doctor run started
}

type (
	spinTickMsg    struct{}
	refreshTickMsg struct{}
	quitResetMsg   struct{}
	toastClearMsg  struct{ until time.Time }
)

const refreshEvery = 20 * time.Second

// New builds the app.
func New(d Deps) Model {
	if d.Now == nil {
		d.Now = time.Now
	}
	env := &Env{Deps: d, Tasks: newTasks(d.Now), State: newState(d.Project, d.Cwd)}
	if d.Branch != "" {
		env.State.Branch = d.Branch
	}
	m := Model{
		repaired:    new(bool),
		doctorAsked: new(bool),
		env:         env,
		screens:     map[string]Screen{},
		prompt:      newPromptBox(prompt.LoadHistory(d.HistoryPath)),
	}
	var first Screen = newDashboard(env)
	m.screens[first.Key()] = first
	m.active = first.Key()
	if d.Project == nil {
		ob := newOnboarding(env, d.Cwd)
		m.screens[ob.Key()] = ob
		m.active = ob.Key()
		m.stack = []string{"dashboard"}
		m.focus = focusMain
	}
	return m
}

// Init starts the first loads. Nothing here blocks the first frame.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.screens[m.active].Init()}
	if m.env.NoAutoLoad {
		return tea.Batch(cmds...)
	}
	if m.env.TokenMissing && m.env.CanBootstrap && m.env.Engine != nil {
		cmds = append(cmds, m.env.pair())
	} else {
		// Doctor is slow; it starts once history has loaded (see dispatch) so
		// the dashboard fills in first.
		cmds = append(cmds, m.env.loadStatus(), m.env.loadHistory())
	}
	cmds = append(cmds, m.env.loadDocker(), m.env.loadFleet(), tickRefresh())
	return tea.Batch(cmds...)
}

// pair creates a token with `muster auth create` — allowed without auth only
// while the machine has no tokens (first run, or a wiped token store).
func (e *Env) pair() tea.Cmd {
	eng := e.Engine
	_, cmd := e.Tasks.Run("pairing with the muster CLI", "", false, func(ctx context.Context) tea.Msg {
		repaired, _ := auth.RepairEmptyStore()
		tok, err := eng.CreateToken(ctx, "muster-tui")
		if err == nil {
			err = auth.SaveToken(tok)
		}
		return tokenMsg{token: tok, err: err, repaired: repaired}
	})
	return cmd
}

func tickRefresh() tea.Cmd {
	return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return refreshTickMsg{} })
}

func tickSpin() tea.Cmd {
	return tea.Tick(110*time.Millisecond, func(time.Time) tea.Msg { return spinTickMsg{} })
}

// Shutdown cancels every running task (child processes are killed).
func (m Model) Shutdown() { m.env.Tasks.CancelAll() }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.env.Active = m.active
	cmd := m.update(msg)
	m.env.Active = m.active
	if !m.ticking && m.env.Tasks.Len() > 0 && !theme.Static {
		m.ticking = true
		cmd = tea.Batch(cmd, tickSpin())
	}
	return m, cmd
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	now := m.env.Now()
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
		return nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case spinTickMsg:
		m.env.Frame++
		if m.env.Tasks.Len() > 0 {
			return tickSpin()
		}
		m.ticking = false
		return nil
	case refreshTickMsg:
		var cmds []tea.Cmd
		if !m.busy("checking health") {
			cmds = append(cmds, m.env.loadStatus())
		}
		if !m.busy("checking docker") {
			cmds = append(cmds, m.env.loadDocker())
		}
		return tea.Batch(append(cmds, tickRefresh())...)
	case quitResetMsg:
		m.quitArmed = false
		return nil
	case toastClearMsg:
		if m.env.State.ToastUntil.Equal(msg.until) {
			m.env.State.Toast = ""
		}
		return nil

	case taskDoneMsg:
		ended, ok := m.env.Tasks.finish(msg.ID)
		if !ok {
			return nil
		}
		cmd := m.dispatch(TaskMsg{ID: msg.ID, Msg: msg.Msg}, now)
		return tea.Batch(cmd, m.broadcast(ended))
	case streamOpenMsg:
		if msg.err != nil {
			ended, _ := m.env.Tasks.finish(msg.ID)
			return tea.Batch(m.dispatch(TaskMsg{ID: msg.ID, Msg: taskErrMsg{msg.err}}, now), m.broadcast(ended))
		}
		return listen(msg.ID, msg.ch)
	case streamItemMsg:
		return tea.Batch(m.dispatch(TaskMsg{ID: msg.ID, Msg: msg.Msg}, now), listen(msg.ID, msg.ch))
	case streamEndMsg:
		ended, ok := m.env.Tasks.finish(msg.ID)
		if !ok {
			return nil
		}
		return m.broadcast(ended)

	case tokenMsg:
		if msg.err != nil {
			return m.setToast(lvlWarn, "Couldn't pair with the muster CLI: "+msg.err.Error())
		}
		m.env.Engine.Token = msg.token
		m.env.TokenMissing = false
		note := m.setToast(lvlOK, "Paired with the muster CLI")
		if msg.repaired {
			note = m.setToast(lvlWarn, "muster's token store was empty (~/.muster/tokens/auth.json) — paired again; recreate any other API tokens")
		}
		return tea.Batch(m.env.loadStatus(), m.env.loadHistory(), m.env.loadFleet(), note)

	case navMsg:
		return m.open(msg.screen)
	case backMsg:
		return m.back()
	case runLineMsg:
		return m.execute(msg.line, false)
	case prefillMsg:
		m.prompt.SetValue(msg.text)
		m.prompt.refresh(m.env.State.PromptContext())
		m.focus = focusPrompt
		return nil
	case toastMsg:
		return m.setToast(msg.level, msg.text)
	case refreshMsg:
		return m.refresh(msg.what)
	case reloadConfigMsg:
		return m.reloadConfig()
	case execMsg:
		after := msg.after
		return tea.ExecProcess(msg.cmd, func(err error) tea.Msg {
			if after != nil {
				return after(err)
			}
			return nil
		})
	case confirmMsg:
		m.overlay = newConfirm(msg)
		return nil
	case fullscreenMsg:
		m.env.Fullscreen = msg.on
		m.layout()
		return nil
	case focusMsg:
		m.focus = msg.zone
		return nil
	case replayKeyMsg:
		return m.toScreen(keyFromString(msg.key))
	}
	m.env.State.apply(msg, now)
	return m.broadcast(msg)
}

func (m *Model) busy(label string) bool {
	for _, t := range m.env.Tasks.List() {
		if t.Label == label {
			return true
		}
	}
	return false
}

// dispatch folds task results into shared state and hands them to screens.
func (m *Model) dispatch(tm TaskMsg, now time.Time) tea.Cmd {
	var cmds []tea.Cmd
	if m.env.State.apply(tm.Msg, now) {
		if sm, ok := tm.Msg.(statusMsg); ok && sm.err == nil && m.env.State.Project != nil {
			m.env.State.HookChanges = m.env.State.Project.HookChanges()
		}
		if _, ok := tm.Msg.(historyMsg); ok && m.env.State.Doctor == nil && !*m.doctorAsked && !m.env.NoAutoLoad {
			*m.doctorAsked = true
			cmds = append(cmds, m.env.loadDoctor(false))
		}
		// Self-heal: the CLI rejected our token and has no tokens at all
		// (first run, or its token store was wiped) — pair again once.
		if sm, ok := tm.Msg.(statusMsg); ok && engine.IsAuth(sm.err) && !*m.repaired && m.env.Engine != nil && !auth.MachineHasTokens() && !m.env.NoAutoLoad {
			*m.repaired = true
			cmds = append(cmds, m.env.pair())
		}
	}
	switch inner := tm.Msg.(type) {
	case navMsg, runLineMsg, toastMsg, refreshMsg, reloadConfigMsg, execMsg, confirmMsg, backMsg, focusMsg, prefillMsg:
		cmds = append(cmds, func() tea.Msg { return inner })
	}
	cmds = append(cmds, m.broadcast(tm))
	return tea.Batch(cmds...)
}

func (m *Model) broadcast(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	for k, s := range m.screens {
		ns, cmd := s.Update(msg)
		m.screens[k] = ns
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

func (m *Model) activeScreen() Screen { return m.screens[m.active] }

func (m *Model) open(s Screen) tea.Cmd {
	key := s.Key()
	if old, ok := m.screens[key]; ok && old != s {
		if c, ok := old.(Closer); ok {
			c.Close()
		}
	}
	if m.active != key {
		m.leave(m.active)
		m.stack = append(m.stack, m.active)
		if len(m.stack) > 20 {
			m.stack = m.stack[len(m.stack)-20:]
		}
	}
	m.screens[key] = s
	m.active = key
	m.env.Active = key
	m.overlay = nil
	m.focus = focusMain
	if key == "dashboard" {
		m.focus = focusPrompt
	}
	// A running deploy leaves the prompt free; its confirm step and
	// recovery menu take keys directly.
	if d, ok := s.(*deployScreen); ok && d.phase != phConfirm {
		m.focus = focusPrompt
	}
	return s.Init()
}

// leave evicts a screen unless it wants to stay alive.
func (m *Model) leave(key string) {
	s, ok := m.screens[key]
	if !ok || key == "dashboard" {
		return
	}
	if p, ok := s.(Persistent); ok && p.Persistent() {
		return
	}
	if c, ok := s.(Closer); ok {
		c.Close()
	}
	m.env.Tasks.CancelOwner(key)
	delete(m.screens, key)
}

func (m *Model) back() tea.Cmd {
	for len(m.stack) > 0 {
		key := m.stack[len(m.stack)-1]
		m.stack = m.stack[:len(m.stack)-1]
		if s, ok := m.screens[key]; ok && key != m.active {
			m.leave(m.active)
			m.active = key
			if key == "dashboard" || key == "deploy" {
				m.focus = focusPrompt
			}
			_ = s
			return nil
		}
	}
	if m.active != "dashboard" {
		m.leave(m.active)
		m.active = "dashboard"
		m.focus = focusPrompt
	}
	return nil
}

func (m *Model) setToast(level int, text string) tea.Cmd {
	st := m.env.State
	st.Toast, st.ToastLevel = text, level
	st.ToastUntil = m.env.Now().Add(6 * time.Second)
	until := st.ToastUntil
	return tea.Tick(6*time.Second, func(time.Time) tea.Msg { return toastClearMsg{until} })
}

func (m *Model) refresh(what string) tea.Cmd {
	e := m.env
	switch what {
	case "status":
		return e.loadStatus()
	case "history":
		return e.loadHistory()
	case "docker":
		return e.loadDocker()
	case "fleet":
		return e.loadFleet()
	case "doctor":
		return e.loadDoctor(false)
	}
	return tea.Batch(e.loadStatus(), e.loadHistory(), e.loadDocker(), e.loadFleet())
}

func (m *Model) reloadConfig() tea.Cmd {
	st := m.env.State
	var p *config.Project
	if st.Project != nil {
		np, err := st.Project.Reload()
		if err != nil {
			return m.setToast(lvlErr, err.Error())
		}
		p = np
	} else if path := config.FindIn(m.env.Cwd); path != "" {
		np, err := config.Load(path)
		if err != nil {
			return m.setToast(lvlErr, err.Error())
		}
		p = np
	}
	st.setProject(p, m.env.Cwd)
	if m.env.Deps.Branch != "" {
		st.Branch = m.env.Deps.Branch
	}
	m.env.Project = p
	return tea.Batch(m.broadcast(reloadConfigMsg{}), m.env.loadStatus(), m.env.loadHistory())
}

func (m *Model) execProcess(cmd *exec.Cmd, after func(error) tea.Msg) tea.Cmd {
	return func() tea.Msg { return execMsg{cmd: cmd, after: after} }
}

// keyFromString turns a help label like "r" or "enter" into a key message.
func keyFromString(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "space", " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	r := []rune(s)
	if len(r) > 0 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: r[:1]}
	}
	return tea.KeyMsg{}
}
