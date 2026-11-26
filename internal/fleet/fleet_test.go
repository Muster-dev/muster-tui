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
