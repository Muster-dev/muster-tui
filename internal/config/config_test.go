package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestScanCompose(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "docker-compose.yml"), `
services:
  web:
    image: nginx:alpine
    ports: ["3000:80"]
  api:
    build: ./api
    ports:
      - "127.0.0.1:8080:8080"
  redis:
    image: redis:7
  debug:
    image: busybox
    profiles: [debug]
`)
	plan, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Stack != "compose" || plan.Fallback {
		t.Fatalf("stack=%q fallback=%v", plan.Stack, plan.Fallback)
	}
	var keys []string
	for _, s := range plan.Services {
		keys = append(keys, s.Key)
	}
	if want := []string{"redis", "api", "web", "debug"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("order = %v, want %v", keys, want)
	}
	if h := plan.Services[0].Health; h.Type != "tcp" || h.Port != 6379 {
		t.Fatalf("redis health = %+v", h)
	}
	if h := plan.Services[1].Health; h.Type != "http" || h.Port != 8080 {
		t.Fatalf("api health = %+v", h)
	}
	if plan.Services[3].Include {
		t.Fatal("profiled service should be excluded by default")
	}
	args := strings.Join(plan.SetupArgs(), " ")
	for _, want := range []string{"--stack compose", "--services redis,api,web", "--health redis=tcp:6379", "--health web=http:/:3000"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args %q missing %q", args, want)
		}
	}
}

func TestScanFallback(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "package.json"), `{}`)
	plan, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Fallback || plan.SetupArgs()[len(plan.SetupArgs())-1] != "--scan" {
		t.Fatalf("expected fallback to CLI scan, got %+v", plan)
	}
}

func TestLoadValidateSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "muster.json")
	write(t, path, `{"project":"acme","services":{"api":{"name":"API","health":{"type":"http","port":8080,"enabled":true},"credentials":{"mode":"off"},"x_custom":1},"redis":{"name":"Redis"}},"deploy_order":["redis","api"]}`)
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ServiceKeys(); !reflect.DeepEqual(got, []string{"redis", "api"}) {
		t.Fatalf("keys = %v", got)
	}
	if errs := p.Validate(); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	p.Doc.Set(70000, "services", "api", "health", "port")
	p.Doc.Set("sometimes", "services", "api", "credentials", "mode")
	p.Doc.Set([]string{"redis", "api", "ghost"}, "deploy_order")
	errs := p.Validate()
	if len(errs) != 3 {
		t.Fatalf("want 3 errors, got %v", errs)
	}
	if err := p.Save(); err == nil {
		t.Fatal("save should refuse invalid config")
	}
	p.Doc.Set(9090, "services", "api", "health", "port")
	p.Doc.Set("save", "services", "api", "credentials", "mode")
	p.Doc.Set([]string{"redis", "api"}, "deploy_order")
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"x_custom": 1`) || !strings.Contains(string(data), `"port": 9090`) {
		t.Fatalf("saved file lost data:\n%s", data)
	}
}

func TestHookChanges(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "muster.json"), `{"services":{"api":{}}}`)
	write(t, filepath.Join(dir, ".muster/hooks/api/deploy.sh"), "echo hi\n")
	write(t, filepath.Join(dir, ".muster/hooks/api/health.sh"), "exit 0\n")
	p, _ := Load(filepath.Join(dir, "muster.json"))
	if ch := p.HookChanges(); len(ch) != 2 || ch[0].Kind != "new" {
		t.Fatalf("no manifest: %+v", ch)
	}
	sum, _ := fileSHA(filepath.Join(dir, ".muster/hooks/api/deploy.sh"))
	write(t, p.ManifestPath(), `{"api/deploy.sh":{"sha256":"`+sum+`"},"api/rollback.sh":{"sha256":"x"},"api/health.sh":{"sha256":"stale"}}`)
	ch := p.HookChanges()
	got := map[string]string{}
	for _, c := range ch {
		got[c.Key] = c.Kind
	}
	want := map[string]string{"api/health.sh": "changed", "api/rollback.sh": "removed"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %v, want %v", got, want)
	}
}

func TestToKeyAndFriendlyName(t *testing.T) {
	if got := ToKey("My-API.v2"); got != "my_api_v2" {
		t.Fatal(got)
	}
	if got := FriendlyName("payment-api"); got != "Payment API" {
		t.Fatal(got)
	}
}
