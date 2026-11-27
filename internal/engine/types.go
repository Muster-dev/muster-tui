package engine

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

// ServiceStatus is one entry of `muster status --json`. The fields after
// Detail come from muster ≥ 0.7 and are zero on older CLIs.
type ServiceStatus struct {
	Key        string `json:"-"`
	Name       string `json:"name"`
	Status     string `json:"status"` // healthy, unhealthy, disabled, no_health_check, blocked
	HealthType string `json:"health_type"`
	Detail     string `json:"detail"`

	Stack      string      `json:"stack"`  // k8s, compose, docker, dev, bare
	Target     string      `json:"target"` // what the health check aims at
	Remote     string      `json:"remote"` // user@host[:port]
	LastDeploy *LastDeploy `json:"last_deploy"`
	LockedBy   *Lock       `json:"locked_by"`
	Deploying  bool        `json:"deploying"`
	Replicas   *Replicas   `json:"replicas"`
}

// LastDeploy is the newest deploy event for a service (status --json).
type LastDeploy struct {
	TS     string `json:"ts"`
	Status string `json:"status"`
	Commit string `json:"commit"`
	Source string `json:"source"`
}

// Lock is a deploy lock holder.
type Lock struct {
	User     string `json:"user"`
	Host     string `json:"host"`
	Source   string `json:"source"`
	PID      int    `json:"pid"`
	Started  string `json:"started"`
	Services string `json:"services"` // project lock only
}

// Who renders "user@host" (or just the user).
func (l *Lock) Who() string {
	if l == nil {
		return ""
	}
	who := l.User
	if l.Host != "" {
		if who != "" {
			who += "@"
		}
		who += l.Host
	}
	if who == "" {
		who = "someone"
	}
	return who
}

// Replicas is a k8s deployment's ready/desired count.
type Replicas struct {
	Ready   int `json:"ready"`
	Desired int `json:"desired"`
}

// StatusResult is `muster status --json`.
type StatusResult struct {
	Project    string                   `json:"project"`
	CheckedAt  string                   `json:"checked_at"`
	Deploying  bool                     `json:"deploying"`
	DeployLock *Lock                    `json:"deploy_lock"`
	Services   map[string]ServiceStatus `json:"services"`
}

// Status runs `muster status --json`.
func (e *Engine) Status(ctx context.Context) (StatusResult, error) {
	var r StatusResult
	err := e.runJSON(ctx, &r, false, "status", "--json")
	for k, v := range r.Services {
		v.Key = k
		r.Services[k] = v
	}
	return r, err
}

// HistoryEvent is one deploy/rollback record.
type HistoryEvent struct {
	Timestamp string  `json:"timestamp"`
	Service   string  `json:"service"`
	Action    string  `json:"action"`
	Status    string  `json:"status"`
	Commit    string  `json:"commit"`
	Source    string  `json:"source"`
	Duration  float64 `json:"duration"`
	Image     string  `json:"image"`
	Version   string  `json:"version"`
}

// Time parses the timestamp (local "2006-01-02 15:04:05" or RFC 3339).
func (h HistoryEvent) Time() time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, h.Timestamp, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}

// OK reports whether the event succeeded.
func (h HistoryEvent) OK() bool {
	s := strings.ToLower(h.Status)
	return s == "ok" || s == "success" || s == "succeeded" || s == "done"
}

// History runs `muster history --json` (oldest first).
func (e *Engine) History(ctx context.Context, all bool, service string) ([]HistoryEvent, error) {
	args := []string{"history", "--json"}
	if all {
		args = append(args, "--all")
	}
	if service != "" {
		args = append(args, service)
	}
	var evs []HistoryEvent
	err := e.runJSON(ctx, &evs, false, args...)
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].Time().Before(evs[j].Time()) })
	return evs, err
}

// DoctorCheck is one doctor check.
type DoctorCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // pass, warn, fail
	Category string `json:"category"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix"`
	Fixable  bool   `json:"fixable"`
}

// DoctorResult is `muster doctor --json`.
type DoctorResult struct {
	Pass     int           `json:"pass"`
	Warnings int           `json:"warnings"`
	Failures int           `json:"failures"`
	Checks   []DoctorCheck `json:"checks"`
}

// Doctor runs `muster doctor --json [--fix]`.
func (e *Engine) Doctor(ctx context.Context, fix bool) (DoctorResult, error) {
	args := []string{"doctor", "--json"}
	if fix {
		args = append(args, "--fix")
	}
	var r DoctorResult
	err := e.runJSON(ctx, &r, true, args...)
	return r, err
}

// DeployEvent is one NDJSON line from `muster deploy --json`. Fields not
// known to this version are kept in Raw; non-JSON lines arrive as events
// "output" (stdout) or "stderr".
type DeployEvent struct {
	Event     string    `json:"event"`
	Service   string    `json:"service"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Line      string    `json:"line"`
	Message   string    `json:"message"`
	Reason    string    `json:"reason"`
	LogFile   string    `json:"log_file"`
	Hook      string    `json:"hook"`
	Remote    string    `json:"remote"`
	Branch    string    `json:"branch"`
	Index     int       `json:"index"`
	Total     int       `json:"total"`
	ExitCode  int       `json:"exit_code"`
	HookLines int       `json:"hook_lines"`
	Timeout   int       `json:"timeout"`
	DryRun    bool      `json:"dry_run"`
	Duration  float64   `json:"duration"`
	Files     []FileRef `json:"files"`

	// muster ≥ 0.7
	Action       string          `json:"action"` // rollback, restart (start/summary)
	Method       string          `json:"method"` // restart: hook, k8s, compose, docker, redeploy
	After        string          `json:"after"`  // stopped
	Remaining    int             `json:"remaining"`
	NotRun       int             `json:"not_run"`
	Restarted    int             `json:"restarted"`
	StoppedEarly bool            `json:"stopped_early"`
	Source       string          `json:"source"`  // logs
	Pattern      string          `json:"pattern"` // diagnosis
	PodStatus    string          `json:"pod_status"`
	HintRaw      json.RawMessage `json:"hint"` // object on "hint" events, text on "diagnosis"

	// summary event (muster ≥ 0.5.61; "succeeded" is an older spelling)
	Deployed   int              `json:"deployed"`
	Succeeded  int              `json:"succeeded"`
	Failed     int              `json:"failed"`
	Skipped    int              `json:"skipped"`
	RolledBack int              `json:"rolled_back"`
	Services   []SummaryService `json:"services"`

	Raw json.RawMessage `json:"-"`
}

// FileRef is a file named in an event: a plain path (older CLIs) or
// {"key":"api/deploy.sh","state":"changed"} (muster ≥ 0.7).
type FileRef struct {
	Key   string `json:"key"`
	State string `json:"state"`
}

// UnmarshalJSON accepts both shapes.
func (f *FileRef) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = FileRef{Key: s}
		return nil
	}
	type plain FileRef
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*f = FileRef(p)
	return nil
}

// Hint returns the "Likely cause / Try" of a hint event (or the text hint
// of a diagnosis event), nil when there is none.
func (ev DeployEvent) Hint() *Hint {
	if len(ev.HintRaw) == 0 || string(ev.HintRaw) == "null" {
		return nil
	}
	var h Hint
	if json.Unmarshal(ev.HintRaw, &h) == nil && (h.Title != "" || h.Try != "") {
		return &h
	}
	var s string
	if json.Unmarshal(ev.HintRaw, &s) == nil && s != "" {
		title, _ := ev.Field("summary").(string)
		return &Hint{ID: ev.Pattern, Title: title, Try: s}
	}
	return nil
}

// SummaryService is one row of the summary event.
type SummaryService struct {
	Service  string  `json:"service"`
	Result   string  `json:"result"` // deployed, failed, skipped, rolled back
	Duration float64 `json:"duration"`
	Health   string  `json:"health"`
	LogFile  string  `json:"log_file"`
}

// UnmarshalJSON also accepts a bare service name (error events list the
// affected services as strings).
func (s *SummaryService) UnmarshalJSON(b []byte) error {
	var name string
	if json.Unmarshal(b, &name) == nil {
		*s = SummaryService{Service: name}
		return nil
	}
	type plain SummaryService
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*s = SummaryService(p)
	return nil
}

// OK counts successful services in a summary (either spelling).
func (ev DeployEvent) OK() int { return max(ev.Deployed, ev.Succeeded) }

// Field returns an arbitrary field from the raw event.
func (ev DeployEvent) Field(name string) any {
	var m map[string]any
	if json.Unmarshal(ev.Raw, &m) != nil {
		return nil
	}
	return m[name]
}

// DeployOptions controls a deploy.
type DeployOptions struct {
	DryRun bool
	Force  bool
	// muster ≥ 0.7: end after the first failed service (the rest are
	// reported not_run), and start at From in deploy order.
	StopOnFailure bool
	From          string
}

// DeployStream is a running deploy.
type DeployStream struct {
	C    <-chan DeployEvent
	wait func() error
}

// Wait returns the process result after C is drained.
func (d *DeployStream) Wait() error { return d.wait() }

// Deploy runs `muster deploy --json [--dry-run] [--force] [services…]`.
func (e *Engine) Deploy(ctx context.Context, services []string, opt DeployOptions) (*DeployStream, error) {
	args := []string{"deploy", "--json"}
	if opt.DryRun {
		args = append(args, "--dry-run")
	}
	if opt.Force {
		args = append(args, "--force")
	}
	if opt.StopOnFailure {
		args = append(args, "--stop-on-failure")
	}
	if opt.From != "" {
		args = append(args, "--from", opt.From)
	}
	args = append(args, services...)
	return e.Stream(ctx, args...)
}

// Stream runs a muster command that prints deploy-style NDJSON events
// (deploy, rollback --json, restart --json, logs --json).
func (e *Engine) Stream(ctx context.Context, args ...string) (*DeployStream, error) {
	authGate.Lock()
	ls, err := e.Raw(ctx, args...)
	if err != nil {
		authGate.Unlock()
		return nil, err
	}
	var once sync.Once
	release := func() { once.Do(authGate.Unlock) }
	timer := time.AfterFunc(gateHold, release)
	ch := make(chan DeployEvent, 64)
	go func() {
		defer close(ch)
		defer release()
		for l := range ls.C {
			if !l.Stderr {
				timer.Stop()
				release()
			}
			ch <- ParseDeployLine(l.Text, l.Stderr)
		}
	}()
	return &DeployStream{C: ch, wait: func() error {
		if err := ls.Wait(); err != nil {
			return classify(err)
		}
		return nil
	}}, nil
}

// ParseDeployLine decodes one line of deploy output.
func ParseDeployLine(text string, stderr bool) DeployEvent {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "{") {
		var ev DeployEvent
		if json.Unmarshal([]byte(t), &ev) == nil {
			ev.Raw = json.RawMessage(t)
			if ev.Event == "" && ev.Line == "" {
				var ce CLIError
				if json.Unmarshal([]byte(t), &ce) == nil && ce.Code != "" {
					return DeployEvent{Event: "error", Status: ce.Code, Message: ce.Message, Raw: ev.Raw}
				}
			}
			return ev
		}
		// A field with an unexpected type must not lose the whole event:
		// keep the basics (new CLI versions may change a shape).
		var basic struct {
			Event, Service, Name, Status, Line, Message, Reason string
		}
		if json.Unmarshal([]byte(t), &basic) == nil && basic.Event != "" {
			return DeployEvent{Event: basic.Event, Service: basic.Service, Name: basic.Name, Status: basic.Status,
				Line: basic.Line, Message: basic.Message, Reason: basic.Reason, Raw: json.RawMessage(t)}
		}
	}
	kind := "output"
	if stderr {
		kind = "stderr"
	}
	return DeployEvent{Event: kind, Line: text}
}

// Rollback runs `muster rollback <svc>` and streams its output.
func (e *Engine) Rollback(ctx context.Context, svc string) (<-chan string, func() error, error) {
	return e.lines(ctx, "rollback", svc)
}

// Logs streams `muster logs <svc>`.
func (e *Engine) Logs(ctx context.Context, svc string) (<-chan string, func() error, error) {
	return e.lines(ctx, "logs", svc)
}

func (e *Engine) lines(ctx context.Context, args ...string) (<-chan string, func() error, error) {
	ls, err := e.Raw(ctx, args...)
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan string, 64)
	go func() {
		defer close(ch)
		for l := range ls.C {
			ch <- l.Text
		}
	}()
	return ch, func() error { return classify(ls.Wait()) }, nil
}
