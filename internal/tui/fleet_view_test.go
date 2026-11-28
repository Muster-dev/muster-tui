package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Muster-dev/muster-tui/internal/fleet"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	oldFleetHelp = "Info:\n  list [--json]                 Show machines\n  test [name|group]             Test connectivity (SSH or cloud)\n"
	newFleetHelp = "Info:\n  list [--json]                 Show machines\n  test [target] [--json]        Test connectivity\n  logs <server> <service> [--tail N] [--json]\n  restart <server> [service...] --yes [--json]\n"
)

var fleetFixture = fleetMsg{inv: fleet.Inventory{Machines: []fleet.Machine{
	{Name: "byricky", Host: "agent-byricky", User: "ricky", Transport: "cloud", Mode: "muster"},
	{Name: "edge", Host: "10.0.0.9", User: "deploy", Transport: "ssh", Mode: "push"},
}}}

func intp(n int) *int { return &n }

var byrickyHealth = fleetHealthMsg{st: []fleet.MachineStatus{{
	Machine: "byricky", Reachable: true, Healthy: intp(2), Total: intp(2), MusterVersion: "0.7.1",
	Services: []fleet.ServiceHealth{{Service: "pond", Status: "healthy"}, {Service: "web", Status: "healthy"}},
}}}

// fleetDeps: no project, a fake muster that knows the new fleet commands
// when newCLI is set.
func fleetDeps(t *testing.T, newCLI bool, env ...string) Deps {
	d := serverDeps(t)
	d.Engine = fakeEngine(t, "")
	if newCLI {
		d.Engine.Env = append(d.Engine.Env, "FAKE_FLEET_NEW=1")
	}
	d.Engine.Env = append(d.Engine.Env, env...)
	return d
}

func fleetHelp(newCLI bool) fleetHelpMsg {
	if newCLI {
		return fleetHelpMsg{out: newFleetHelp}
	}
	return fleetHelpMsg{out: oldFleetHelp}
}

func TestFleetTestUsesJSONWhenSupported(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv")
	f := startFlow(t, fleetDeps(t, true, "FAKE_LOG="+log), 80, 24)
	f.send(fleetHelp(true), fleetFixture, runLineMsg{"fleet test"}).
		wait("Connected · muster OK · 120 ms", "Operation", "1 of 2 failed", "enter/esc back", "r run again", "y copy")
	m, v := f.end()
	argv, _ := os.ReadFile(log)
	if !strings.Contains(string(argv), "fleet test --json") {
		t.Fatalf("argv:\n%s", argv)
	}
	if contains(v, "┌") || contains(v, "machine") || contains(v, "/ search") {
		t.Fatalf("raw CLI text or log hints leaked:\n%s", v)
	}
	if m.active != "fleet-test:" {
		t.Fatalf("active %s", m.active)
	}
}

func TestFleetTestFallsBackToText(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv")
	f := startFlow(t, fleetDeps(t, false, "FAKE_LOG="+log), 80, 24)
	f.send(fleetHelp(false), fleetFixture, runLineMsg{"fleet test"}).
		wait("byricky", "Connected · muster OK", "isn't", "SSH couldn't connect", "2 of 4 failed")
	_, v := f.end()
	argv, _ := os.ReadFile(log)
	if strings.Contains(string(argv), "--json") {
		t.Fatalf("old CLI got --json:\n%s", argv)
	}
	if contains(v, "│") || contains(v, "┌") || contains(v, "machine(s)") {
		t.Fatalf("box drawing leaked:\n%s", v)
	}
}

func TestServerLogsAndRestartFollowTheCLI(t *testing.T) {
	m := New(fleetDeps(t, false))
	m = feed(m, tea.WindowSizeMsg{Width: 80, Height: 24}, fleetHelp(false), fleetFixture, byrickyHealth, runLineMsg{"server byricky"})
	v := plain(m.View())
	if strings.Count(v, "needs a newer muster") != 2 {
		t.Fatalf("old CLI should disable Logs and Restart with a reason:\n%s", v)
	}
	m = feed(m, fleetHelp(true))
	v = plain(m.View())
	if contains(v, "needs a newer muster") || !contains(v, "recent log lines") || !contains(v, "muster 0.7.1") {
		t.Fatalf("new CLI should enable them:\n%s", v)
	}
	// The header carries the status; the body doesn't repeat the name.
	lines := strings.Split(v, "\n")
	if !contains(lines[0], "Servers › byricky") || !contains(lines[0], "Online") || contains(lines[1], "byricky") {
		t.Fatalf("header:\n%s", strings.Join(lines[:3], "\n"))
	}
}

func TestServerLogsPicksAServiceAndOpensTheViewer(t *testing.T) {
	f := startFlow(t, fleetDeps(t, true), 80, 24)
	f.send(fleetHelp(true), fleetFixture, byrickyHealth, runLineMsg{"server byricky"}).wait("Logs")
	f.key("l").wait("Logs for which service?", "pond")
	f.key("down").key("enter").wait("ERROR upstream timed out", "/ search")
	m, v := f.end()
	if m.active != "flogs:byricky:web" || !contains(v, "Servers › byricky › Logs · web") {
		t.Fatalf("active %s\n%s", m.active, v)
	}
}

func TestServerRestartConfirmsFirst(t *testing.T) {
	log := filepath.Join(t.TempDir(), "argv")
	f := startFlow(t, fleetDeps(t, true, "FAKE_LOG="+log), 80, 24)
	f.send(fleetHelp(true), fleetFixture, byrickyHealth, runLineMsg{"server byricky"}).wait("Restart")
	f.key("R").wait("Restart which service?", "All services")
	f.key("down").key("down").key("enter").wait("Restart web on byricky?")
	if argv, _ := os.ReadFile(log); strings.Contains(string(argv), "restart") {
		t.Fatalf("restarted before the confirm:\n%s", argv)
	}
	f.key("enter").wait("Restarted web on byricky")
	f.end()
	argv, _ := os.ReadFile(log)
	if !strings.Contains(string(argv), "fleet restart byricky web --yes --json") {
		t.Fatalf("argv:\n%s", argv)
	}
}

func TestCleanOutputStripsBoxesAndSaysServer(t *testing.T) {
	in := []string{"", "  \x1b[33m┌─Deploy──────┐\x1b[0m", "  │  ● web   ok│", "  └─────────────┘", "", "", "  * All 2 machine(s) passed\r"}
	got := strings.Join(cleanOutput(nil, in), "\n")
	if strings.ContainsAny(got, "┌│└\x1b\r") || !strings.Contains(got, "All 2 servers passed") || strings.Contains(got, "\n\n\n") {
		t.Fatalf("%q", got)
	}
}

func TestFleetStatusCardCountsOnlineServers(t *testing.T) {
	m := New(fleetDeps(t, true))
	s := newFleetResult(m.env, "status", "")
	s.status = []fleet.MachineStatus{byrickyHealth.st[0], {Machine: "edge", Reachable: false}}
	if got := s.statusSummary(); got != "1 of 2 online" {
		t.Fatalf("summary %q", got)
	}
	s.status = s.status[:1]
	if got := s.statusSummary(); got != "Online" || !s.allOnline() {
		t.Fatalf("summary %q", got)
	}
}
