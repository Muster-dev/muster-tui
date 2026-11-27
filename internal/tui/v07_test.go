package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if !strings.Contains(m.env.State.Toast, "0.6.2 is older than 0.7.0") {
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
	f.key("a").wait("Approve 3 files on this machine?", "contains a risky command")
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
	f.send(refreshMsg{"fleet"}, runLineMsg{"servers"}).wait("edge-1", "prod-1", "deploy@10.0.0.5:2222", "not paired")
	f.key("U").wait("2/3 healthy", "unreachable")
	_, view := f.end()
	if !strings.Contains(view, "Groups") || !strings.Contains(view, "edge-1, prod-1") {
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
