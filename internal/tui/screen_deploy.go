package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Muster-dev/muster-tui/internal/engine"
	"github.com/Muster-dev/muster-tui/internal/theme"
	tea "github.com/charmbracelet/bubbletea"
)

type runState int

const (
	rsPending runState = iota
	rsRunning
	rsOK
	rsFailed
	rsSkipped
	rsCanceled
	rsNotRun
)

type step struct {
	label      string
	state      runState
	start, end time.Time
}

// svcRun is one service's deploy attempt.
type svcRun struct {
	key, name  string
	state      runState
	steps      []*step
	logs       []string
	warnings   []string
	exitCode   int
	timeout    int
	logFile    string
	reason     string // skip reason / error message
	health     string // "", checking, healthy, unhealthy
	start, end time.Time
	attempts   int
	rolledBack bool
	collapsed  bool
	sawDone    bool
	hint       *engine.Hint // Likely cause / Try (muster ≥ 0.7)
	fixed      bool         // its fix was applied
	seen       int          // events received this attempt (stream mode)
}

func (r *svcRun) step(label string) *step {
	for _, s := range r.steps {
		if s.label == label {
			return s
		}
	}
	s := &step{label: label}
	r.steps = append(r.steps, s)
	return s
}

func (r *svcRun) addLog(l string) {
	r.logs = append(r.logs, l)
	if len(r.logs) > 5000 {
		r.logs = r.logs[len(r.logs)-5000:]
	}
}

type deployPhase int

const (
	phConfirm deployPhase = iota
	phRunning
	phFailed // waiting for a recovery choice
	phRollingBack
	phDone
	phAborted
)

var recoveryChoices = []string{"Retry", "Rollback", "Skip", "Abort"}

// deployScreen stops on a failure and offers Retry / Rollback / Skip /
// Abort (and Fix when muster knows one). On muster ≥ 0.7 it makes one
// `deploy --json --stop-on-failure` call and resumes with `--from <svc>`;
// older CLIs get one `deploy --json <svc>` call per service. Dry runs use a
// single `deploy --json --dry-run` call.
type deployScreen struct {
	env       *Env
	dry       bool
	force     bool
	explicit  bool // services were named (vs "everything")
	phase     deployPhase
	runs      []*svcRun
	cur       int
	taskID    int
	choice    int
	sel       int
	scroll    int
	started   time.Time
	ended     time.Time
	plan      []engine.DeployEvent
	summary   *engine.DeployEvent
	fatal     string
	v07       bool // one stop-on-failure stream instead of per-service calls
	explainID int
	fixID     int
}

func newDeploy(env *Env, services []string, dry, force, explicit bool) *deployScreen {
	d := &deployScreen{env: env, dry: dry, force: force, sel: -1, explicit: explicit, v07: env.V07()}
	keys := services
	if len(keys) == 0 && env.State.Project != nil {
		for _, s := range env.State.Project.Services() {
			if !s.SkipDeploy {
				keys = append(keys, s.Key)
			}
		}
	}
	for _, k := range keys {
		name := k
		if env.State.Project != nil {
			name = env.State.Project.Service(k).Name
		}
		d.runs = append(d.runs, &svcRun{key: k, name: name})
	}
	if !explicit && !dry && len(d.runs) > 1 {
		d.phase = phConfirm
	} else {
		d.phase = phRunning
	}
	return d
}

func (d *deployScreen) Key() string      { return "deploy" }
func (d *deployScreen) Persistent() bool { return true }
func (d *deployScreen) Title() string {
	if d.dry {
		return "Dry run"
	}
	return "Deploy"
}

func (d *deployScreen) running() bool {
	return d.phase == phRunning || d.phase == phRollingBack
}

func (d *deployScreen) failedService() string {
	if d.phase == phFailed && d.cur < len(d.runs) {
		return d.runs[d.cur].key
	}
	return ""
}

func (d *deployScreen) Init() tea.Cmd {
	if d.phase != phRunning {
		return nil
	}
	return d.begin()
}

func (d *deployScreen) begin() tea.Cmd {
	d.started = d.env.Now()
	d.phase = phRunning
	if len(d.runs) == 0 {
		d.phase = phDone
		d.fatal = "No services to deploy — every service has skip_deploy set."
		return nil
	}
	if d.dry {
		return d.startDry()
	}
	d.cur = 0
	return d.startRun()
}

// startDry asks the CLI for the plan. `muster deploy` takes a single
// service, so "everything" is one call (the CLI orders it) and a named
// subset is one call per service.
func (d *deployScreen) startDry() tea.Cmd {
	eng := d.env.Eng()
	calls := [][]string{nil}
	if d.explicit {
		calls = nil
		for _, r := range d.runs {
			calls = append(calls, []string{r.key})
		}
	}
	id, cmd := d.env.Tasks.Stream("dry run", d.Key(), true, func(ctx context.Context) (<-chan tea.Msg, error) {
		out := make(chan tea.Msg, 32)
		go func() {
			defer close(out)
			for _, svcs := range calls {
				s, err := eng.Deploy(ctx, svcs, engine.DeployOptions{DryRun: true})
				if err != nil {
					out <- deployExitMsg{err}
					return
				}
				for msg := range pipeDeploy(s) {
					out <- msg
				}
				if ctx.Err() != nil {
					return
				}
			}
		}()
		return out, nil
	})
	d.taskID = id
	return cmd
}

// startStream deploys the remaining services in one call that stops at the
// first failure (muster ≥ 0.7). Events are routed to services by name.
func (d *deployScreen) startStream() tea.Cmd {
	r := d.runs[d.cur]
	d.beginRun(r)
	var svcs []string
	if d.explicit {
		for _, x := range d.runs {
			svcs = append(svcs, x.key)
		}
	}
	opt := engine.DeployOptions{Force: d.force, StopOnFailure: true}
	if d.cur > 0 {
		opt.From = r.key
	}
	eng := d.env.Eng()
	id, cmd := d.env.Tasks.Stream("deploying "+r.key, d.Key(), true, func(ctx context.Context) (<-chan tea.Msg, error) {
		s, err := eng.Deploy(ctx, svcs, opt)
		if err != nil {
			return nil, err
		}
		return pipeDeploy(s), nil
	})
	d.taskID = id
	return cmd
}

// route makes the event's service the current one (stream mode): the
// previous service is finished when the next one starts.
func (d *deployScreen) route(ev engine.DeployEvent) {
	if ev.Service == "" || d.cur >= len(d.runs) || ev.Service == d.runs[d.cur].key {
		return
	}
	idx := -1
	for i, r := range d.runs {
		if r.key == ev.Service {
			idx = i
		}
	}
	if idx < 0 { // muster deployed something we didn't list
		name := ev.Service
		if p := d.env.State.Project; p != nil && p.HasService(ev.Service) {
			name = p.Service(ev.Service).Name
		}
		d.runs = append(d.runs, &svcRun{key: ev.Service, name: name})
		idx = len(d.runs) - 1
	}
	if idx < d.cur && d.runs[idx].state != rsPending {
		return
	}
	next := d.cur + 1
	switch prev := d.runs[d.cur]; {
	case prev.state == rsRunning && prev.seen == 0:
		// muster started with a different service than we guessed (deploy
		// phases, infra first): this one hasn't run yet.
		prev.state, prev.start = rsPending, time.Time{}
		prev.attempts--
		d.env.State.Svc(prev.key).Deploying = false
		next = d.cur
	case prev.state == rsRunning || prev.state == rsSkipped:
		d.finishRun(prev, false)
	}
	// Keep the list in muster's order.
	r := d.runs[idx]
	d.runs = append(d.runs[:idx], d.runs[idx+1:]...)
	if idx < next {
		next--
	}
	next = clamp(next, 0, len(d.runs))
	d.runs = append(d.runs[:next], append([]*svcRun{r}, d.runs[next:]...)...)
	d.cur = next
	if r.state == rsPending {
		d.beginRun(r)
	}
	d.env.Tasks.Relabel(d.taskID, "deploying "+ev.Service)
}

type deployEventMsg struct{ ev engine.DeployEvent }
type deployExitMsg struct{ err error }

func pipeDeploy(s *engine.DeployStream) <-chan tea.Msg {
	out := make(chan tea.Msg, 32)
	go func() {
		defer close(out)
		for ev := range s.C {
			out <- deployEventMsg{ev}
		}
		out <- deployExitMsg{s.Wait()}
	}()
	return out
}

// beginRun resets a service's attempt.
func (d *deployScreen) beginRun(r *svcRun) {
	r.state, r.steps, r.logs, r.warnings = rsRunning, nil, nil, nil
	r.exitCode, r.timeout, r.reason, r.health, r.sawDone = 0, 0, "", "", false
	r.start, r.end = d.env.Now(), time.Time{}
	r.attempts++
	r.collapsed, r.rolledBack, r.hint, r.fixed, r.seen = false, false, nil, false, 0
	d.env.State.Svc(r.key).Deploying = true
}

func (d *deployScreen) startRun() tea.Cmd {
	d.phase = phRunning // also after Retry from the recovery menu
	if d.v07 {
		return d.startStream()
	}
	r := d.runs[d.cur]
	d.beginRun(r)
	eng := d.env.Eng()
	key, force := r.key, d.force
	id, cmd := d.env.Tasks.Stream("deploying "+key, d.Key(), true, func(ctx context.Context) (<-chan tea.Msg, error) {
		s, err := eng.Deploy(ctx, []string{key}, engine.DeployOptions{Force: force})
		if err != nil {
			return nil, err
		}
		return pipeDeploy(s), nil
	})
	d.taskID = id
	return cmd
}

func (d *deployScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch m := msg.(type) {
	case fixConfirmedMsg:
		if m.owner == d.Key() {
			return d, d.runFix(m.svc)
		}
	case TaskMsg:
		if m.ID == d.explainID && d.explainID != 0 {
			if in, ok := m.Msg.(explainMsg); ok && in.err == nil && in.ex.Matched && in.ex.Hint != nil {
				if r := d.runFor(in.svc); r != nil && r.hint == nil {
					r.hint = in.ex.Hint
				}
			}
			return d, nil
		}
		if m.ID == d.fixID && d.fixID != 0 {
			return d, d.onFixMsg(m.Msg)
		}
		if m.ID != d.taskID {
			return d, nil
		}
		switch in := m.Msg.(type) {
		case deployEventMsg:
			return d, d.onEvent(in.ev)
		case deployExitMsg:
			d.onExit(in.err)
		case logBatchMsg: // rollback output
			if d.cur < len(d.runs) {
				for _, l := range in.lines {
					d.runs[d.cur].addLog(l)
				}
			}
		case logEndMsg:
			if d.phase == phRollingBack && d.cur < len(d.runs) {
				r := d.runs[d.cur]
				if in.err != nil {
					r.addLog("rollback failed: " + in.err.Error())
				} else {
					r.rolledBack = true
				}
			}
		case taskErrMsg:
			d.fatal = in.Err.Error()
			if d.cur < len(d.runs) {
				d.runs[d.cur].reason = in.Err.Error()
			}
		}
	case TaskEndedMsg:
		if m.ID == d.taskID {
			return d, d.onTaskEnd(m.Canceled)
		}
	}
	return d, nil
}

func (d *deployScreen) onEvent(ev engine.DeployEvent) tea.Cmd {
	now := d.env.Now()
	if d.dry {
		switch ev.Event {
		case "dry_run":
			d.plan = append(d.plan, ev)
		case "summary", "complete":
			e := ev
			d.summary = &e
		case "error":
			d.fatal = firstNonEmpty(ev.Message, ev.Reason, ev.Status)
		case "skipped":
			d.plan = append(d.plan, engine.DeployEvent{Event: "skipped", Service: ev.Service, Reason: ev.Reason})
		}
		return nil
	}
	if d.cur >= len(d.runs) {
		return nil
	}
	if d.v07 && d.phase == phRunning {
		d.route(ev)
	}
	r := d.runs[d.cur]
	r.seen++
	finishRunning := func() {
		for _, s := range r.steps {
			if s.state == rsRunning {
				s.state, s.end = rsOK, now
			}
		}
	}
	hook := func() *step {
		s := r.step("run deploy.sh")
		if s.state == rsPending {
			finishRunning()
			s.state, s.start = rsRunning, now
		}
		return s
	}
	switch ev.Event {
	case "start":
		r.logFile = ev.LogFile
		if ev.Name != "" {
			r.name = ev.Name
		}
	case "git_pull":
		label := "git pull"
		if ev.Remote != "" {
			label = fmt.Sprintf("git pull %s/%s", ev.Remote, firstNonEmpty(ev.Branch, "main"))
		}
		s := r.steps
		var gp *step
		for _, x := range s {
			if strings.HasPrefix(x.label, "git pull") {
				gp = x
			}
		}
		if gp == nil {
			gp = r.step(label)
		}
		switch ev.Status {
		case "pulling":
			gp.state, gp.start = rsRunning, now
		case "done":
			gp.state, gp.end = rsOK, now
		case "failed":
			gp.state, gp.end = rsFailed, now
			r.addLog(fmt.Sprintf("git pull failed (exit %d)", ev.ExitCode))
		}
	case "log", "output":
		hook()
		r.addLog(ev.Line)
	case "stderr":
		r.addLog(ev.Line)
	case "done":
		r.sawDone = true
		s := hook()
		s.end = now
		switch ev.Status {
		case "success":
			s.state = rsOK
		case "timeout":
			s.state = rsFailed
			r.timeout = ev.Timeout
			r.exitCode = 124
		case "blocked":
			s.state = rsFailed
			r.reason = "Blocked by muster's hook security check (it found a risky command or a changed hook):"
		default:
			s.state = rsFailed
			r.exitCode = ev.ExitCode
		}
	case "health":
		s := r.step("health check")
		switch ev.Status {
		case "checking":
			s.state, s.start = rsRunning, now
			r.health = "checking"
		case "healthy":
			s.state, s.end = rsOK, now
			r.health = "healthy"
		default:
			s.state, s.end = rsFailed, now
			r.health = "unhealthy"
		}
	case "verify_warning":
		r.warnings = append(r.warnings, ev.Message)
	case "hint":
		r.hint = ev.Hint()
	case "diagnosis":
		if h := ev.Hint(); h != nil && r.hint == nil {
			r.hint = h
		}
		if ev.PodStatus != "" {
			r.addLog("pod " + ev.PodStatus)
		}
	case "stopped":
		// stop-on-failure: the failed service is the current one. A lock
		// skip stops the run too, so it counts as a failure here.
		if r.state == rsSkipped {
			r.state, r.reason = rsRunning, skipText(r.reason)
		}
	case "skipped":
		r.state = rsSkipped
		r.reason = ev.Reason
	case "error":
		switch ev.Reason {
		case "hooks_not_approved":
			r.reason = "Hooks changed since you approved them — muster stopped before running anything."
			return nav(newApproval(d.env, "retry"))
		case "preflight_failed":
			r.reason = "A preflight check failed — muster stopped before running the hook:"
		default:
			r.reason = firstNonEmpty(ev.Message, ev.Reason, ev.Status)
		}
	case "summary", "complete":
		e := ev
		d.summary = &e
	case "approval_required", "hooks_changed", "hook_approval":
		r.reason = "hooks changed since approval"
		return nav(newApproval(d.env, "retry"))
	default:
		if t := firstNonEmpty(ev.Line, ev.Message); t != "" {
			r.addLog(t)
		}
	}
	return nil
}

func (d *deployScreen) onExit(err error) {
	if d.dry || d.cur >= len(d.runs) {
		if err != nil && d.fatal == "" && len(d.plan) == 0 {
			d.fatal = err.Error()
		}
		return
	}
	r := d.runs[d.cur]
	if err != nil && r.reason == "" && !r.sawDone {
		r.reason = err.Error()
	}
}

func (d *deployScreen) onTaskEnd(canceled bool) tea.Cmd {
	now := d.env.Now()
	if d.dry {
		d.phase, d.ended = phDone, now
		if canceled {
			d.phase = phAborted
		}
		return nil
	}
	if d.phase == phRollingBack {
		d.phase = phFailed
		d.choice = 0
		return refresh("history")
	}
	if d.cur >= len(d.runs) {
		return nil
	}
	r := d.runs[d.cur]
	d.finishRun(r, canceled)
	switch {
	case canceled:
		d.markRest(rsNotRun)
		d.phase, d.ended = phAborted, now
		return tea.Batch(refresh("status"), refresh("history"))
	case r.state == rsFailed:
		d.phase, d.choice = phFailed, 0
		cmds := []tea.Cmd{refresh("history")}
		if d.env.Active == d.Key() {
			cmds = append(cmds, func() tea.Msg { return focusMsg{focusMain} })
		} else {
			cmds = append(cmds, toast(lvlErr, r.key+" failed to deploy — retry, rollback "+r.key+", skip or abort"))
		}
		if d.v07 && r.hint == nil && r.sawDone {
			// No hint event: ask the offline rules engine directly.
			id, cmd := loadExplain(d.env, d.Key(), r.key)
			d.explainID = id
			cmds = append(cmds, cmd)
		}
		return tea.Batch(cmds...)
	case d.v07:
		// The stream covered every remaining service.
		for i := d.cur + 1; i < len(d.runs); i++ {
			if d.runs[i].state == rsPending {
				d.runs[i].state, d.runs[i].reason = rsSkipped, "not deployed by muster"
			}
		}
		d.cur = len(d.runs) - 1
		return d.advance()
	default:
		return d.advance()
	}
}

// finishRun settles a service's state once its attempt is over.
func (d *deployScreen) finishRun(r *svcRun, canceled bool) {
	now := d.env.Now()
	r.end = now
	d.env.State.Svc(r.key).Deploying = false
	for _, s := range r.steps {
		if s.state == rsRunning {
			s.state, s.end = rsFailed, now
			if canceled {
				s.state = rsCanceled
			}
		}
	}
	switch {
	case canceled:
		r.state = rsCanceled
	case r.state == rsSkipped:
		r.collapsed = true
	case r.exitCode != 0 || r.health == "unhealthy" || (!r.sawDone && r.reason != "") || d.hookFailed(r):
		r.state = rsFailed
	default:
		r.state = rsOK
		r.collapsed = true
	}
}

func (d *deployScreen) failExit(r *svcRun) int {
	if r.timeout > 0 {
		return 124
	}
	return r.exitCode
}

func (d *deployScreen) runFor(key string) *svcRun {
	for _, r := range d.runs {
		if r.key == key {
			return r
		}
	}
	return nil
}

// runFix applies the failed service's fix (the user already confirmed).
func (d *deployScreen) runFix(svc string) tea.Cmd {
	r := d.runFor(svc)
	if r == nil || d.phase != phFailed {
		return nil
	}
	r.addLog("── fix ──")
	id, cmd := startFix(d.env, d.Key(), svc)
	d.fixID = id
	return cmd
}

func (d *deployScreen) onFixMsg(msg tea.Msg) tea.Cmd {
	if d.cur >= len(d.runs) {
		return nil
	}
	r := d.runs[d.cur]
	switch in := msg.(type) {
	case logBatchMsg:
		for _, l := range in.lines {
			r.addLog(l)
		}
	case logEndMsg:
		if in.err != nil {
			r.addLog("fix failed: " + in.err.Error())
			return toast(lvlErr, "The fix failed: "+in.err.Error())
		}
		r.fixed = true
		d.choice = 0
		return tea.Batch(toast(lvlOK, "Fix applied — r to retry "+r.key), func() tea.Msg { return reloadConfigMsg{} }, refresh("hooks"))
	case taskErrMsg:
		r.addLog("fix failed: " + in.Err.Error())
	}
	return nil
}

// choices are the recovery buttons for the failed service.
func (d *deployScreen) choices() []string {
	out := append([]string(nil), recoveryChoices...)
	if d.cur < len(d.runs) {
		if h := d.runs[d.cur].hint; h != nil && h.Fix != nil && !d.runs[d.cur].fixed {
			out = append(out, "Fix")
		}
	}
	if ok, _ := d.env.assistantReady(); ok {
		out = append(out, "Explain with AI")
	}
	return out
}

func (d *deployScreen) hookFailed(r *svcRun) bool {
	for _, s := range r.steps {
		if s.state == rsFailed {
			return true
		}
	}
	return false
}

func (d *deployScreen) markRest(st runState) {
	for i := d.cur + 1; i < len(d.runs); i++ {
		if d.runs[i].state == rsPending {
			d.runs[i].state = st
		}
	}
}

func (d *deployScreen) advance() tea.Cmd {
	d.cur++
	if d.cur < len(d.runs) {
		return d.startRun()
	}
	d.phase, d.ended = phDone, d.env.Now()
	cmds := []tea.Cmd{refresh("status"), refresh("history"), refresh("docker")}
	if d.env.Active != d.Key() {
		cmds = append(cmds, toast(lvlOK, "Deploy finished — "+plural(len(d.runs), "service")))
	}
	return tea.Batch(cmds...)
}

// recover applies a recovery choice (also reachable from the prompt).
func (d *deployScreen) recover(choice string) (Screen, tea.Cmd) {
	if d.phase != phFailed {
		if choice == "abort" && d.running() {
			d.env.Tasks.Cancel(d.taskID)
			return d, nil
		}
		return d, toast(lvlInfo, "Nothing to "+choice)
	}
	r := d.runs[d.cur]
	if d.env.Tasks.Running(d.fixID) {
		return d, toast(lvlInfo, "The fix is still running")
	}
	switch strings.ToLower(choice) {
	case "retry":
		if d.v07 && len(d.env.State.HookChanges) > 0 {
			// A fix (or an edit) changed muster.json or a hook: approve it
			// first, then the approval screen runs `retry`.
			return d, nav(newApproval(d.env, "retry"))
		}
		return d, d.startRun()
	case "explain with ai":
		if ok, why := d.env.assistantReady(); !ok {
			return d, toast(lvlInfo, "Muster Assistant: "+why)
		}
		return d, nav(newAskForFailure(d.env, r.key, d.failExit(r), r.logFile))
	case "fix":
		if r.hint == nil || r.hint.Fix == nil {
			return d, toast(lvlInfo, "muster has no automatic fix for this one")
		}
		return d, confirmFix(d.Key(), r.key, r.hint)
	case "rollback":
		if r.rolledBack {
			return d, toast(lvlInfo, r.key+" was already rolled back")
		}
		d.phase = phRollingBack
		r.addLog("── rollback ──")
		eng := d.env.Eng()
		key := r.key
		v07 := d.v07
		id, cmd := d.env.Tasks.Stream("rolling back "+key, d.Key(), true, func(ctx context.Context) (<-chan tea.Msg, error) {
			var lines <-chan string
			var wait func() error
			var err error
			if v07 {
				var s *engine.DeployStream
				if s, err = eng.RollbackStream(ctx, key); err == nil {
					lines, wait, err = streamLines(s, actionEventLines)
				}
			} else {
				lines, wait, err = eng.Rollback(ctx, key)
			}
			if err != nil {
				return nil, err
			}
			out := make(chan tea.Msg, 8)
			go func() {
				defer close(out)
				for l := range lines {
					out <- logBatchMsg{[]string{l}}
				}
				out <- logEndMsg{wait()}
			}()
			return out, nil
		})
		d.taskID = id
		return d, cmd
	case "skip":
		r.state = rsSkipped
		r.reason = "skipped after failure"
		r.collapsed = true
		d.phase = phRunning
		return d, d.advance()
	case "abort":
		d.markRest(rsNotRun)
		d.phase, d.ended = phAborted, d.env.Now()
		return d, tea.Batch(refresh("status"), refresh("history"))
	}
	return d, nil
}

func (d *deployScreen) HandleKey(k tea.KeyMsg) (Screen, tea.Cmd, bool) {
	key := k.String()
	switch d.phase {
	case phConfirm:
		switch key {
		case "enter", "y":
			return d, d.begin(), true
		case "d":
			d.dry = true
			return d, d.begin(), true
		case "esc", "n":
			return d, func() tea.Msg { return backMsg{} }, true
		}
		return d, nil, false
	case phFailed:
		ch := d.choices()
		switch key {
		case "left", "h":
			d.choice = (d.choice + len(ch) - 1) % len(ch)
			return d, nil, true
		case "right", "l", "tab":
			d.choice = (d.choice + 1) % len(ch)
			return d, nil, true
		case "enter":
			s, cmd := d.recover(ch[min(d.choice, len(ch)-1)])
			return s, cmd, true
		case "f":
			s, cmd := d.recover("fix")
			return s, cmd, true
		case "e":
			s, cmd := d.recover("explain with ai")
			return s, cmd, true
		case "r":
			s, cmd := d.recover("retry")
			return s, cmd, true
		case "b":
			s, cmd := d.recover("rollback")
			return s, cmd, true
		case "s":
			s, cmd := d.recover("skip")
			return s, cmd, true
		case "a", "esc":
			s, cmd := d.recover("abort")
			return s, cmd, true
		}
	case phRunning, phRollingBack:
		if key == "esc" {
			d.env.Tasks.Cancel(d.taskID)
			return d, toast(lvlWarn, "Cancelling deploy…"), true
		}
	case phDone, phAborted:
		switch key {
		case "enter":
			if d.dry && d.phase == phDone && d.fatal == "" {
				var keys []string
				for _, r := range d.runs {
					keys = append(keys, r.key)
				}
				if !d.explicit {
					keys = nil
				}
				nd := newDeploy(d.env, keys, false, d.force, d.explicit)
				return d, nav(nd), true
			}
			return d, func() tea.Msg { return backMsg{} }, true
		case "esc":
			return d, func() tea.Msg { return backMsg{} }, true
		}
	}
	switch key {
	case "j", "down":
		d.sel = clamp(d.sel+1, 0, len(d.runs)-1)
		return d, nil, true
	case "k", "up":
		d.sel = clamp(d.sel-1, 0, len(d.runs)-1)
		return d, nil, true
	case " ", "o":
		if d.sel >= 0 && d.sel < len(d.runs) {
			d.runs[d.sel].collapsed = !d.runs[d.sel].collapsed
		}
		return d, nil, true
	case "pgup":
		d.scroll += 5
		return d, nil, true
	case "pgdown":
		d.scroll = max(d.scroll-5, 0)
		return d, nil, true
	}
	return d, nil, false
}

// ── View ──

func runGlyph(st runState, spin string) string {
	switch st {
	case rsRunning:
		return theme.AccentFg.Render(spin)
	case rsOK:
		return theme.OK.Render(theme.GlyphOK)
	case rsFailed:
		return theme.Err.Render(theme.GlyphFail)
	case rsSkipped, rsNotRun:
		return theme.Dim.Render(theme.GlyphSkip)
	case rsCanceled:
		return theme.Warning.Render(theme.GlyphSkip)
	}
	return theme.Dim.Render(theme.GlyphPending)
}

func (d *deployScreen) View(w, h int) string {
	if d.phase == phConfirm {
		return d.confirmView(w, h)
	}
	if d.dry {
		return d.dryView(w, h)
	}
	now := d.env.Now()
	title := "Deploy"
	if d.cur < len(d.runs) && d.running() {
		title += " · " + d.runs[d.cur].key
	}
	elapsed := ""
	if !d.started.IsZero() {
		end := now
		if !d.ended.IsZero() {
			end = d.ended
		}
		elapsed = theme.Dim.Render(dur(end.Sub(d.started)))
	}
	head := []string{spread(section(title), elapsed, w)}

	if d.env.Fullscreen && d.cur < len(d.runs) {
		r := d.runs[d.cur]
		if d.sel >= 0 && d.sel < len(d.runs) {
			r = d.runs[d.sel]
		}
		head[0] = spread(section("Log · "+r.key), theme.Dim.Render(fmt.Sprintf("%d lines", len(r.logs))), w)
		logs := r.logs
		end := max(len(logs)-d.scroll, 0)
		start := max(end-(h-1), 0)
		for _, l := range logs[start:end] {
			head = append(head, styleLogLine(l, w))
		}
		return block(head, w, h)
	}

	var body []string
	for i, r := range d.runs {
		body = append(body, d.runLines(i, r, w)...)
	}
	var foot []string
	switch d.phase {
	case phFailed, phRollingBack:
		foot = d.recoveryLines(w)
	case phDone, phAborted:
		foot = d.summaryLines(w)
	}
	avail := h - len(head) - len(foot)
	if avail < len(body) {
		// keep the current service visible: drop from the top
		body = body[len(body)-max(avail, 0):]
	}
	lines := append(head, body...)
	if len(foot) > 0 {
		for len(lines) < h-len(foot) {
			lines = append(lines, "")
		}
		lines = append(lines, foot...)
	}
	return block(lines, w, h)
}

func (d *deployScreen) runLines(i int, r *svcRun, w int) []string {
	now := d.env.Now()
	spin := d.env.Spin()
	timing := ""
	if !r.start.IsZero() {
		end := now
		if !r.end.IsZero() {
			end = r.end
		}
		timing = theme.Dim.Render(dur(end.Sub(r.start)))
	}
	label := r.key
	if r.name != "" && !strings.EqualFold(r.name, r.key) {
		label += theme.Dim.Render("  " + r.name)
	}
	note := ""
	switch r.state {
	case rsSkipped:
		note = theme.Dim.Render(skipText(r.reason))
	case rsNotRun:
		note = theme.Dim.Render("not run")
	case rsCanceled:
		note = theme.Warning.Render("cancelled")
	case rsFailed:
		if r.rolledBack {
			note = theme.Warning.Render("rolled back")
		}
	}
	if r.attempts > 1 && r.state == rsRunning {
		note = theme.Dim.Render(fmt.Sprintf("attempt %d", r.attempts))
	}
	right := strings.TrimSpace(note + "  " + timing)
	line := runGlyph(r.state, spin) + " " + label
	if i == d.sel {
		line = theme.AccentFg.Render(theme.GlyphCursor) + line
	} else {
		line = " " + line
	}
	out := []string{spread(line, right, w)}
	expanded := r.state == rsRunning || r.state == rsFailed || (!r.collapsed && r.state != rsPending && r.state != rsNotRun)
	if !expanded {
		return out
	}
	for _, s := range r.steps {
		t := ""
		if !s.start.IsZero() {
			end := now
			if !s.end.IsZero() {
				end = s.end
			}
			t = theme.Dim.Render(dur(end.Sub(s.start)))
		}
		out = append(out, spread("   "+runGlyph(s.state, spin)+" "+s.label, t, w))
		if s.label == "run deploy.sh" && (s.state == rsRunning) {
			for _, l := range tail(r.logs, 3) {
				out = append(out, "     "+theme.Rule.Render("│")+" "+theme.Dim.Render(trunc(l, w-8)))
			}
		}
	}
	if r.state == rsRunning && len(r.steps) == 0 {
		out = append(out, "   "+theme.AccentFg.Render(spin)+theme.Dim.Render(" starting…"))
	}
	if r.state == rsOK {
		for _, wn := range r.warnings {
			out = append(out, "   "+theme.Warning.Render(theme.GlyphWarn)+" "+theme.Dim.Render(trunc(wn, w-6)))
		}
	}
	return out
}

func skipText(reason string) string {
	switch reason {
	case "locked":
		return "skipped — another deploy holds the lock"
	case "":
		return "skipped"
	}
	return reason
}

func (d *deployScreen) recoveryLines(w int) []string {
	r := d.runs[d.cur]
	var out []string
	what := "failed"
	switch {
	case r.timeout > 0:
		what = fmt.Sprintf("timed out after %ds", r.timeout)
	case r.exitCode > 0:
		what = fmt.Sprintf("failed (exit %d)", r.exitCode)
	case r.health == "unhealthy":
		what = "deployed but its health check failed"
	case r.reason != "" && strings.HasPrefix(r.reason, "A preflight"):
		what = "didn't start"
	}
	out = append(out, rule(w))
	out = append(out, theme.Err.Render(theme.GlyphFail+" "+r.key+" "+what))
	if r.hint != nil {
		for _, l := range hintLines(r.hint, w-2) {
			out = append(out, "  "+l)
		}
		if f := r.hint.Fix; f != nil {
			if r.fixed {
				out = append(out, "  "+theme.OK.Render(theme.GlyphOK+" Fixed: ")+f.Label+theme.Dim.Render(" — r to retry"))
			} else {
				out = append(out, "  "+theme.OK.Render("Fix: ")+f.Label+theme.Dim.Render(" · f"))
			}
		}
	} else {
		for _, l := range diagnose(r) {
			for _, wl := range wrap(l, w-2) {
				out = append(out, "  "+wl)
			}
		}
	}
	for _, l := range tail(nonEmpty(r.logs), 5) {
		out = append(out, "  "+theme.Rule.Render("│")+" "+styleLogLine(l, w-4))
	}
	if r.logFile != "" {
		out = append(out, "  "+theme.Dim.Render("Full log: "+relPath(d.env, r.logFile)+" · Ctrl+O"))
	}
	if d.phase == phRollingBack {
		out = append(out, "", theme.AccentFg.Render(d.env.Spin())+" Rolling back "+r.key+"…")
		return out
	}
	if d.env.Tasks.Running(d.fixID) {
		out = append(out, "", theme.AccentFg.Render(d.env.Spin())+" Applying the fix…")
		return out
	}
	var btns []string
	ch := d.choices()
	for i, c := range ch {
		label := c
		if c == "Rollback" && r.rolledBack {
			label = "Rolled back"
		}
		btns = append(btns, button(label, i == d.choice))
	}
	keys := "r · b · s · a"
	for _, c := range ch[len(recoveryChoices):] {
		switch c {
		case "Fix":
			keys += " · f"
		case "Explain with AI":
			keys += " · e"
		}
	}
	out = append(out, "", strings.Join(btns, " ")+"   "+theme.Dim.Render(keys))
	return out
}

func (d *deployScreen) summaryLines(w int) []string {
	var ok, failed, skipped int
	for _, r := range d.runs {
		switch r.state {
		case rsOK:
			ok++
		case rsFailed:
			failed++
		case rsSkipped, rsNotRun, rsCanceled:
			skipped++
		}
	}
	took := ""
	if !d.ended.IsZero() {
		took = " in " + dur(d.ended.Sub(d.started))
	}
	out := []string{rule(w)}
	switch {
	case d.fatal != "":
		out = append(out, theme.Err.Render(theme.GlyphFail+" ")+d.fatal)
	case d.phase == phAborted:
		out = append(out, theme.Warning.Render(theme.GlyphWarn+" Deploy stopped")+theme.Dim.Render(fmt.Sprintf(" — %d deployed, %d not run", ok, skipped+failed)))
	case failed == 0 && skipped == 0:
		out = append(out, theme.OK.Render(theme.GlyphOK+" Deployed "+plural(ok, "service")+took))
	default:
		out = append(out, theme.Warning.Render(theme.GlyphWarn+" Finished with problems")+theme.Dim.Render(fmt.Sprintf(" — %d ok, %d failed, %d skipped%s", ok, failed, skipped, took)))
	}
	next := keyHint("Enter", "back") + theme.Dim.Render(" · ") + code("status")
	if len(d.runs) > 0 {
		next += theme.Dim.Render(" · ") + code("logs "+d.runs[len(d.runs)-1].key)
	}
	out = append(out, next)
	return out
}

func (d *deployScreen) confirmView(w, h int) string {
	lines := []string{section(fmt.Sprintf("Deploy %s", plural(len(d.runs), "service"))), ""}
	for i, r := range d.runs {
		meta := ""
		if p := d.env.State.Project; p != nil {
			s := p.Service(r.key)
			meta = strings.TrimSpace(p.Stack(r.key) + "  " + s.HealthLabel())
			if s.Remote.Enabled {
				meta += "  → " + s.Remote.Host
			}
		}
		lines = append(lines, fmt.Sprintf(" %s  %s  %s", theme.Dim.Render(fmt.Sprintf("%d", i+1)), fit(r.key, 14), theme.Dim.Render(meta)))
	}
	lines = append(lines, "", theme.Dim.Render("In this order. A failure stops here and lets you retry, roll back or skip."), "",
		keyHint("Enter", "deploy", "d", "dry run first", "Esc", "cancel"))
	return block(lines, w, h)
}

func (d *deployScreen) dryView(w, h int) string {
	lines := []string{spread(section("Dry run"), theme.Dim.Render("nothing will change"), w), ""}
	for _, ev := range d.plan {
		if ev.Event == "skipped" {
			lines = append(lines, fmt.Sprintf(" %s %s %s", theme.Dim.Render(theme.GlyphSkip), fit(ev.Service, 14), theme.Dim.Render(skipText(ev.Reason))))
			continue
		}
		hook := ev.Hook
		if d.env.State.Project != nil {
			hook = relPath(d.env, hook)
		}
		lines = append(lines, fmt.Sprintf(" %s %s %s", theme.OK.Render(theme.GlyphOK), fit(ev.Service, 14), theme.Dim.Render(fmt.Sprintf("%s · %d lines", hook, ev.HookLines))))
	}
	if d.phase == phRunning && len(d.plan) == 0 {
		lines = append(lines, " "+theme.AccentFg.Render(d.env.Spin())+theme.Dim.Render(" building the plan…"))
	}
	if d.phase == phDone {
		lines = append(lines, "")
		if d.fatal != "" {
			lines = append(lines, theme.Err.Render(theme.GlyphFail+" ")+d.fatal)
			lines = append(lines, keyHint("Esc", "back"))
		} else {
			lines = append(lines, fmt.Sprintf("Plan: %s, in this order. Nothing was changed.", plural(len(d.plan), "service")), "")
			lines = append(lines, keyHint("Enter", "deploy for real", "Esc", "back"))
		}
	}
	if d.phase == phAborted {
		lines = append(lines, "", theme.Warning.Render("Dry run cancelled"))
	}
	return block(lines, w, h)
}

func (d *deployScreen) Hint() string {
	switch d.phase {
	case phConfirm:
		return keyHint("Enter", "start", "d", "dry run first", "Esc", "cancel")
	case phRunning:
		if d.dry {
			return theme.AccentFg.Render(d.env.Spin()) + " Planning — " + keyHint("Esc", "cancel")
		}
		if d.cur < len(d.runs) {
			r := d.runs[d.cur]
			verb := "deploying"
			for _, s := range r.steps {
				if s.state == rsRunning {
					verb = strings.TrimPrefix(s.label, "run ")
				}
			}
			return theme.AccentFg.Render(d.env.Spin()) + " " + r.key + " — " + verb + theme.Dim.Render(" · ") + keyHint("Ctrl+O", "full log", "Esc", "cancel")
		}
	case phRollingBack:
		return theme.AccentFg.Render(d.env.Spin()) + " Rolling back — " + keyHint("Esc", "cancel")
	case phFailed:
		pairs := []string{"r", "retry", "b", "rollback", "s", "skip", "a", "abort"}
		for _, c := range d.choices()[len(recoveryChoices):] {
			switch c {
			case "Fix":
				pairs = append(pairs, "f", "fix")
			case "Explain with AI":
				pairs = append(pairs, "e", "explain with AI")
			}
		}
		return theme.Err.Render(d.runs[d.cur].key+" failed") + theme.Dim.Render(" — ") + keyHint(pairs...)
	}
	return ""
}

func (d *deployScreen) Keys() []KeyHelp {
	return []KeyHelp{
		{"Enter", "start / choose"}, {"r b s a", "retry · rollback · skip · abort"}, {"f", "apply muster's fix (asks first)"}, {"e", "explain with AI (Fleet Cloud)"}, {"← →", "move between choices"},
		{"j / k", "select a service"}, {"Space", "expand / collapse"}, {"Ctrl+O", "full log"}, {"Esc", "cancel deploy"},
	}
}

// diagnose explains a failure in plain words from exit codes and log text.
func diagnose(r *svcRun) []string {
	text := strings.ToLower(strings.Join(tail(r.logs, 200), "\n"))
	var out []string
	has := func(s ...string) bool {
		for _, x := range s {
			if strings.Contains(text, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("cannot connect to the docker daemon", "failed to connect to the docker api", "is the docker daemon running"):
		out = append(out, "Docker isn't running. Start Docker Desktop (or `colima start`), then Retry.")
	case has("unknown shorthand flag: 'f' in -f") && has("docker"):
		out = append(out, "This Docker has no compose plugin, so `docker compose` failed. Install Docker Compose v2, then Retry.")
	case has("port is already allocated", "address already in use"):
		out = append(out, "A port this service needs is already in use. Stop whatever holds it (`docker` shows containers), then Retry.")
	case has("pull access denied", "repository does not exist", "manifest unknown"):
		out = append(out, "The image couldn't be pulled. Check the image name and tag, or log in to the registry.")
	case has("imagepullbackoff", "errimagepull"):
		out = append(out, "Kubernetes can't pull the image. Check the tag exists and the cluster has registry credentials.")
	case has("crashloopbackoff"):
		out = append(out, "The new pods keep crashing. Check `logs "+r.key+"`, or Rollback.")
	case has("oomkilled"):
		out = append(out, "The container ran out of memory. Raise its memory limit, or Rollback.")
	case has("could not resolve host", "no such host", "temporary failure in name resolution"):
		out = append(out, "A host name didn't resolve — check your network or VPN.")
	case has("permission denied"):
		out = append(out, "Permission denied — check file permissions, SSH keys or credentials.")
	case has("git pull failed"):
		out = append(out, "git pull failed — commit or stash local changes, or check the remote.")
	case r.exitCode == 127 || has("command not found"):
		out = append(out, "The hook calls a command that isn't installed on this machine.")
	}
	if r.timeout > 0 {
		out = append(out, fmt.Sprintf("The hook ran longer than %ds. Raise deploy_timeout in `settings %s`, or look for a hung step.", r.timeout, r.key))
	}
	if r.health == "unhealthy" && len(out) == 0 {
		out = append(out, "The deploy hook finished, but the service didn't pass its health check. Check `logs "+r.key+"`, or Rollback.")
	}
	if r.reason != "" && len(out) == 0 {
		out = append(out, r.reason)
	}
	return out
}

func styleLogLine(l string, w int) string {
	l = trunc(strings.ReplaceAll(l, "\t", "    "), w)
	switch lineLevel(l) {
	case 2:
		return theme.Err.Render(l)
	case 1:
		return theme.Warning.Render(l)
	}
	return theme.Dim.Render(l)
}

func nonEmpty(xs []string) []string {
	var out []string
	for _, x := range xs {
		if strings.TrimSpace(x) != "" && !strings.HasPrefix(x, "── ") {
			out = append(out, x)
		}
	}
	return out
}

func relPath(env *Env, p string) string {
	if env.State.Project == nil || p == "" {
		return p
	}
	if r, err := filepath.Rel(env.State.Project.Dir, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}
