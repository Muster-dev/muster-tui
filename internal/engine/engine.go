// Package engine wraps the muster bash CLI. muster-tui never deploys anything
// itself: it runs `muster <cmd> --json` (or raw commands where no JSON exists)
// and turns the output into typed values and event streams.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Muster-dev/muster-tui/internal/proc"
)

// authGate serializes the start of every token-checked CLI call. The CLI
// rewrites ~/.muster/tokens/auth.json (last_used) through a shared .tmp file
// on each --json call; concurrent calls can truncate it to 0 bytes and wipe
// every token (docs/ENGINE-GAPS.md #0). One at a time is safe.
var authGate sync.Mutex

// gateHold is how long a streaming call keeps the gate if it prints nothing
// (the CLI's auth check happens before its first output line).
var gateHold = 5 * time.Second

// Engine runs the muster CLI for one project directory.
type Engine struct {
	Bin   string   // path to the muster executable
	Token string   // MUSTER_TOKEN for --json commands
	Dir   string   // working directory (project root); "" = current
	Env   []string // extra KEY=VALUE pairs
}

// Locate finds the muster executable: $MUSTER_BIN, then PATH.
func Locate() (string, error) {
	if p := os.Getenv("MUSTER_BIN"); p != "" {
		return p, nil
	}
	p, err := exec.LookPath("muster")
	if err != nil {
		return "", errors.New("the muster CLI is not on your PATH — install it from https://getmuster.dev")
	}
	return p, nil
}

// New returns an engine for bin.
func New(bin, token, dir string) *Engine { return &Engine{Bin: bin, Token: token, Dir: dir} }

// WithDir returns a copy of the engine that runs in dir.
func (e *Engine) WithDir(dir string) *Engine {
	c := *e
	c.Dir = dir
	return &c
}

// Command builds a cancellable muster command (not attached to the terminal).
func (e *Engine) Command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := proc.Command(ctx, e.Bin, args...)
	cmd.Dir = e.Dir
	cmd.Env = append(os.Environ(), e.env()...)
	return cmd
}

// Interactive builds a muster command for tea.ExecProcess (inherits the TTY).
func (e *Engine) Interactive(args ...string) *exec.Cmd {
	cmd := exec.Command(e.Bin, args...)
	cmd.Dir = e.Dir
	cmd.Env = append(os.Environ(), e.env()...)
	if e.Token != "" {
		cmd.Env = append(cmd.Env, "MUSTER_TOKEN="+e.Token)
	}
	return cmd
}

func (e *Engine) env() []string {
	env := []string{"NO_COLOR=1", "MUSTER_COLOR=never", "TERM=dumb", "MUSTER_TUI=1"}
	if e.Token != "" {
		env = append(env, "MUSTER_TOKEN="+e.Token)
	}
	return append(env, e.Env...)
}

// CLIError is a structured error the CLI printed as JSON on stderr, e.g.
// {"error":"auth_required","message":"MUSTER_TOKEN not set"}.
type CLIError struct {
	Code     string          `json:"error"`
	Message  string          `json:"message"`
	Files    json.RawMessage `json:"files"` // dangerous_hooks
	ExitCode int             `json:"-"`
}

func (e *CLIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

// IsAuth reports whether err means the CLI rejected or lacked our token.
func IsAuth(err error) bool {
	var ce *CLIError
	if errors.As(err, &ce) {
		return strings.HasPrefix(ce.Code, "auth") || ce.Code == "forbidden"
	}
	return false
}

// classify turns a process error into a CLIError when stderr carries one.
func classify(err error) error {
	var ee *proc.ExitError
	if !errors.As(err, &ee) {
		return err
	}
	for _, line := range strings.Split(ee.Stderr, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ce CLIError
		if json.Unmarshal([]byte(line), &ce) == nil && ce.Code != "" {
			ce.ExitCode = ee.Code
			return &ce
		}
	}
	if strings.Contains(ee.Stderr, "No muster.json") || strings.Contains(ee.Stderr, "no config") {
		return &CLIError{Code: "no_project", Message: "No muster.json here — run setup first", ExitCode: ee.Code}
	}
	return err
}

// runJSON runs muster and decodes stdout into v. When tolerant is set, a
// non-zero exit with decodable stdout is not an error (doctor exits non-zero
// when checks fail).
func (e *Engine) runJSON(ctx context.Context, v any, tolerant bool, args ...string) error {
	authGate.Lock()
	out, err := proc.Output(e.Command(ctx, args...))
	authGate.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && !(tolerant && len(strings.TrimSpace(string(out))) > 0) {
		return classify(err)
	}
	body := extractJSON(out)
	if len(body) == 0 {
		return fmt.Errorf("muster %s returned no JSON", args[0])
	}
	if jerr := json.Unmarshal(body, v); jerr != nil {
		return fmt.Errorf("muster %s returned unexpected output: %w", args[0], jerr)
	}
	return nil
}

// extractJSON skips any non-JSON preamble (warnings printed to stdout).
func extractJSON(out []byte) []byte {
	s := string(out)
	for i, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			lines := strings.Split(s, "\n")[i:]
			return []byte(strings.Join(lines, "\n"))
		}
	}
	return nil
}

// Version returns `muster --version`.
func (e *Engine) Version(ctx context.Context) (string, error) {
	out, err := proc.Output(e.Command(ctx, "--version"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

var tokenRe = regexp.MustCompile(`(?m)Token:\s+([A-Za-z0-9_\-]{16,})`)

// CreateToken runs `muster auth create <name> --scope admin` and returns the
// raw token. The CLI only allows this without an admin token when no tokens
// exist yet (first-run bootstrap).
func (e *Engine) CreateToken(ctx context.Context, name string) (string, error) {
	c := *e
	c.Token = ""
	authGate.Lock()
	out, err := proc.Output(c.Command(ctx, "auth", "create", name, "--scope", "admin"))
	authGate.Unlock()
	if err != nil {
		return "", classify(err)
	}
	m := tokenRe.FindSubmatch([]byte(proc.StripANSI(string(out))))
	if m == nil {
		return "", errors.New("could not read the token from `muster auth create`")
	}
	return string(m[1]), nil
}

// Raw runs a command without JSON support and streams its output lines.
func (e *Engine) Raw(ctx context.Context, args ...string) (*proc.LineStream, error) {
	return proc.Lines(e.Command(ctx, args...))
}

// RunJSON runs an arbitrary JSON command and decodes into v (used by fleet).
func (e *Engine) RunJSON(ctx context.Context, v any, args ...string) error {
	return e.runJSON(ctx, v, false, args...)
}
