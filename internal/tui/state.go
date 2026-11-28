package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Muster-dev/muster-tui/internal/config"
	"github.com/Muster-dev/muster-tui/internal/docker"
	"github.com/Muster-dev/muster-tui/internal/engine"
	"github.com/Muster-dev/muster-tui/internal/fleet"
	"github.com/Muster-dev/muster-tui/internal/prompt"
	tea "github.com/charmbracelet/bubbletea"
)

// Deps is everything the TUI needs from the outside world.
type Deps struct {
	Engine    *engine.Engine // nil when the CLI is missing
	EngineErr error          // why Engine is nil
	Docker    *docker.Client // nil when docker is missing
	Project   *config.Project
	Cwd       string
	// TokenMissing is set when no MUSTER_TOKEN was found; the app tries to
	// bootstrap one when the machine has no tokens yet.
	TokenMissing bool
	CanBootstrap bool
	HistoryPath  string
	Now          func() time.Time
	Version      string
	// Branch overrides git branch detection (tests).
	Branch string
	// NoAutoLoad disables startup loads and timers (tests feed data).
	NoAutoLoad bool
	// CLIVersion presets `muster --version` (tests); otherwise it's asked
	// for at startup.
	CLIVersion string
	// NoMouse: mouse reporting is off (--no-mouse, MUSTER_TUI_MOUSE=off or
	// tui_mouse: off), so the terminal's own text selection works.
	NoMouse bool
}

// Env is shared by the app and every screen. Screens may mutate State from
// Update (never from View); everything runs on the Bubble Tea goroutine.
type Env struct {
	Deps
	Tasks *Tasks
	State *State
	Frame int // spinner frame
	// Current main pane size (updated on resize / layout change).
	MainW, MainH int
	Fullscreen   bool
	Active       string // key of the screen in the main pane
	// Zones are the clickable regions of the current frame.
	Zones *clickZones
}

// Spin returns the current spinner frame.
func (e *Env) Spin() string { return spinnerFrame(e.Frame) }

// HasProject reports whether a muster project is loaded.
func (e *Env) HasProject() bool { return e.State.Project != nil }

// Eng returns the engine bound to the current project directory.
func (e *Env) Eng() *engine.Engine {
	if e.Engine == nil {
		return nil
	}
	if e.State.Project != nil {
		return e.Engine.WithDir(e.State.Project.Dir)
	}
	return e.Engine.WithDir(e.Cwd)
}

// SvcState is live state for one service.
type SvcState struct {
	Health     string // healthy, unhealthy, disabled, no_health_check, unknown
	Detail     string
	Since      time.Time // when Health last changed
	CheckedAt  time.Time
	Deploying  bool
	LastDeploy *engine.HistoryEvent
	// NeverDeployed is true once history has loaded and has no deploy or
	// rollback for this service.
	NeverDeployed bool

	// From status --json on muster ≥ 0.7 (zero otherwise).
	Stack    string
	Target   string
	Remote   string
	LockedBy *engine.Lock // a deploy of this service is running somewhere
	Replicas *engine.Replicas
	CLILast  *engine.LastDeploy
}

// Shown is the health to display. A failing check on a service that has
// never been deployed isn't "unhealthy" — there's nothing running yet.
func (ss *SvcState) Shown() string {
	if ss.Health == "unhealthy" && ss.NeverDeployed {
		return "not_deployed"
	}
	return ss.Health
}

// State is the app-wide model shared by the rail, dashboard and screens.
type State struct {
	Project  *config.Project
	Branch   string
	Services map[string]*SvcState

	StatusLoaded bool
	StatusErr    error
	StatusAt     time.Time
	AuthErr      error

	History       []engine.HistoryEvent
	HistoryLoaded bool
	HistoryErr    error

	Docker       docker.Summary
	DockerLoaded bool
	DockerErr    error
	Containers   []docker.Container

	Fleet       fleet.Inventory
	FleetLoaded bool
	Cloud       fleet.Cloud
	// FleetHealth is the last `muster fleet status --json` answer, by
	// server name. It refreshes on its own while servers are shown.
	FleetHealth    map[string]fleet.MachineStatus
	FleetHealthAt  time.Time
	FleetHealthErr error

	Doctor      *engine.DoctorResult
	HookChanges []config.HookChange
	// Hooks is the CLI's own view (muster ≥ 0.7, hooks status --json).
	Hooks *engine.HooksStatus

	// Caps: what the installed muster CLI supports (from --version).
	Caps engine.Caps
	// DeployLock: a project-wide deploy/rollback holds the lock (≥ 0.7).
	DeployLock *engine.Lock
	// AssistantUnavailable is set when `muster ask` said we're not signed
	// in or it's turned off, so "Explain with AI" stays hidden.
	AssistantUnavailable string

	Toast      string
	ToastLevel int
	ToastUntil time.Time
}

func newState(p *config.Project, cwd string) *State {
	s := &State{Services: map[string]*SvcState{}}
	s.setProject(p, cwd)
	s.Cloud = fleet.CloudStatus()
	return s
}

func (s *State) setProject(p *config.Project, cwd string) {
	s.Project = p
	dir := cwd
	if p != nil {
		dir = p.Dir
		for _, k := range p.ServiceKeys() {
			if s.Services[k] == nil {
				s.Services[k] = &SvcState{Health: "unknown"}
			}
		}
		for k := range s.Services {
			if !p.HasService(k) {
				delete(s.Services, k)
			}
		}
		s.HookChanges = p.HookChanges()
	}
	s.Branch = config.GitBranch(dir)
}

// Svc returns (creating) a service's state.
func (s *State) Svc(key string) *SvcState {
	if s.Services[key] == nil {
		s.Services[key] = &SvcState{Health: "unknown"}
	}
	return s.Services[key]
}

// ServiceKeys returns configured services in deploy order.
func (s *State) ServiceKeys() []string {
	if s.Project == nil {
		return nil
	}
	return s.Project.ServiceKeys()
}

// Counts returns healthy/unhealthy/deploying/other counts.
func (s *State) Counts() (healthy, unhealthy, deploying, other int) {
	for _, k := range s.ServiceKeys() {
		st := s.Svc(k)
		switch {
		case st.Deploying:
			deploying++
		case st.Health == "healthy":
			healthy++
		case st.Shown() == "unhealthy":
			unhealthy++
		default:
			other++
		}
	}
	return
}

// PromptContext feeds the parser and completer.
func (s *State) PromptContext() prompt.Context {
	ctx := prompt.Context{Services: s.ServiceKeys(), Machines: s.Fleet.Names(), Groups: s.Fleet.GroupNames()}
	for _, c := range s.Containers {
		ctx.Containers = append(ctx.Containers, c.Name())
	}
	return ctx
}

// Data messages produced by loaders.
type (
	statusMsg struct {
		r   engine.StatusResult
		err error
	}
	historyMsg struct {
		evs []engine.HistoryEvent
		err error
	}
	dockerSummaryMsg struct {
		s   docker.Summary
		cs  []docker.Container
		err error
	}
	fleetMsg struct {
		inv fleet.Inventory
		err error
	}
	fleetHealthMsg struct {
		st  []fleet.MachineStatus
		err error
	}
	doctorMsg struct {
		r   engine.DoctorResult
		err error
		fix bool
	}
	versionMsg struct {
		out string
		err error
	}
	hooksMsg struct {
		hs  engine.HooksStatus
		err error
	}
	tokenMsg struct {
		token    string
		err      error
		repaired bool // the CLI's empty token store was restored first
	}
)

// apply folds a data message into the state. Returns true if it was one.
func (s *State) apply(msg tea.Msg, now time.Time) bool {
	switch m := msg.(type) {
	case statusMsg:
		s.StatusLoaded = true
		s.StatusErr = m.err
		if engine.IsAuth(m.err) {
			s.AuthErr = m.err
		}
		if m.err != nil {
			return true
		}
		s.AuthErr = nil
		s.StatusAt = now
		for k, v := range m.r.Services {
			st := s.Svc(k)
			h := v.Status
			if h == "" {
				h = "unknown"
			}
			if h != st.Health {
				st.Since = now
			}
			st.Health, st.Detail, st.CheckedAt = h, v.Detail, now
			st.Stack, st.Target, st.Remote = v.Stack, v.Target, v.Remote
			st.LockedBy, st.Replicas, st.CLILast = v.LockedBy, v.Replicas, v.LastDeploy
		}
		s.DeployLock = m.r.DeployLock
	case historyMsg:
		s.HistoryLoaded = true
		s.HistoryErr = m.err
		if m.err != nil && s.Project != nil {
			if evs, err := readEventsLog(s.Project.Dir); err == nil {
				m.evs, s.HistoryErr = evs, nil
			}
		}
		if m.evs != nil || m.err == nil {
			s.History = m.evs
		}
		for _, st := range s.Services {
			st.LastDeploy = nil
			st.NeverDeployed = true
		}
		for i := range s.History {
			ev := s.History[i]
			if ev.Action == "deploy" || ev.Action == "rollback" {
				if st, ok := s.Services[ev.Service]; ok {
					st.LastDeploy = &s.History[i]
					st.NeverDeployed = false
				}
			}
		}
	case dockerSummaryMsg:
		s.DockerLoaded = true
		s.DockerErr = m.err
		if m.err == nil {
			s.Docker = m.s
			s.Containers = m.cs
		} else {
			s.Containers = nil
		}
	case fleetMsg:
		s.FleetLoaded = true
		if m.err == nil {
			s.Fleet = m.inv
		}
		s.Cloud = fleet.CloudStatus()
	case fleetHealthMsg:
		s.FleetHealthErr = m.err
		if m.err == nil {
			if s.FleetHealth == nil {
				s.FleetHealth = map[string]fleet.MachineStatus{}
			}
			for _, ms := range m.st {
				s.FleetHealth[ms.Machine] = ms
			}
			s.FleetHealthAt = now
		}
	case doctorMsg:
		if m.err == nil {
			r := m.r
			s.Doctor = &r
		}
	case versionMsg:
		if m.err == nil {
			s.Caps = engine.CapsFor(m.out)
		}
	case hooksMsg:
		if m.err == nil {
			hs := m.hs
			s.Hooks = &hs
			s.HookChanges = hookChangesFrom(s.Project, hs)
		}
	default:
		return false
	}
	return true
}

// hookChangesFrom turns `hooks status --json` into the pending changes the
// banner, approval screen and service screen show.
func hookChangesFrom(p *config.Project, hs engine.HooksStatus) []config.HookChange {
	if !hs.SecurityEnabled {
		return nil
	}
	var out []config.HookChange
	for _, f := range hs.All() {
		if !f.Pending() {
			continue
		}
		path := f.Path
		if p != nil && path != "" && !filepath.IsAbs(path) {
			path = filepath.Join(p.Dir, path)
		}
		out = append(out, config.HookChange{Key: f.Key, Path: path, Approved: f.ApprovedCopy, Kind: f.State, Dangerous: f.Dangerous})
	}
	return out
}

// readEventsLog parses .muster/logs/deploy-events.log directly (used when
// the CLI can't be asked, e.g. before pairing).
func readEventsLog(dir string) ([]engine.HistoryEvent, error) {
	f, err := os.Open(filepath.Join(dir, ".muster", "logs", "deploy-events.log"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []engine.HistoryEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "{") {
			var raw struct {
				TS, Service, Action, Status, Commit, Source string
			}
			if json.Unmarshal([]byte(line), &raw) == nil {
				out = append(out, engine.HistoryEvent{Timestamp: raw.TS, Service: raw.Service, Action: raw.Action, Status: raw.Status, Commit: raw.Commit, Source: raw.Source})
			}
			continue
		}
		if parts := strings.Split(line, "|"); len(parts) >= 4 {
			ev := engine.HistoryEvent{Timestamp: parts[0], Service: parts[1], Action: parts[2], Status: parts[3]}
			if len(parts) > 4 {
				ev.Commit = parts[4]
			}
			out = append(out, ev)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time().Before(out[j].Time()) })
	return out, sc.Err()
}

// Loaders. Each returns a background task.

func (e *Env) loadStatus() tea.Cmd {
	eng := e.Eng()
	if eng == nil || e.State.Project == nil {
		return nil
	}
	_, cmd := e.Tasks.Run("checking health", "", false, func(ctx context.Context) tea.Msg {
		r, err := eng.Status(ctx)
		return statusMsg{r, err}
	})
	return cmd
}

func (e *Env) loadHistory() tea.Cmd {
	if e.State.Project == nil {
		return nil
	}
	eng := e.Eng()
	dir := e.State.Project.Dir
	_, cmd := e.Tasks.Run("loading history", "", false, func(ctx context.Context) tea.Msg {
		if eng == nil {
			evs, err := readEventsLog(dir)
			if os.IsNotExist(err) {
				err = nil
			}
			return historyMsg{evs, err}
		}
		evs, err := eng.History(ctx, true, "")
		return historyMsg{evs, err}
	})
	return cmd
}

func (e *Env) loadDocker() tea.Cmd {
	d := e.Docker
	if d == nil {
		return func() tea.Msg { return dockerSummaryMsg{err: docker.ErrNotInstalled} }
	}
	_, cmd := e.Tasks.Run("checking docker", "", false, func(ctx context.Context) tea.Msg {
		cs, err := d.Containers(ctx)
		if err != nil {
			return dockerSummaryMsg{err: err}
		}
		s := docker.Summary{Total: len(cs)}
		for _, c := range cs {
			if c.Running() {
				s.Running++
			}
		}
		if df, err := d.DiskUsage(ctx); err == nil {
			for _, x := range df {
				if x.Type == "Images" {
					s.ImagesSize, s.Reclaimable = x.Size, x.Reclaimable
				}
			}
		}
		return dockerSummaryMsg{s: s, cs: cs}
	})
	return cmd
}

func (e *Env) loadFleet() tea.Cmd {
	eng := e.Eng()
	dir := e.Cwd
	if e.State.Project != nil {
		dir = e.State.Project.Dir
	}
	_, cmd := e.Tasks.Run("loading fleet", "", false, func(ctx context.Context) tea.Msg {
		inv, err := fleet.List(ctx, eng, dir)
		return fleetMsg{inv, err}
	})
	return cmd
}

// fleetHealthLabel is the background task that runs `fleet status --json`.
const fleetHealthLabel = "checking servers"

// loadFleetHealth asks the CLI how every server is doing (cloud servers go
// through the relay, SSH servers over SSH; a few seconds).
func (e *Env) loadFleetHealth() tea.Cmd {
	eng := e.Eng()
	if eng == nil || len(e.State.Fleet.Machines) == 0 || e.Tasks.Busy(fleetHealthLabel) {
		return nil
	}
	_, cmd := e.Tasks.Run(fleetHealthLabel, "", false, func(ctx context.Context) tea.Msg {
		st, err := fleet.Status(ctx, eng, "")
		return fleetHealthMsg{st, err}
	})
	return cmd
}

// CheckingServers reports whether a fleet health check is running.
func (e *Env) CheckingServers() bool { return e.Tasks.Busy(fleetHealthLabel) }

// V07 reports whether the CLI has the muster 0.7 engine API.
func (e *Env) V07() bool { return e.State.Caps.EngineAPI }

func (e *Env) loadVersion() tea.Cmd {
	eng := e.Engine
	if eng == nil {
		return nil
	}
	_, cmd := e.Tasks.Run("checking muster version", "", false, func(ctx context.Context) tea.Msg {
		out, err := eng.Version(ctx)
		return versionMsg{out, err}
	})
	return cmd
}

// loadHooks asks the CLI which hooks need approval (muster ≥ 0.7).
func (e *Env) loadHooks() tea.Cmd {
	eng := e.Eng()
	if eng == nil || e.State.Project == nil || !e.V07() {
		return nil
	}
	_, cmd := e.Tasks.Run("checking hooks", "", false, func(ctx context.Context) tea.Msg {
		hs, err := eng.HooksStatus(ctx)
		return hooksMsg{hs, err}
	})
	return cmd
}

func (e *Env) loadDoctor(fix bool) tea.Cmd {
	eng := e.Eng()
	if eng == nil || e.State.Project == nil {
		return nil
	}
	_, cmd := e.Tasks.Run("running doctor", "", false, func(ctx context.Context) tea.Msg {
		r, err := eng.Doctor(ctx, fix)
		return doctorMsg{r, err, fix}
	})
	return cmd
}
