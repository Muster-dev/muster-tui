package engine

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// ServiceStatus is one entry of `muster status --json`.
type ServiceStatus struct {
	Key        string `json:"-"`
	Name       string `json:"name"`
	Status     string `json:"status"` // healthy, unhealthy, disabled, no_health_check
	HealthType string `json:"health_type"`
	Detail     string `json:"detail"`
}

// StatusResult is `muster status --json`.
type StatusResult struct {
	Project  string                   `json:"project"`
	Services map[string]ServiceStatus `json:"services"`
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
	Event     string   `json:"event"`
	Service   string   `json:"service"`
	Name      string   `json:"name"`
	Status    string   `json:"status"`
	Line      string   `json:"line"`
	Message   string   `json:"message"`
	Reason    string   `json:"reason"`
	LogFile   string   `json:"log_file"`
	Hook      string   `json:"hook"`
	Remote    string   `json:"remote"`
	Branch    string   `json:"branch"`
	Index     int      `json:"index"`
	Total     int      `json:"total"`
	ExitCode  int      `json:"exit_code"`
	HookLines int      `json:"hook_lines"`
	Timeout   int      `json:"timeout"`
	DryRun    bool     `json:"dry_run"`
	Duration  float64  `json:"duration"`
	Succeeded int      `json:"succeeded"`
	Failed    int      `json:"failed"`
	Skipped   int      `json:"skipped"`
	Files     []string `json:"files"`

	Raw json.RawMessage `json:"-"`
}

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
	args = append(args, services...)
	ls, err := e.Raw(ctx, args...)
	if err != nil {
		return nil, err
	}
	ch := make(chan DeployEvent, 64)
	go func() {
		defer close(ch)
		for l := range ls.C {
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
