package proc

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLinesAndExitError(t *testing.T) {
	cmd := Command(context.Background(), "sh", "-c", `printf 'one\n\033[31mtwo\033[0m\n'; echo oops >&2; exit 3`)
	s, err := Lines(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var out, errs []string
	for l := range s.C {
		if l.Stderr {
			errs = append(errs, l.Text)
		} else {
			out = append(out, l.Text)
		}
	}
	if len(out) != 2 || out[1] != "two" || len(errs) != 1 {
		t.Fatalf("out=%q errs=%q", out, errs)
	}
	var ee *ExitError
	if err := s.Wait(); !errors.As(err, &ee) || ee.Code != 3 || ee.Stderr != "oops\n" {
		t.Fatalf("err = %#v", err)
	}
}

func TestCancelKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := Command(ctx, "sh", "-c", `sleep 30 & sleep 30; echo never`)
	s, err := Lines(cmd)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	time.AfterFunc(100*time.Millisecond, cancel)
	for range s.C {
	}
	_ = s.Wait()
	if time.Since(start) > 5*time.Second {
		t.Fatalf("cancel took %s", time.Since(start))
	}
}

func TestStripANSI(t *testing.T) {
	if got := StripANSI("\x1b[1;32mok\x1b[0m"); got != "ok" {
		t.Fatal(got)
	}
	if got := StripANSI("50%\r100%"); got != "100%" {
		t.Fatal(got)
	}
}
