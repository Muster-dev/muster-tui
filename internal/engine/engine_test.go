package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fake(t *testing.T, scenario string) (*Engine, string) {
	t.Helper()
	bin, err := filepath.Abs("testdata/fake-muster")
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "argv.log")
	e := New(bin, "tok", t.TempDir())
	e.Env = []string{"FAKE_SCENARIO=" + scenario, "FAKE_LOG=" + log}
	return e, log
}

func collect(t *testing.T, s *DeployStream) []DeployEvent {
	t.Helper()
	var evs []DeployEvent
	for ev := range s.C {
		evs = append(evs, ev)
	}
	return evs
}

func TestStatus(t *testing.T) {
	e, _ := fake(t, "")
	r, err := e.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Project != "acme-store" || len(r.Services) != 4 || r.Services["api"].Status != "healthy" || r.Services["api"].Key != "api" {
		t.Fatalf("%+v", r)
	}
}

func TestAuthErrorIsClassified(t *testing.T) {
	e, _ := fake(t, "")
	e.Token = ""
	_, err := e.Status(context.Background())
	if !IsAuth(err) {
		t.Fatalf("want auth error, got %v", err)
	}
	if !strings.Contains(err.Error(), "MUSTER_TOKEN") {
		t.Fatalf("message: %v", err)
	}
}

func TestHistorySortedAndTolerant(t *testing.T) {
	e, _ := fake(t, "")
	evs, err := e.History(context.Background(), true, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Service != "api" || !evs[0].OK() || evs[1].OK() {
		t.Fatalf("%+v", evs)
	}
}

func TestDoctorNonZeroExitStillParses(t *testing.T) {
	e, _ := fake(t, "")
	r, err := e.Doctor(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Failures != 1 || len(r.Checks) != 4 || r.Checks[3].Status != "fail" {
		t.Fatalf("%+v", r)
	}
}

func TestDeploySuccessWithSummaryAndUnknownFields(t *testing.T) {
	e, log := fake(t, "success")
	s, err := e.Deploy(context.Background(), []string{"api"}, DeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, s)
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	var summary DeployEvent
	for _, ev := range evs {
		kinds[ev.Event]++
		if ev.Event == "summary" {
			summary = ev
		}
	}
	for _, k := range []string{"start", "git_pull", "log", "done", "health", "verify_warning", "complete", "summary", "stderr"} {
		if kinds[k] == 0 {
			t.Errorf("missing %q event (got %v)", k, kinds)
		}
	}
	if summary.OK() != 1 || summary.Duration != 4 || len(summary.Services) != 1 || summary.Services[0].Health != "healthy" || summary.Field("new_field") == nil {
		t.Fatalf("summary = %+v", summary)
	}
	argv, _ := os.ReadFile(log)
	if strings.TrimSpace(string(argv)) != "deploy --json api" {
		t.Fatalf("argv = %q", argv)
	}
}

func TestDeployFailure(t *testing.T) {
	e, _ := fake(t, "failure")
	s, err := e.Deploy(context.Background(), []string{"redis"}, DeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var done DeployEvent
	for _, ev := range collect(t, s) {
		if ev.Event == "done" {
			done = ev
		}
	}
	if done.Status != "failed" || done.ExitCode != 125 {
		t.Fatalf("done = %+v", done)
	}
	if err := s.Wait(); err == nil {
		t.Fatal("expected non-zero exit to surface as an error")
	}
}

func TestDeployHooksNotApproved(t *testing.T) {
	e, _ := fake(t, "unapproved")
	s, _ := e.Deploy(context.Background(), []string{"api"}, DeployOptions{})
	var gate DeployEvent
	var stderr []string
	for _, ev := range collect(t, s) {
		switch ev.Event {
		case "error":
			gate = ev
		case "stderr":
			stderr = append(stderr, ev.Line)
		}
	}
	if gate.Reason != "hooks_not_approved" || len(stderr) != 1 {
		t.Fatalf("gate=%+v stderr=%q", gate, stderr)
	}
	if s.Wait() == nil {
		t.Fatal("expected exit 1")
	}
}

func TestDeployDryRunAndLocked(t *testing.T) {
	e, _ := fake(t, "locked")
	s, _ := e.Deploy(context.Background(), nil, DeployOptions{DryRun: true})
	evs := collect(t, s)
	if len(evs) != 3 || evs[0].Event != "dry_run" || evs[1].HookLines != 12 || !evs[2].DryRun {
		t.Fatalf("%+v", evs)
	}
	s, _ = e.Deploy(context.Background(), []string{"api"}, DeployOptions{Force: false})
	evs = collect(t, s)
	if evs[1].Event != "skipped" || evs[1].Reason != "locked" {
		t.Fatalf("%+v", evs)
	}
}

func TestDeployCancel(t *testing.T) {
	e, _ := fake(t, "slow")
	ctx, cancel := context.WithCancel(context.Background())
	s, err := e.Deploy(ctx, []string{"api"}, DeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	<-s.C // start
	<-s.C // log
	cancel()
	for range s.C {
	}
	_ = s.Wait()
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancel did not stop the deploy")
	}
}

func TestCreateToken(t *testing.T) {
	e, _ := fake(t, "")
	tok, err := e.CreateToken(context.Background(), "muster-tui")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("token = %q", tok)
	}
}

func TestParseDeployLine(t *testing.T) {
	if ev := ParseDeployLine(`{"error":"forbidden","message":"nope"}`, true); ev.Event != "error" || ev.Message != "nope" {
		t.Fatalf("%+v", ev)
	}
	if ev := ParseDeployLine("plain text", false); ev.Event != "output" || ev.Line != "plain text" {
		t.Fatalf("%+v", ev)
	}
}

func TestLogsAndRollbackStreams(t *testing.T) {
	e, _ := fake(t, "")
	ch, wait, err := e.Logs(context.Background(), "api")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for l := range ch {
		lines = append(lines, l)
	}
	if len(lines) != 2 || wait() != nil {
		t.Fatalf("%q", lines)
	}
}
