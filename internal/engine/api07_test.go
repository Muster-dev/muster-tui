package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fake07(t *testing.T, scenario string, env ...string) (*Engine, string) {
	t.Helper()
	bin, err := filepath.Abs("testdata/fake-muster-07")
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "argv.log")
	e := New(bin, "tok", t.TempDir())
	e.Env = append([]string{"FAKE_SCENARIO=" + scenario, "FAKE_LOG=" + log}, env...)
	return e, log
}

func TestParseVersionAndCaps(t *testing.T) {
	for in, want := range map[string]bool{"v0.7.0": true, "muster 0.7.12": true, "0.10.0-dev": true, "1.0.0": true, "muster 0.5.60": false, "v0.6.2": false} {
		if c := CapsFor(in); !c.Known || c.EngineAPI != want {
			t.Errorf("%q: %+v", in, c)
		}
	}
	if c := CapsFor("dev build"); c.Known || c.EngineAPI {
		t.Errorf("%+v", c)
	}
	e, _ := fake07(t, "")
	v, err := e.Version(context.Background())
	if err != nil || !CapsFor(v).EngineAPI || CapsFor(v).Version.String() != "0.7.0" {
		t.Fatalf("%q %v", v, err)
	}
}

func TestStatusRichFields(t *testing.T) {
	e, _ := fake07(t, "")
	r, err := e.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !r.Deploying || r.DeployLock == nil || r.DeployLock.User != "sam" || r.DeployLock.PID != 4242 || r.DeployLock.Services != "api web" {
		t.Fatalf("lock: %+v", r.DeployLock)
	}
	api := r.Services["api"]
	if api.Stack != "k8s" || api.Target != "http://localhost:8080/health" || api.LastDeploy == nil || api.LastDeploy.Commit != "4f2a9c1" ||
		api.LockedBy.Who() != "sam@ci-runner-3" || !api.Deploying || api.Replicas.Ready != 2 || api.Replicas.Desired != 3 {
		t.Fatalf("api: %+v", api)
	}
	if web := r.Services["web"]; web.Remote != "deploy@10.0.0.5" || web.LockedBy != nil || web.Replicas != nil || web.LastDeploy.Status != "failed" {
		t.Fatalf("web: %+v", web)
	}
	if redis := r.Services["redis"]; redis.Target != "" || redis.LastDeploy != nil {
		t.Fatalf("redis: %+v", redis)
	}
}

func TestHooksStatusAndApprove(t *testing.T) {
	state := filepath.Join(t.TempDir(), "approved")
	e, log := fake07(t, "", "FAKE_STATE="+state)
	hs, err := e.HooksStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var pending []string
	for _, f := range hs.All() {
		if f.Pending() {
			pending = append(pending, f.Key+":"+f.State)
		}
	}
	if got := strings.Join(pending, ","); got != "api/deploy.sh:changed,web/deploy.sh:new,_config/muster.json:changed" {
		t.Fatalf("pending = %s", got)
	}
	if hs.Files[0].ApprovedCopy == "" || hs.Files[2].ApprovedCopy != "" || len(hs.Files[2].Dangerous) != 1 || hs.Files[3].Pending() {
		t.Fatalf("%+v", hs.Files)
	}
	r, err := e.HooksApprove(context.Background(), "api")
	if err != nil || !r.OK || r.Count != 2 || r.Approved[0].Was != "changed" {
		t.Fatalf("%+v %v", r, err)
	}
	argv, _ := os.ReadFile(log)
	if !strings.Contains(string(argv), "hooks approve --yes api --json") {
		t.Fatalf("argv: %s", argv)
	}
	if hs, _ := e.HooksStatus(context.Background()); len(hs.Pending) != 0 {
		t.Fatalf("still pending: %v", hs.Pending)
	}

	e, _ = fake07(t, "dangerous")
	_, err = e.HooksApprove(context.Background())
	files := DangerousFiles(err)
	if len(files) != 1 || files[0].Key != "web/deploy.sh" || !strings.Contains(files[0].Issue, "curl pipe bash") {
		t.Fatalf("%v %+v", err, files)
	}
}

func TestExplainAndFix(t *testing.T) {
	e, log := fake07(t, "")
	ex, err := e.Explain(context.Background(), "api")
	if err != nil || !ex.Matched || ex.Hint == nil || ex.Hint.Fix == nil || !ex.Hint.Fix.Internal || *ex.ExitCode != 124 {
		t.Fatalf("%+v %v", ex, err)
	}
	if !strings.HasPrefix(ex.Hint.Title, "The api hook ran longer") || ex.Hint.Fix.Label != "Raise api deploy_timeout to 4s" {
		t.Fatalf("%+v", ex.Hint)
	}
	lines, wait, err := e.ExplainFix(context.Background(), "api")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for l := range lines {
		out = append(out, l)
	}
	if err := wait(); err != nil || !strings.Contains(strings.Join(out, "\n"), "Fix applied") {
		t.Fatalf("%v %q", err, out)
	}
	argv, _ := os.ReadFile(log)
	if !strings.Contains(string(argv), "explain api --fix --yes") {
		t.Fatalf("argv: %s", argv)
	}
	e, _ = fake07(t, "nomatch")
	if ex, err := e.Explain(context.Background(), "web"); err != nil || ex.Matched || ex.Hint != nil {
		t.Fatalf("%+v %v", ex, err)
	}
}

func TestDeployStopOnFailureFromAndHintEvent(t *testing.T) {
	e, log := fake07(t, "", "FAKE_FAIL=api")
	s, err := e.Deploy(context.Background(), nil, DeployOptions{StopOnFailure: true})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, s)
	if err := s.Wait(); err == nil {
		t.Fatal("want a non-zero exit")
	}
	var hint, stopped, summary *DeployEvent
	for i := range evs {
		switch evs[i].Event {
		case "hint":
			hint = &evs[i]
		case "stopped":
			stopped = &evs[i]
		case "summary":
			summary = &evs[i]
		}
	}
	if hint == nil || hint.Hint() == nil || hint.Hint().Fix == nil || hint.Service != "api" {
		t.Fatalf("hint: %+v", hint)
	}
	if stopped == nil || stopped.After != "api" || stopped.Remaining != 1 || summary == nil || !summary.StoppedEarly || summary.NotRun != 1 {
		t.Fatalf("stopped=%+v summary=%+v", stopped, summary)
	}

	s, _ = e.Deploy(context.Background(), []string{"api", "web"}, DeployOptions{StopOnFailure: true, From: "web"})
	var started []string
	for _, ev := range collect(t, s) {
		if ev.Event == "start" {
			started = append(started, ev.Service)
		}
	}
	_ = s.Wait()
	if strings.Join(started, ",") != "web" {
		t.Fatalf("started %v", started)
	}
	argv, _ := os.ReadFile(log)
	if !strings.Contains(string(argv), "deploy --json --stop-on-failure --from web api web") {
		t.Fatalf("argv: %s", argv)
	}
}

func TestHooksNotApprovedFilesAreObjects(t *testing.T) {
	e, _ := fake07(t, "unapproved")
	s, _ := e.Deploy(context.Background(), nil, DeployOptions{})
	evs := collect(t, s)
	_ = s.Wait()
	if evs[0].Event != "error" || evs[0].Reason != "hooks_not_approved" || len(evs[0].Files) != 1 || evs[0].Files[0].Key != "api/deploy.sh" || evs[0].Files[0].State != "changed" {
		t.Fatalf("%+v", evs[0])
	}
	// Older CLIs sent plain paths.
	ev := ParseDeployLine(`{"event":"error","reason":"hooks_not_approved","files":["api/deploy.sh"]}`, false)
	if ev.Event != "error" || len(ev.Files) != 1 || ev.Files[0].Key != "api/deploy.sh" {
		t.Fatalf("%+v", ev)
	}
	// A diagnosis event's hint is text.
	ev = ParseDeployLine(`{"event":"diagnosis","service":"api","pattern":"ImagePullBackOff","summary":"Can't pull container image","hint":"Check registry auth","pod_status":"0/1 ImagePullBackOff"}`, false)
	if h := ev.Hint(); h == nil || h.Title != "Can't pull container image" || h.Try != "Check registry auth" || ev.PodStatus == "" {
		t.Fatalf("%+v", ev)
	}
}

func TestRollbackRestartLogsStreams(t *testing.T) {
	e, _ := fake07(t, "")
	s, err := e.RollbackStream(context.Background(), "api")
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, s)
	if err := s.Wait(); err != nil || evs[0].Action != "rollback" || evs[len(evs)-1].Event != "summary" || evs[len(evs)-1].RolledBack != 1 {
		t.Fatalf("%v %+v", err, evs)
	}
	s, _ = e.Restart(context.Background(), "web")
	evs = collect(t, s)
	_ = s.Wait()
	if evs[0].Method != "compose" || evs[len(evs)-1].Restarted != 1 {
		t.Fatalf("%+v", evs)
	}
	e, _ = fake07(t, "redeploy")
	s, _ = e.Restart(context.Background(), "web")
	evs = collect(t, s)
	_ = s.Wait()
	if evs[0].Event != "restart" || evs[0].Method != "redeploy" || !strings.Contains(evs[0].Message, "redeploying web") {
		t.Fatalf("%+v", evs[0])
	}

	e, log := fake07(t, "")
	s, _ = e.LogsStream(context.Background(), "api", 200, false)
	evs = collect(t, s)
	_ = s.Wait()
	if len(evs) != 3 || evs[1].Line != "ERROR\tsomething broke" || evs[1].Source != "hook" || evs[2].Event != "end" {
		t.Fatalf("%+v", evs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s, _ = e.LogsStream(ctx, "api", 50, true)
	<-s.C
	cancel()
	done := make(chan struct{})
	go func() {
		for range s.C {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("follow stream didn't stop on cancel")
	}
	argv, _ := os.ReadFile(log)
	if !strings.Contains(string(argv), "logs api --json --tail 200\n") || !strings.Contains(string(argv), "logs api --json --tail 50 --follow") {
		t.Fatalf("argv: %s", argv)
	}
}

func TestAskSessionApproveAndFollowUp(t *testing.T) {
	stdin := filepath.Join(t.TempDir(), "stdin.log")
	e, log := fake07(t, "", "FAKE_STDIN_LOG="+stdin)
	a, err := e.Ask(context.Background(), AskOptions{Question: "--why did api fail?", Consent: true, ReadOnly: false})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var types []string
	for ev := range a.C {
		types = append(types, ev.Type)
		switch ev.Type {
		case "session":
			if ev.Plan != "free" || ev.RemainingSessions() != "4" {
				t.Errorf("session: %+v", ev)
			}
		case "text":
			text.WriteString(ev.Delta)
		case "approval_needed":
			if ev.ToolUseID != "tu_2" || ev.Summary == "" {
				t.Errorf("approval: %+v", ev)
			}
			if err := a.Approve(ev.ToolUseID); err != nil {
				t.Fatal(err)
			}
		case "awaiting_input":
			if strings.Count(strings.Join(types, ","), "awaiting_input") == 1 {
				_ = a.Message("and web?")
			} else {
				_ = a.End()
			}
		}
	}
	if err := a.Wait(); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(types, ",")
	for _, want := range []string{"session", "tool_running", "tool_result", "approval_needed", "turn_end", "awaiting_input", "done"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if !strings.HasPrefix(text.String(), "Let me check the current status first.\nThe **api** deploy failed") ||
		!strings.Contains(text.String(), "Rolled back **api**") || !strings.Contains(text.String(), "follow-up") {
		t.Fatalf("text: %q", text.String())
	}
	in, _ := os.ReadFile(stdin)
	if string(in) != "{\"approve\":\"tu_2\"}\n{\"message\":\"and web?\"}\n{\"end\":true}\n" {
		t.Fatalf("stdin: %q", in)
	}
	argv, _ := os.ReadFile(log)
	if !strings.Contains(string(argv), "ask --json --yes -- --why did api fail?") {
		t.Fatalf("argv: %s", argv)
	}
}

func TestAskDeclineByEOFAndErrors(t *testing.T) {
	stdin := filepath.Join(t.TempDir(), "stdin.log")
	e, _ := fake07(t, "", "FAKE_STDIN_LOG="+stdin)
	a, _ := e.Ask(context.Background(), AskOptions{Question: "hi", Consent: true})
	var declined bool
	for ev := range a.C {
		if ev.Type == "approval_needed" {
			a.Close()
		}
		if ev.Type == "tool_result" && ev.IsError && strings.Contains(ev.Content, "declined") {
			declined = true
		}
	}
	_ = a.Wait()
	if !declined {
		t.Fatal("EOF should decline")
	}
	if err := a.Approve("x"); err == nil {
		t.Fatal("send after close should fail")
	}

	for scenario, want := range map[string]string{"not_logged_in": "not_logged_in", "unauthorized": "unauthorized", "payment": "payment_required", "consent": "consent_required"} {
		e, _ := fake07(t, scenario)
		a, err := e.Ask(context.Background(), AskOptions{Question: "hi"})
		if err != nil {
			t.Fatal(err)
		}
		var got AskEvent
		for ev := range a.C {
			if ev.Type == "error" {
				got = ev
			}
		}
		if err := a.Wait(); err == nil {
			t.Errorf("%s: want exit 1", scenario)
		}
		if got.Err() != want {
			t.Errorf("%s: %+v", scenario, got)
		}
		if scenario == "payment" && got.UpgradeURL != "https://cloud.example.test/billing" {
			t.Errorf("upgrade url: %+v", got)
		}
	}
	if ev := ParseAskLine(`{"type":"error","error":{"code":"overloaded","message":"busy"}}`, false); ev.Err() != "overloaded" {
		t.Fatalf("%+v", ev)
	}
	if ev := ParseAskLine(`{"error":"auth_required","message":"x"}`, true); ev.Type != "error" || ev.Err() != "auth_required" {
		t.Fatalf("%+v", ev)
	}
}

func TestParseDeployLineKeepsBasicsOnTypeMismatch(t *testing.T) {
	ev := ParseDeployLine(`{"event":"done","service":"api","status":"failed","exit_code":"oops","timeout":{"x":1}}`, false)
	if ev.Event != "done" || ev.Service != "api" || ev.Status != "failed" {
		t.Fatalf("%+v", ev)
	}
}
