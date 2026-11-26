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
	// NoAutoLoad disables startup loads and timers (tests feed data).
	NoAutoLoad bool
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

	Doctor      *engine.DoctorResult
	HookChanges []config.HookChange

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
		case st.Health == "unhealthy":
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
	doctorMsg struct {
		r   engine.DoctorResult
		err error
		fix bool
	}
	tokenMsg struct {
		token string
		err   error
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
		}
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
		}
		for i := range s.History {
			ev := s.History[i]
			if ev.Action == "deploy" || ev.Action == "rollback" {
				if st, ok := s.Services[ev.Service]; ok {
					st.LastDeploy = &s.History[i]
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
	case doctorMsg:
		if m.err == nil {
			r := m.r
			s.Doctor = &r
		}
	default:
		return false
	}
	return true
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
