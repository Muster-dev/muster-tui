// Package proc runs child processes for muster-tui: cancellable via context,
// killed as a whole process group, never attached to the terminal, with
// line-oriented output streams.
package proc

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Command builds a cancellable command. Cancelling ctx sends SIGTERM to the
// child's whole process group (so `docker compose` grandchildren die too) and
// SIGKILL after a grace period.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	setGroup(cmd)
	cmd.WaitDelay = 3 * time.Second
	return cmd
}

// ExitError describes a failed command.
type ExitError struct {
	Name   string
	Args   []string
	Code   int
	Stderr string
	Err    error
}

func (e *ExitError) Error() string {
	msg := FirstLine(e.Stderr)
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	cmd := e.Name
	if len(e.Args) > 0 {
		cmd += " " + e.Args[0]
	}
	if e.Code > 0 {
		return fmt.Sprintf("%s failed (exit %d): %s", cmd, e.Code, msg)
	}
	return fmt.Sprintf("%s failed: %s", cmd, msg)
}

func (e *ExitError) Unwrap() error { return e.Err }

// Output runs the command and returns stdout. A non-zero exit returns stdout
// together with an *ExitError carrying stderr.
func Output(cmd *exec.Cmd) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.Bytes(), wrapErr(cmd, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func wrapErr(cmd *exec.Cmd, err error, stderr string) error {
	if errors.Is(err, context.Canceled) || cmd.ProcessState == nil && errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	ee := &ExitError{Name: baseName(cmd.Path), Stderr: StripANSI(stderr), Err: err, Code: -1}
	if len(cmd.Args) > 1 {
		ee.Args = cmd.Args[1:]
	}
	var xe *exec.ExitError
	if errors.As(err, &xe) {
		ee.Code = xe.ExitCode()
	}
	return ee
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Line is one line of process output.
type Line struct {
	Text   string
	Stderr bool
}

// LineStream is a running process emitting lines.
type LineStream struct {
	C      <-chan Line
	done   chan struct{}
	err    error
	stderr bytes.Buffer
	mu     sync.Mutex
}

// Wait blocks until the process has exited and all output was delivered.
func (s *LineStream) Wait() error {
	<-s.done
	return s.err
}

// Stderr returns everything written to stderr so far.
func (s *LineStream) Stderr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stderr.String()
}

// Lines starts cmd and streams stdout and stderr lines (ANSI stripped). The
// channel closes after the process exits; Wait then returns its error.
func Lines(cmd *exec.Cmd) (*LineStream, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	ch := make(chan Line, 64)
	s := &LineStream{C: ch, done: make(chan struct{})}
	var wg sync.WaitGroup
	pump := func(r io.Reader, isErr bool) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			text := StripANSI(strings.TrimRight(sc.Text(), "\r"))
			if isErr {
				s.mu.Lock()
				s.stderr.WriteString(text + "\n")
				s.mu.Unlock()
			}
			ch <- Line{Text: text, Stderr: isErr}
		}
	}
	wg.Add(2)
	go pump(stdout, false)
	go pump(stderr, true)
	go func() {
		wg.Wait()
		err := cmd.Wait()
		if err != nil {
			s.err = wrapErr(cmd, err, s.Stderr())
		}
		close(ch)
		close(s.done)
	}()
	return s, nil
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][A-Za-z0-9]|\x1b[=>78]`)

// StripANSI removes terminal escape sequences and stray carriage-return
// redraws (keeping the last segment).
func StripANSI(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	if i := strings.LastIndex(s, "\r"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// FirstLine returns the first non-empty trimmed line.
func FirstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}
