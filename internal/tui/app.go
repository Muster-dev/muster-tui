// Package tui is muster-tui's Bubble Tea application: a header line, the
// active screen, a footer of clickable hints, and a command palette
// (ctrl+k) that runs every action and understands typed commands.
package tui

import (
	"context"
	"os/exec"
	"strings"
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
	hist    *prompt.History
	overlay overlay
	ovRect  *rect // where the overlay box was drawn (for clicks outside it)
	click   *lastClick
	w, h    int

	quitArmed   bool
	ticking     bool
	repaired    *bool // re-pair attempted after an auth failure
	doctorAsked *bool // background doctor run started
}

// lastClick remembers the previous click, to spot double clicks.
type lastClick struct {
	id string
	at time.Time
}

type (
	spinTickMsg    struct{}
	fleetTickMsg   struct{}
	refreshTickMsg struct{}
	quitResetMsg   struct{}
	toastClearMsg  struct{ until time.Time }
)

const (
	refreshEvery      = 20 * time.Second
	fleetRefreshEvery = 30 * time.Second
)

// New builds the app.
func New(d Deps) Model {
	if d.Now == nil {
		d.Now = time.Now
	}
	env := &Env{Deps: d, Tasks: newTasks(d.Now), State: newState(d.Project, d.Cwd), Zones: newClickZones()}
	if d.CLIVersion != "" {
		env.State.Caps = engine.CapsFor(d.CLIVersion)
	}
	if d.Branch != "" {
		env.State.Branch = d.Branch
	}
	m := Model{
		repaired:    new(bool),
		doctorAsked: new(bool),
		env:         env,
		screens:     map[string]Screen{},
		hist:        prompt.LoadHistory(d.HistoryPath),
		ovRect:      &rect{},
		click:       &lastClick{},
	}
	var first Screen = newDashboard(env)
	m.screens[first.Key()] = first
	m.active = first.Key()
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
	cmds = append(cmds, m.env.loadDocker(), m.env.loadFleet(), m.env.loadFleetHelp(), tickRefresh(), tickFleet())
	if !m.env.State.Caps.Known {
		cmds = append(cmds, m.env.loadVersion())
	}
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

func tickFleet() tea.Cmd {
	return tea.Tick(fleetRefreshEvery, func(time.Time) tea.Msg { return fleetTickMsg{} })
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
	case fleetTickMsg:
		// Servers refresh on their own while they're on screen.
		var cmd tea.Cmd
		if m.showsServers() {
			cmd = m.env.loadFleetHealth()
		}
		return tea.Batch(cmd, tickFleet())
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
		return m.openPalette(msg.text)
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
		m.overlay = newConfirm(m.env, msg)
		return nil
	case fullscreenMsg:
		m.env.Fullscreen = msg.on
		return nil
	case replayKeyMsg:
		return m.toScreen(keyFromString(msg.key))
	}
	m.env.State.apply(msg, now)
	return m.broadcast(msg)
}

// versionNotice tells users of an older CLI what they're missing (once,
// at startup).
func (m *Model) versionNotice() tea.Cmd {
	c := m.env.State.Caps
	if !c.Known || c.EngineAPI {
		return nil
	}
	return m.setToast(lvlWarn, "muster "+c.Version.String()+" predates "+engine.EngineAPIVersion+": explain, ask and quick approvals are off — run muster update")
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
			if m.env.V07() {
				// The CLI is the authority on approvals (~/.muster/trust).
				if !m.busy("checking hooks") {
					cmds = append(cmds, m.env.loadHooks())
				}
			} else {
				m.env.State.HookChanges = m.env.State.Project.HookChanges()
			}
		}
		if vm, ok := tm.Msg.(versionMsg); ok && vm.err == nil {
			cmds = append(cmds, m.versionNotice())
			if m.env.V07() {
				cmds = append(cmds, m.env.loadHooks())
			}
		}
		// Servers get checked as soon as we know there are some.
		if fm, ok := tm.Msg.(fleetMsg); ok && fm.err == nil && len(m.env.State.Fleet.Machines) > 0 && m.env.State.FleetHealthAt.IsZero() && !m.env.NoAutoLoad {
			cmds = append(cmds, m.env.loadFleetHealth())
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
	case navMsg, runLineMsg, toastMsg, refreshMsg, reloadConfigMsg, execMsg, confirmMsg, backMsg, prefillMsg:
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
		if _, ok := m.screens[key]; ok && key != m.active {
			m.leave(m.active)
			m.active = key
			return nil
		}
	}
	if m.active != "dashboard" {
		m.leave(m.active)
		m.active = "dashboard"
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
	case "fleet-health":
		return e.loadFleetHealth()
	case "doctor":
		return e.loadDoctor(false)
	case "hooks":
		return e.loadHooks()
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

// showsServers reports whether server status is on screen.
func (m *Model) showsServers() bool {
	if len(m.env.State.Fleet.Machines) == 0 {
		return false
	}
	a := m.active
	return a == "dashboard" || a == "servers" || strings.HasPrefix(a, "server:")
}

// keyFromString turns a help label like "r", "enter" or "ctrl+s" into a key
// message.
func keyFromString(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "space", " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up", "↑":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down", "↓":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left", "←":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right", "→":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	}
	if c, ok := strings.CutPrefix(strings.ToLower(s), "ctrl+"); ok && len(c) == 1 && c[0] >= 'a' && c[0] <= 'z' {
		return tea.KeyMsg{Type: tea.KeyCtrlA + tea.KeyType(c[0]-'a')}
	}
	r := []rune(s)
	if len(r) > 0 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: r[:1]}
	}
	return tea.KeyMsg{}
}
