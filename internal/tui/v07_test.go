package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Muster-dev/muster-tui/internal/engine"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// fake07Engine is the muster 0.7 fake CLI (shared with the engine tests).
func deps07(t *testing.T, scenario string, env ...string) Deps {
	t.Helper()
	d := baseDeps(t)
	bin, _ := filepath.Abs("../engine/testdata/fake-muster-07")
	d.Engine.Bin = bin
	d.Engine.Env = append([]string{"FAKE_SCENARIO=" + scenario}, env...)
	d.CLIVersion = "v0.7.0"
	return d
}

// flow drives the real program (tasks run the fake CLI) step by step.
type flow struct {
	t  *testing.T
	tm *teatest.TestModel
}

func startFlow(t *testing.T, d Deps, w, h int) *flow {
	t.Helper()
	tm := teatest.NewTestModel(t, New(d), teatest.WithInitialTermSize(w, h))
	return &flow{t: t, tm: tm}
}

func (f *flow) send(msgs ...tea.Msg) *flow {
	for _, m := range msgs {
		f.tm.Send(m)
	}
	return f
}

func (f *flow) key(s string) *flow {
	f.tm.Send(keyFromString(s))
	return f
}

// wait blocks until every substring has been drawn since the last wait.
func (f *flow) wait(subs ...string) *flow {
	f.t.Helper()
	teatest.WaitFor(f.t, f.tm.Output(), func(b []byte) bool {
		for _, s := range subs {
			if !bytes.Contains(b, []byte(s)) {
				return false
			}
		}
		return true
	}, teatest.WithDuration(8*time.Second), teatest.WithCheckInterval(20*time.Millisecond))
	return f
}

// end quits and returns the final model's screen as plain text.
func (f *flow) end() (Model, string) {
	f.t.Helper()
	time.Sleep(150 * time.Millisecond)
	f.tm.Send(snapshotMsg{})
	if err := f.tm.Quit(); err != nil {
		f.t.Fatal(err)
	}
	fm := f.tm.FinalModel(f.t, teatest.WithFinalTimeout(5*time.Second)).(Model)
	fm.Shutdown()
	return fm, plain(fm.View())
}

func TestOldCLIGetsUpgradeHint(t *testing.T) {
	d := baseDeps(t) // fake-muster reports 0.5.60
	d.CLIVersion = "muster 0.5.60"
	m := New(d)
	m = feed(m, tea.WindowSizeMsg{Width: 120, Height: 30}, runLineMsg{"explain api"})
	if m.active == "explain" || !strings.Contains(m.env.State.Toast, "needs muster 0.7.0") || !strings.Contains(m.env.State.Toast, "0.5.60") {
		t.Fatalf("active=%s toast=%q", m.active, m.env.State.Toast)
	}
	// Restart falls back to the docker/redeploy path, logs to raw output.
	if m.env.V07() {
		t.Fatal("0.5.60 has no engine API")
	}
	// The startup notice.
	m.env.State.Caps.Known = false
	nm, _ := m.Update(TaskMsg{Msg: versionMsg{out: "v0.6.2"}})
	m = nm.(Model)
	_ = m.dispatch(TaskMsg{Msg: versionMsg{out: "v0.6.2"}}, fixedNow)
	if !strings.Contains(m.env.State.Toast, "0.6.2 predates 0.7.0") {
		t.Fatalf("toast=%q", m.env.State.Toast)
	}
}

func TestVersionDetectedAtStartup(t *testing.T) {
	d := deps07(t, "approved")
	d.CLIVersion = ""
	d.NoAutoLoad = false
	f := startFlow(t, d, 120, 30)
	f.wait("acme-store")
	m, _ := f.end()
	if !m.env.State.Caps.EngineAPI {
		t.Fatalf("caps: %+v", m.env.State.Caps)
	}
}

func TestStatusRichFieldsOnScreens(t *testing.T) {
	d := deps07(t, "approved")
	f := startFlow(t, d, 140, 40)
	f.send(refreshMsg{"status"}).wait("A deploy is running")
	f.send(runLineMsg{"open api"}).wait("Last deploy", "2/3 ready", "localhost:8080/health")
	_, view := f.end()
	for _, want := range []string{"being deployed by sam@ci-runner-3", "Deploying", "now, by sam@ci-runner-3 (fleet)", "4f2a9c1", "Replicas"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in\n%s", want, view)
		}
	}
}

func TestDashboardShowsProjectLock(t *testing.T) {
	d := deps07(t, "approved")
	f := startFlow(t, d, 140, 40)
	f.send(refreshMsg{"status"}).wait("A deploy is running")
	_, view := f.end()
	if !strings.Contains(view, "started by sam at 2026-11-26 13:39:00 (api web)") {
		t.Fatalf("\n%s", view)
	}
}

func TestApproveHooksWithConfirm(t *testing.T) {
	state := filepath.Join(t.TempDir(), "approved")
	log := filepath.Join(t.TempDir(), "argv")
	d := deps07(t, "", "FAKE_STATE="+state, "FAKE_LOG="+log)
	f := startFlow(t, d, 140, 40)
	f.send(runLineMsg{"approve"}).wait("api/deploy.sh", "web/deploy.sh", "muster.json", "risky command")
	f.key("a").wait("Approve 3 files on this computer?", "contains a risky command")
	f.key("enter").wait("Hooks approved")
	m, _ := f.end()
	argv, _ := os.ReadFile(log)
	if !strings.Contains(string(argv), "hooks approve --yes --json") {
		t.Fatalf("argv:\n%s", argv)
	}
	if len(m.env.State.HookChanges) != 0 {
		t.Fatalf("still pending: %+v", m.env.State.HookChanges)
	}
}

func TestApproveDangerousIsRefused(t *testing.T) {
	d := deps07(t, "dangerous")
	f := startFlow(t, d, 140, 40)
	f.send(runLineMsg{"approve"}).wait("web/deploy.sh")
	f.key("a").wait("Approve 3 files")
	f.key("enter").wait("dangerous command", "curl pipe bash")
	m, _ := f.end()
	if m.active != "approval" {
		t.Fatalf("active=%s", m.active)
	}
}

func TestApproveCancelRunsNothing(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv")
	d := deps07(t, "", "FAKE_LOG="+log)
	f := startFlow(t, d, 140, 40)
	f.send(runLineMsg{"approve"}).wait("api/deploy.sh")
	f.key("a").wait("Approve 3 files")
	f.key("esc")
	f.end()
	argv, _ := os.ReadFile(log)
	if strings.Contains(string(argv), "hooks approve") {
		t.Fatalf("approved without a yes:\n%s", argv)
	}
}

func TestRestartRollbackAndLogsUseJSON(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv")
	d := deps07(t, "approved", "FAKE_LOG="+log, "FAKE_FOLLOW=1")
	f := startFlow(t, d, 140, 40)
	f.send(runLineMsg{"restart web"}).wait("Restart web?", "redeployed instead")
	f.key("enter").wait("Restarting web (docker compose restart)", "Container acme-web-1", "Restarted 1 service")
	f.send(runLineMsg{"rollback api"}).wait("Roll back api?")
	f.key("enter").wait("Rolling back api", "api rolled back", "Rolled back 1 service")
	f.send(runLineMsg{"logs api"}).wait("api log line 29", "something broke")
	f.end()
	argv, _ := os.ReadFile(log)
	for _, want := range []string{"restart web --json", "rollback api --json", "logs api --json --tail 200 --follow"} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("missing %q in\n%s", want, argv)
		}
	}
}

func TestServersShowNewFleetShape(t *testing.T) {
	d := deps07(t, "approved")
	f := startFlow(t, d, 120, 30)
	f.send(refreshMsg{"fleet"}, runLineMsg{"servers"}).wait("edge-1", "prod-1", "SSH · deploy@10.0.0.5:2222", "Fleet Cloud", "Needs pairing")
	f.key("r").wait("2/3 healthy", "Offline")
	_, view := f.end()
	if !strings.Contains(view, "Groups") || !strings.Contains(view, "edge-1, prod-1") || strings.Contains(view, "MACHINE") {
		t.Fatalf("\n%s", view)
	}
}

func TestExplainScreenFixAfterConfirm(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv")
	d := deps07(t, "approved", "FAKE_LOG="+log)
	f := startFlow(t, d, 120, 30)
	f.send(runLineMsg{"explain api"}).wait("Likely cause:", "deploy_timeout and was stopped", "Fix available: Raise api deploy_timeout to 4s")
	f.key("f").wait("Raise api deploy_timeout to 4s?", "It edits muster.json")
	f.key("esc")
	time.Sleep(200 * time.Millisecond)
	if argv, _ := os.ReadFile(log); strings.Contains(string(argv), "--fix") {
		t.Fatalf("fix ran without a yes:\n%s", argv)
	}
	f.key("f").wait("Apply fix")
	f.key("enter").wait("Fix applied: Raise api deploy_timeout", "deploy again with d")
	f.end()
	argv, _ := os.ReadFile(log)
	if !strings.Contains(string(argv), "explain api --fix --yes") {
		t.Fatalf("argv:\n%s", argv)
	}
}

func TestExplainNothingToExplain(t *testing.T) {
	d := deps07(t, "nomatch")
	f := startFlow(t, d, 120, 30)
	f.send(runLineMsg{"explain web"}).wait("No known problem pattern matched")
	f.end()
}

func TestDeployAllIsOneStopOnFailureCall(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv")
	d := deps07(t, "approved", "FAKE_LOG="+log)
	f := startFlow(t, d, 120, 30)
	f.send(runLineMsg{"deploy api web"}).wait("Deployed 2 services")
	m, _ := f.end()
	argv, _ := os.ReadFile(log)
	if n := strings.Count(string(argv), "deploy --json"); n != 1 || !strings.Contains(string(argv), "deploy --json --stop-on-failure api web") {
		t.Fatalf("want one call:\n%s", argv)
	}
	dd := m.screens["deploy"].(*deployScreen)
	if dd.runs[0].state != rsOK || dd.runs[1].state != rsOK {
		t.Fatalf("%v %v", dd.runs[0].state, dd.runs[1].state)
	}
}

func TestDeployFailureShowsHintFixThenSkipResumesFrom(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv")
	d := deps07(t, "approved", "FAKE_LOG="+log, "FAKE_FAIL=api")
	f := startFlow(t, d, 120, 34)
	f.send(runLineMsg{"deploy api web"}).wait("api timed out after 2s", "Likely cause:", "Fix: Raise api deploy_timeout to 4s · f", "Abort   Fix")
	f.key("f").wait("Raise api deploy_timeout to 4s?", "Apply fix")
	f.key("enter").wait("Fix applied — r to retry api")
	f.key("s").wait("Finished with problems")
	m, view := f.end()
	argv, _ := os.ReadFile(log)
	for _, want := range []string{"deploy --json --stop-on-failure api web", "explain api --fix --yes", "deploy --json --stop-on-failure --from web api web"} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("missing %q in\n%s", want, argv)
		}
	}
	dd := m.screens["deploy"].(*deployScreen)
	if dd.runs[0].state != rsSkipped || dd.runs[1].state != rsOK {
		t.Fatalf("states %v %v\n%s", dd.runs[0].state, dd.runs[1].state, view)
	}
}

func TestDeployFailureFallsBackToExplain(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv")
	d := deps07(t, "nohint", "FAKE_LOG="+log, "FAKE_FAIL=web")
	f := startFlow(t, d, 120, 34)
	f.send(runLineMsg{"deploy api web"}).wait("web timed out", "Likely cause:", "web hook ran longer")
	f.end()
	argv, _ := os.ReadFile(log)
	if !strings.Contains(string(argv), "explain web --json") {
		t.Fatalf("argv:\n%s", argv)
	}
}

func TestDeployRetryAfterFixGoesThroughApproval(t *testing.T) {
	d := deps07(t, "approved", "FAKE_FAIL=api")
	f := startFlow(t, d, 120, 34)
	f.send(runLineMsg{"deploy api web"}).wait("Abort   Fix")
	f.send(hooksMsg{hs: engine.HooksStatus{SecurityEnabled: true, Config: &engine.HookFile{Key: "_config/muster.json", Path: "muster.json", State: "changed"}}})
	// The approval screen opens; the CLI then says nothing is pending any
	// more, so Enter carries on with retry.
	f.key("r").wait("continue with retry")
	f.key("enter")
	time.Sleep(time.Second)
	m, _ := f.end()
	if dd := m.screens["deploy"].(*deployScreen); dd.runs[0].attempts != 2 {
		t.Fatalf("attempts=%d", dd.runs[0].attempts)
	}
}

func TestStreamRoutingFollowsMustersOrder(t *testing.T) {
	m := New(deps07(t, "approved"))
	d := newDeploy(m.env, []string{"api", "redis", "web"}, false, false, true)
	d.phase, d.started = phRunning, fixedNow
	d.beginRun(d.runs[0]) // we guessed api goes first
	ev := func(js string) { d.onEvent(engine.ParseDeployLine(js, false)) }
	ev(`{"event":"start","service":"redis"}`) // infra first
	ev(`{"event":"done","service":"redis","status":"success"}`)
	ev(`{"event":"start","service":"api"}`)
	if d.runs[0].key != "redis" || d.runs[0].state != rsOK || d.runs[1].key != "api" || d.runs[1].state != rsRunning || d.runs[1].attempts != 1 {
		t.Fatalf("%s=%v %s=%v attempts=%d", d.runs[0].key, d.runs[0].state, d.runs[1].key, d.runs[1].state, d.runs[1].attempts)
	}
	ev(`{"event":"done","service":"api","status":"failed","exit_code":1}`)
	ev(`{"event":"stopped","reason":"failure","after":"api","remaining":1}`)
	d.onTaskEnd(false)
	if d.phase != phFailed || d.failedService() != "api" || d.runs[2].state != rsPending {
		t.Fatalf("phase=%v failed=%s web=%v", d.phase, d.failedService(), d.runs[2].state)
	}
}

func startFlowModel(t *testing.T, m Model, w, h int) *flow {
	t.Helper()
	return &flow{t: t, tm: teatest.NewTestModel(t, m, teatest.WithInitialTermSize(w, h))}
}

func signedIn(m Model) Model {
	m.env.State.Cloud.HasToken = true
	m.env.State.Cloud.LoggedIn = true
	return m
}

func TestAskConsentStreamApproveFollowUp(t *testing.T) {
	dir := t.TempDir()
	stdin, log := filepath.Join(dir, "stdin"), filepath.Join(dir, "argv")
	m := signedIn(New(deps07(t, "consent", "FAKE_STDIN_LOG="+stdin, "FAKE_LOG="+log)))
	f := startFlowModel(t, m, 120, 34)
	f.send(runLineMsg{"ask why did api fail?"}).wait("Send redacted logs and config from acme-store", "y send redacted data")
	f.key("y").wait("free plan · 4 sessions left", "Let me check the current status first.", "Checking service status", "the database password is wrong", "Roll back api to the previous version", "Run this?")
	f.key("y").wait("Approved: Roll back api", "Rolling back api", "Rolled back api", "Ask a follow-up")
	f.send(keys("and web?")...)
	f.key("enter").wait("You asked a follow-up")
	f.key("enter").wait("new question")
	m, view := f.end()
	in, _ := os.ReadFile(stdin)
	if string(in) != "{\"approve\":\"tu_2\"}\n{\"message\":\"and web?\"}\n{\"end\":true}\n" {
		t.Fatalf("stdin: %q", in)
	}
	argv, _ := os.ReadFile(log)
	if !strings.Contains(string(argv), "ask --json --yes -- why did api fail?") {
		t.Fatalf("argv:\n%s", argv)
	}
	if strings.Contains(view, "**") {
		t.Fatalf("markdown markers leaked:\n%s", view)
	}
	if a := m.screens["ask"].(*askScreen); a.phase != askDone {
		t.Fatalf("phase=%v", a.phase)
	}
}

func TestAskDeclineAndConsentRefused(t *testing.T) {
	dir := t.TempDir()
	stdin := filepath.Join(dir, "stdin")
	m := New(deps07(t, "", "FAKE_STDIN_LOG="+stdin))
	_ = os.MkdirAll(filepath.Join(m.env.State.Project.Dir, ".muster"), 0o755)
	consent := filepath.Join(m.env.State.Project.Dir, ".muster", "assistant.json")
	_ = os.WriteFile(consent, []byte(`{"consent":true}`), 0o600)
	defer os.Remove(consent)
	m = New(deps07(t, "", "FAKE_STDIN_LOG="+stdin))
	f := startFlowModel(t, m, 120, 34)
	f.send(runLineMsg{"ask why?"}).wait("Run this?")
	f.key("n").wait("Declined: Roll back api", "not rolling back")
	f.key("esc")
	f.end()
	if in, _ := os.ReadFile(stdin); !strings.HasPrefix(string(in), "{\"decline\":\"tu_2\"}\n") {
		t.Fatalf("stdin: %q", in)
	}

	os.Remove(consent)
	log := filepath.Join(dir, "argv2")
	m = New(deps07(t, "", "FAKE_LOG="+log))
	f = startFlowModel(t, m, 120, 34)
	f.send(runLineMsg{"ask why?"}).wait("Send redacted logs")
	f.key("n").wait("Nothing was sent")
	f.end()
	if argv, _ := os.ReadFile(log); strings.Contains(string(argv), "ask") {
		t.Fatalf("asked without consent:\n%s", argv)
	}
}

func TestAskNotLoggedInAndPayment(t *testing.T) {
	m := New(deps07(t, "not_logged_in"))
	m.screens["ask"] = nil
	delete(m.screens, "ask")
	a := newAsk(m.env, "hi")
	a.consent, a.phase = true, askRunning
	m = feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	f := startFlowModel(t, signedIn(m), 120, 30)
	f.send(navMsg{a}).wait("needs a Fleet Cloud login", "Sign in to Fleet Cloud", "cloud login")
	fm, _ := f.end()
	if ok, why := fm.env.assistantReady(); ok || why != "needs a Fleet Cloud login" {
		t.Fatalf("assistant should be hidden now: %v %q", ok, why)
	}

	m = New(deps07(t, "payment"))
	a = newAsk(m.env, "hi")
	a.consent, a.phase = true, askRunning
	f = startFlowModel(t, signedIn(m), 120, 30)
	f.send(navMsg{a}).wait("sessions are used up", "Upgrade to keep going: https://cloud.example.test/billing")
	f.end()
}

func TestExplainWithAIOnlyWhenAvailable(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv")
	// Not signed in: no button.
	m := New(deps07(t, "approved", "FAKE_FAIL=api"))
	f := startFlowModel(t, m, 120, 34)
	f.send(runLineMsg{"deploy api web"}).wait("Abort   Fix")
	_, view := f.end()
	if strings.Contains(view, "Explain with AI") {
		t.Fatalf("shown without a login:\n%s", view)
	}

	m = signedIn(New(deps07(t, "approved", "FAKE_FAIL=api", "FAKE_LOG="+log)))
	_ = os.MkdirAll(filepath.Join(m.env.State.Project.Dir, ".muster"), 0o755)
	consent := filepath.Join(m.env.State.Project.Dir, ".muster", "assistant.json")
	_ = os.WriteFile(consent, []byte(`{"consent":true}`), 0o600)
	defer os.Remove(consent)
	f = startFlowModel(t, m, 120, 34)
	f.send(runLineMsg{"deploy api web"}).wait("Explain with AI", "· e")
	f.key("e").wait("read-only", "Why did the api deploy fail")
	f.end()
	argv, _ := os.ReadFile(log)
	if !strings.Contains(string(argv), "ask --json --yes --read-only -- Why did the api deploy fail, and how do I fix it?") {
		t.Fatalf("argv:\n%s", argv)
	}
}

func TestRecoveryTakesKeysAfterApprovalThenDeploy(t *testing.T) {
	state := filepath.Join(t.TempDir(), "approved")
	d := deps07(t, "", "FAKE_STATE="+state, "FAKE_FAIL=api")
	m := New(d)
	m.env.State.HookChanges = hookChangesFrom(m.env.State.Project, mustHooks(t, d))
	f := startFlowModel(t, m, 120, 34)
	f.send(runLineMsg{"deploy api web"}).wait("Approve hooks")
	f.key("a").wait("on this computer?")
	f.key("enter").wait("Likely cause")
	fm, _ := f.end()
	if d, ok := fm.screens["deploy"].(*deployScreen); !ok || d.phase != phFailed || fm.active != "deploy" {
		t.Fatal("the recovery menu should be showing")
	}
}

func mustHooks(t *testing.T, d Deps) engine.HooksStatus {
	hs, err := d.Engine.WithDir(d.Project.Dir).HooksStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return hs
}

func TestRetryRoutesTheRestOfTheStream(t *testing.T) {
	m := New(deps07(t, "approved"))
	d := newDeploy(m.env, []string{"api", "web"}, false, false, true)
	d.started = fixedNow
	m.env.Tasks.Cancel(0)
	d.phase, d.cur = phFailed, 0
	d.runs[0].state, d.runs[0].exitCode = rsFailed, 1
	d.recover("retry")
	defer m.env.Tasks.CancelAll()
	if d.phase != phRunning {
		t.Fatalf("retry should be running, phase=%v", d.phase)
	}
	ev := func(js string) { d.onEvent(engine.ParseDeployLine(js, false)) }
	ev(`{"event":"start","service":"api","name":"API"}`)
	ev(`{"event":"done","service":"api","status":"success"}`)
	ev(`{"event":"start","service":"web","name":"Web"}`)
	if d.runs[0].name != "API" || d.runs[0].state != rsOK || d.cur != 1 || d.runs[1].state != rsRunning {
		t.Fatalf("api=%q/%v cur=%d web=%v", d.runs[0].name, d.runs[0].state, d.cur, d.runs[1].state)
	}
}

func TestLegacyRetryShowsRunning(t *testing.T) {
	m := New(baseDeps(t))
	d := newDeploy(m.env, []string{"api"}, false, false, true)
	d.phase = phFailed
	d.runs[0].state = rsFailed
	d.recover("retry")
	defer m.env.Tasks.CancelAll()
	if d.phase != phRunning || strings.Contains(d.View(80, 20), "[Retry]") {
		t.Fatalf("phase=%v\n%s", d.phase, d.View(80, 20))
	}
}
