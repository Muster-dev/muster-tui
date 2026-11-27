package fleet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseShapes(t *testing.T) {
	inv, err := Parse([]byte(`{"machines":[],"groups":[]}`))
	if err != nil || len(inv.Machines) != 0 {
		t.Fatalf("%v %+v", err, inv)
	}
	inv, err = Parse([]byte(`{"machines":{"prod-1":{"host":"10.0.0.5","user":"deploy","port":2222,"transport":"ssh","hook_mode":"manual"},"edge-1":{"host":"edge","user":"ops","port":"22","transport":"cloud"}},"groups":{"prod":["prod-1","edge-1"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Machines) != 2 || inv.Machines[0].Name != "edge-1" || inv.Machines[1].Port != 2222 || inv.Machines[1].Mode != "muster" {
		t.Fatalf("%+v", inv.Machines)
	}
	if inv.Machines[1].Target() != "deploy@10.0.0.5:2222" {
		t.Fatal(inv.Machines[1].Target())
	}
	if len(inv.Groups) != 1 || len(inv.Groups[0].Machines) != 2 {
		t.Fatalf("%+v", inv.Groups)
	}
}

func TestSSHTestUsesBatchMode(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	script := filepath.Join(dir, "ssh")
	_ = os.WriteFile(script, []byte("#!/bin/sh\necho \"$*\" > "+log+"\n[ \"$FAIL\" = 1 ] && { echo 'ssh: connect to host x port 22: Connection refused' >&2; exit 255; }\nexit 0\n"), 0o755)
	old := SSHBin
	SSHBin = script
	defer func() { SSHBin = old }()
	m := Machine{Name: "prod-1", Host: "10.0.0.5", User: "deploy", Port: 2222}
	if _, err := Test(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	argv, _ := os.ReadFile(log)
	if !strings.Contains(string(argv), "BatchMode=yes") || !strings.Contains(string(argv), "-p 2222 deploy@10.0.0.5 true") {
		t.Fatalf("argv = %s", argv)
	}
	t.Setenv("FAIL", "1")
	if _, err := Test(context.Background(), m); err == nil || !strings.Contains(err.Error(), "Connection refused") {
		t.Fatalf("err = %v", err)
	}
}

func TestCloudStatus(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	if CloudStatus().LoggedIn {
		t.Fatal("not logged in")
	}
	_ = os.MkdirAll(filepath.Join(h, ".muster", "tokens"), 0o700)
	_ = os.WriteFile(filepath.Join(h, ".muster", "settings.json"), []byte(`{"cloud":{"org_id":"org_1","name":"ricky","relay":"wss://r"}}`), 0o600)
	_ = os.WriteFile(filepath.Join(h, ".muster", "tokens", "cloud.json"), []byte(`{"tokens":{"default":"t"}}`), 0o600)
	if c := CloudStatus(); !c.LoggedIn || c.Name != "ricky" {
		t.Fatalf("%+v", c)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// muster ≥ 0.7: machines and groups are arrays of objects.
func TestParseV07Shape(t *testing.T) {
	inv, err := Parse(fixture(t, "list-v07.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(inv.Names(), ","); got != "edge-1,prod-1" {
		t.Fatalf("machines = %s", got)
	}
	edge, prod := inv.Machines[0], inv.Machines[1]
	if edge.Transport != "cloud" || edge.Mode != "push" || edge.Port != 22 || edge.Paired == nil || !*edge.Paired {
		t.Fatalf("edge = %+v", edge)
	}
	if prod.Target() != "deploy@10.0.0.5:2222" || prod.Mode != "muster" || prod.Path != "/opt/acme" ||
		prod.Identity != "~/.ssh/id_ed25519" || prod.Fleet != "default" || prod.Group != "prod" || *prod.Paired {
		t.Fatalf("prod = %+v", prod)
	}
	if got := strings.Join(inv.GroupNames(), ","); got != "canary,prod" {
		t.Fatalf("groups = %s", got)
	}
	if got := strings.Join(inv.Groups[1].Machines, ","); got != "edge-1,prod-1" {
		t.Fatalf("prod members = %s", got)
	}

	inv, err = Parse(fixture(t, "list-v07-remotes.json"))
	if err != nil || len(inv.Machines) != 1 || inv.Machines[0].Name != "web-1" || inv.Machines[0].Group != "" ||
		inv.Machines[0].Fleet != "" || inv.Machines[0].Path != "/srv/app" || len(inv.Groups) != 0 {
		t.Fatalf("%v %+v", err, inv)
	}
	inv, err = Parse(fixture(t, "list-v07-empty.json"))
	if err != nil || len(inv.Machines) != 0 || len(inv.Groups) != 0 {
		t.Fatalf("%v %+v", err, inv)
	}
}

// muster < 0.7 and remotes.json: objects keyed by name.
func TestParseLegacyShape(t *testing.T) {
	inv, err := Parse(fixture(t, "list-legacy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(inv.Names(), ","); got != "edge-1,prod-1" {
		t.Fatalf("machines = %s", got)
	}
	prod := inv.Machines[1]
	if prod.Target() != "deploy@10.0.0.5:2222" || prod.Mode != "muster" || prod.Path != "/opt/acme" || prod.Paired != nil {
		t.Fatalf("prod = %+v", prod)
	}
	if inv.Machines[0].Port != 22 || inv.Machines[0].Transport != "cloud" {
		t.Fatalf("edge = %+v", inv.Machines[0])
	}
	if got := strings.Join(inv.GroupNames(), ","); got != "canary,prod" {
		t.Fatalf("groups = %s", got)
	}
	if got := strings.Join(inv.Groups[0].Machines, ","); got != "edge-1" {
		t.Fatalf("canary = %s", got)
	}
}

func TestParseSameGroupInTwoFleets(t *testing.T) {
	inv, err := Parse([]byte(`{"machines":[{"name":"a","host":"a"},{"name":"b","host":"b"}],"groups":[{"name":"default","fleet":"f1","machines":["a"]},{"name":"default","fleet":"f2","machines":["b","a"]}]}`))
	if err != nil || len(inv.Groups) != 1 || strings.Join(inv.Groups[0].Machines, ",") != "a,b" {
		t.Fatalf("%v %+v", err, inv.Groups)
	}
}

func TestParseStatusShapes(t *testing.T) {
	st, err := ParseStatus(fixture(t, "status-v07.json"))
	if err != nil || len(st) != 2 {
		t.Fatalf("%v %+v", err, st)
	}
	if st[0].Machine != "edge-1" || st[0].Reachable || st[0].Error != "unreachable" || st[0].Healthy != nil {
		t.Fatalf("edge = %+v", st[0])
	}
	if p := st[1]; !p.Reachable || p.Healthy == nil || *p.Healthy != 2 || *p.Total != 3 || len(p.Services) != 3 || p.Services[1].Status != "unhealthy" {
		t.Fatalf("prod = %+v", p)
	}

	st, err = ParseStatus(fixture(t, "status-legacy.json"))
	if err != nil || len(st) != 2 || st[0].Reachable || !st[1].Reachable || st[1].Host != "10.0.0.5" || st[1].Healthy != nil {
		t.Fatalf("%v %+v", err, st)
	}
	if _, err := ParseStatus([]byte("oops")); err == nil {
		t.Fatal("want an error")
	}
}

func TestCloudStatusKeychainRef(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	_ = os.MkdirAll(filepath.Join(h, ".muster"), 0o700)
	_ = os.WriteFile(filepath.Join(h, ".muster", "settings.json"), []byte(`{"assistant":"off","cloud":{"org_id":"org_1","token_ref":"default"}}`), 0o600)
	c := CloudStatus()
	if !c.LoggedIn || !c.HasToken || !c.AssistantOff {
		t.Fatalf("%+v", c)
	}
}
