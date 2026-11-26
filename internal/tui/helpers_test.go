package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Muster-dev/muster-tui/internal/config"
	"github.com/Muster-dev/muster-tui/internal/docker"
	"github.com/Muster-dev/muster-tui/internal/engine"
	"github.com/Muster-dev/muster-tui/internal/theme"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

var fixedNow = time.Date(2026, 11, 26, 14, 0, 0, 0, time.Local)

func TestMain(m *testing.M) {
	theme.Static = true
	lipgloss.SetColorProfile(termenv.Ascii)
	lipgloss.SetHasDarkBackground(true)
	home, err := os.MkdirTemp("", "muster-tui-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("MUSTER_TOKEN", "")
	approveFixture(home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// approveFixture writes a trust-store manifest so the acme hooks count as
// approved on this (fake) machine.
func approveFixture(home string) {
	p := acmeProject()
	m := map[string]string{}
	add := func(key, path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		sum := sha256.Sum256(data)
		m[key] = hex.EncodeToString(sum[:])
	}
	for _, svc := range p.ServiceKeys() {
		for _, h := range []string{"deploy", "health"} {
			add(svc+"/"+h+".sh", p.HookPath(svc, h))
		}
	}
	add("_config/muster.json", p.Path)
	var parts []string
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%q:{\"sha256\":%q}", k, v))
	}
	dir := p.TrustDir()
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "hooks.manifest"), []byte("{"+strings.Join(parts, ",")+"}"), 0o600)
}

func acmeProject() *config.Project {
	abs, _ := filepath.Abs("testdata/acme/muster.json")
	p, err := config.Load(abs)
	if err != nil {
		panic(err)
	}
	return p
}

func fakeEngine(t *testing.T, scenario string) *engine.Engine {
	t.Helper()
	bin, _ := filepath.Abs("testdata/fake-muster")
	e := engine.New(bin, "test-token", "")
	e.Env = []string{"FAKE_SCENARIO=" + scenario}
	return e
}

func fakeDocker(t *testing.T, mode string) *docker.Client {
	t.Helper()
	bin, _ := filepath.Abs("testdata/fake-docker")
	return &docker.Client{Bin: bin, Env: []string{"FAKE_DOCKER=" + mode}}
}

func baseDeps(t *testing.T) Deps {
	t.Helper()
	p := acmeProject()
	return Deps{
		Project:    p,
		Cwd:        p.Dir,
		Engine:     fakeEngine(t, "success"),
		Now:        func() time.Time { return fixedNow },
		NoAutoLoad: true,
		Branch:     "main",
	}
}

// fixtures feeds realistic data for the dashboard.
func fixtures() []any {
	return []any{
		statusMsg{r: engine.StatusResult{Project: "acme-store", Services: map[string]engine.ServiceStatus{
			"redis":  {Name: "Redis", Status: "healthy"},
			"api":    {Name: "API Server", Status: "healthy"},
			"web":    {Name: "Web", Status: "unhealthy"},
			"worker": {Name: "Worker", Status: "disabled"},
		}}},
		historyMsg{evs: []engine.HistoryEvent{
			{Timestamp: "2026-11-25 16:20:00", Service: "redis", Action: "deploy", Status: "ok"},
			{Timestamp: "2026-11-26 11:02:10", Service: "web", Action: "deploy", Status: "ok", Commit: "9d1c2aa71"},
			{Timestamp: "2026-11-26 13:12:45", Service: "web", Action: "deploy", Status: "failed", Commit: "4f2a9c1e0"},
			{Timestamp: "2026-11-26 13:40:05", Service: "api", Action: "deploy", Status: "ok", Commit: "4f2a9c1e0"},
		}},
		dockerSummaryMsg{s: docker.Summary{Running: 5, Total: 7, ImagesSize: "2.1GB"}},
		fleetMsg{},
	}
}

func plain(s string) string {
	lines := strings.Split(ansi.Strip(s), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n") + "\n"
}
