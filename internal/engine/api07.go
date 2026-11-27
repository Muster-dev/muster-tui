package engine

// Engine API commands added in muster 0.7 (docs/ENGINE-API.md in the muster
// repo). Callers check Caps.EngineAPI before using them.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/Muster-dev/muster-tui/internal/proc"
)

// HookFile is one file in `muster hooks status --json`.
type HookFile struct {
	Service      string   `json:"service"`
	File         string   `json:"file"`
	Key          string   `json:"key"`   // "api/deploy.sh", "_config/muster.json"
	Path         string   `json:"path"`  // relative to the project
	State        string   `json:"state"` // approved, changed, new, missing
	ApprovedAt   string   `json:"approved_at"`
	ApprovedCopy string   `json:"approved_copy"`
	Dangerous    []string `json:"dangerous"`
}

// Pending reports whether a deploy would stop on this file.
func (f HookFile) Pending() bool { return f.State == "changed" || f.State == "new" }

// HooksStatus is `muster hooks status --json`.
type HooksStatus struct {
	Approved        bool       `json:"approved"`
	SecurityEnabled bool       `json:"security_enabled"`
	Signature       string     `json:"signature"` // valid, invalid, unsigned, none
	Files           []HookFile `json:"files"`
	Config          *HookFile  `json:"config"`
	Env             *HookFile  `json:"env"`
	Pending         []string   `json:"pending"`
}

// All returns hooks, then muster.json and .env when present.
func (h HooksStatus) All() []HookFile {
	out := append([]HookFile(nil), h.Files...)
	if h.Config != nil {
		out = append(out, *h.Config)
	}
	if h.Env != nil {
		out = append(out, *h.Env)
	}
	return out
}

// HooksStatus runs `muster hooks status --json`.
func (e *Engine) HooksStatus(ctx context.Context) (HooksStatus, error) {
	var r HooksStatus
	err := e.runJSON(ctx, &r, false, "hooks", "status", "--json")
	return r, err
}

// ApproveResult is `muster hooks approve --yes --json`.
type ApproveResult struct {
	OK       bool `json:"ok"`
	Count    int  `json:"count"`
	Approved []struct {
		Key string `json:"key"`
		Was string `json:"was"`
	} `json:"approved"`
	Services []string `json:"services"`
}

// HooksApprove approves pending hooks without a prompt (`--yes` is the
// confirmation; callers must have asked the user). With services, only
// theirs (and a changed muster.json); without, everything.
func (e *Engine) HooksApprove(ctx context.Context, services ...string) (ApproveResult, error) {
	args := append([]string{"hooks", "approve", "--yes"}, services...)
	var r ApproveResult
	err := e.runJSON(ctx, &r, false, append(args, "--json")...)
	return r, err
}

// DangerousFile is one entry of a dangerous_hooks error.
type DangerousFile struct {
	Key   string `json:"key"`
	Issue string `json:"issue"`
}

// DangerousFiles returns the files a dangerous_hooks error names.
func DangerousFiles(err error) []DangerousFile {
	var ce *CLIError
	if !errors.As(err, &ce) || len(ce.Files) == 0 {
		return nil
	}
	var out []DangerousFile
	_ = json.Unmarshal(ce.Files, &out)
	return out
}

// Hint is one offline failure explanation (lib/core/hints.sh).
type Hint struct {
	ID    string   `json:"id"`
	Title string   `json:"title"` // "Likely cause"
	Try   string   `json:"try"`
	Fix   *HintFix `json:"fix"`
	Docs  string   `json:"docs"`
	Line  string   `json:"line"` // the log line that matched
}

// HintFix is a fix `muster explain --fix` can apply.
type HintFix struct {
	Label    string `json:"label"`
	Command  string `json:"command"`
	Internal bool   `json:"internal"` // edits muster.json rather than running a command
}

// Explanation is `muster explain [service] --json`.
type Explanation struct {
	Service  string `json:"service"`
	Action   string `json:"action"`
	ExitCode *int   `json:"exit_code"`
	Log      string `json:"log"`
	Matched  bool   `json:"matched"`
	Reason   string `json:"reason"` // no_logs
	Hint     *Hint  `json:"hint"`
	Hints    []Hint `json:"hints"`
}

// Explain runs `muster explain [service] --json` (offline, no network).
func (e *Engine) Explain(ctx context.Context, service string) (Explanation, error) {
	args := []string{"explain"}
	if service != "" {
		args = append(args, service)
	}
	var r Explanation
	err := e.runJSON(ctx, &r, false, append(args, "--json")...)
	return r, err
}

// ExplainFix runs `muster explain [service] --fix --yes` and streams its
// output. Only call it after the user confirmed the fix.
func (e *Engine) ExplainFix(ctx context.Context, service string) (<-chan string, func() error, error) {
	args := []string{"explain"}
	if service != "" {
		args = append(args, service)
	}
	return e.lines(ctx, append(args, "--fix", "--yes")...)
}

// RollbackStream runs `muster rollback <svc> --json` (deploy-style events).
func (e *Engine) RollbackStream(ctx context.Context, svc string) (*DeployStream, error) {
	return e.Stream(ctx, "rollback", svc, "--json")
}

// Restart runs `muster restart <svc> --json`. When the stack has no
// restart, the first event is {"event":"restart","method":"redeploy"} and a
// deploy stream follows.
func (e *Engine) Restart(ctx context.Context, svc string) (*DeployStream, error) {
	return e.Stream(ctx, "restart", svc, "--json")
}

// LogsStream runs `muster logs <svc> --json --tail N [--follow]`.
func (e *Engine) LogsStream(ctx context.Context, svc string, tail int, follow bool) (*DeployStream, error) {
	args := []string{"logs", svc, "--json", "--tail", strconv.Itoa(tail)}
	if follow {
		args = append(args, "--follow")
	}
	return e.Stream(ctx, args...)
}

// AskEvent is one NDJSON line from `muster ask --json`. Lines that aren't
// JSON arrive as Type "output" (stdout) or "stderr" with the text in Message.
type AskEvent struct {
	Type       string          `json:"type"`
	Delta      string          `json:"delta"`
	SessionID  string          `json:"session_id"`
	Plan       string          `json:"plan"`
	Remaining  json.RawMessage `json:"remaining_sessions"`
	ToolUseID  string          `json:"tool_use_id"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	Summary    string          `json:"summary"`
	Input      json.RawMessage `json:"input"`
	Diff       string          `json:"diff"`
	IsError    bool            `json:"is_error"`
	Content    string          `json:"content"`
	ErrRaw     json.RawMessage `json:"error"`
	Message    string          `json:"message"`
	UpgradeURL string          `json:"upgrade_url"`
	StopReason string          `json:"stop_reason"`
	OK         *bool           `json:"ok"`
	Raw        json.RawMessage `json:"-"`
}

// Err is the error code of an error event ("unauthorized",
// "payment_required", …), whether the server sent a string or an object.
func (ev AskEvent) Err() string {
	if len(ev.ErrRaw) == 0 || string(ev.ErrRaw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(ev.ErrRaw, &s) == nil {
		return s
	}
	var obj struct {
		Code    string `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(ev.ErrRaw, &obj) == nil {
		return firstOf(obj.Code, obj.Type, obj.Message)
	}
	return strings.Trim(string(ev.ErrRaw), `"`)
}

// RemainingSessions renders remaining_sessions ("" when absent/null).
func (ev AskEvent) RemainingSessions() string {
	s := strings.Trim(string(ev.Remaining), `" `)
	if s == "null" {
		return ""
	}
	return s
}

func firstOf(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// ParseAskLine decodes one line of `muster ask --json` output.
func ParseAskLine(text string, stderr bool) AskEvent {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "{") {
		var ev AskEvent
		if json.Unmarshal([]byte(t), &ev) == nil && ev.Type != "" {
			ev.Raw = json.RawMessage(t)
			return ev
		}
		var ce CLIError // {"error":"auth_required",…} on stderr
		if json.Unmarshal([]byte(t), &ce) == nil && ce.Code != "" {
			b, _ := json.Marshal(ce.Code)
			return AskEvent{Type: "error", ErrRaw: b, Message: ce.Message, Raw: json.RawMessage(t)}
		}
	}
	kind := "output"
	if stderr {
		kind = "stderr"
	}
	return AskEvent{Type: kind, Message: text}
}

// AskOptions starts a Muster Assistant session.
type AskOptions struct {
	Question string
	Consent  bool     // pass --yes (the user agreed to send redacted data)
	ReadOnly bool     // --read-only: every action tool is declined
	Env      []string // e.g. MUSTER_ASK_FAILURE_SERVICE=api
}

// AskSession is a running `muster ask --json`. Answers go to its stdin.
type AskSession struct {
	C     <-chan AskEvent
	mu    sync.Mutex
	stdin io.WriteCloser
	wait  func() error
}

// Ask starts `muster ask --json [--yes] [--read-only] -- <question>`.
func (e *Engine) Ask(ctx context.Context, opt AskOptions) (*AskSession, error) {
	args := []string{"ask", "--json"}
	if opt.Consent {
		args = append(args, "--yes")
	}
	if opt.ReadOnly {
		args = append(args, "--read-only")
	}
	if opt.Question != "" {
		args = append(args, "--", opt.Question)
	}
	cmd := e.Command(ctx, args...)
	cmd.Env = append(cmd.Env, opt.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	ls, err := proc.Lines(cmd)
	if err != nil {
		return nil, err
	}
	ch := make(chan AskEvent, 64)
	go func() {
		defer close(ch)
		for l := range ls.C {
			ch <- ParseAskLine(l.Text, l.Stderr)
		}
	}()
	return &AskSession{C: ch, stdin: stdin, wait: ls.Wait}, nil
}

func (a *AskSession) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stdin == nil {
		return errors.New("the assistant session has ended")
	}
	_, err = a.stdin.Write(append(b, '\n'))
	return err
}

// Approve answers an approval_needed event with yes.
func (a *AskSession) Approve(id string) error { return a.send(map[string]string{"approve": id}) }

// Decline answers an approval_needed event with no.
func (a *AskSession) Decline(id string) error { return a.send(map[string]string{"decline": id}) }

// Message sends a follow-up question (after awaiting_input).
func (a *AskSession) Message(text string) error { return a.send(map[string]string{"message": text}) }

// End finishes the session ({"end":true}, then EOF).
func (a *AskSession) End() error {
	err := a.send(map[string]bool{"end": true})
	a.Close()
	return err
}

// Close closes stdin (EOF declines pending approvals and ends the session).
func (a *AskSession) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stdin != nil {
		_ = a.stdin.Close()
		a.stdin = nil
	}
}

// Wait returns the process result after C is drained.
func (a *AskSession) Wait() error {
	if err := a.wait(); err != nil {
		return classify(err)
	}
	return nil
}
