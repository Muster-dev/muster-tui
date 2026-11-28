package tui

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest"
)

// Every scenario is checked narrow, medium and wide: wide terminals centre
// a reading column and split list screens into master-detail.
var sizes = []struct{ w, h int }{{80, 24}, {120, 40}, {200, 55}}

type scenario struct {
	name  string
	deps  func(t *testing.T) Deps
	send  []tea.Msg
	until []string // substrings that must appear before the snapshot
	wide  []string // extra substrings at wide sizes
}

func scenarios() []scenario {
	return []scenario{
		{
			name: "dashboard",
			deps: baseDeps,
			send: func() []tea.Msg {
				var out []tea.Msg
				for _, f := range fixtures() {
					out = append(out, f)
				}
				return out
			}(),
			until: []string{"Recent activity", "unhealthy"},
		},
		{
			name: "deploy-in-progress",
			deps: func(t *testing.T) Deps {
				d := baseDeps(t)
				d.Engine = fakeEngine(t, "progress")
				return d
			},
			send:  []tea.Msg{runLineMsg{"deploy api"}},
			until: []string{"npm run build"},
		},
		{
			name: "deploy-failed-recovery",
			deps: func(t *testing.T) Deps {
				d := baseDeps(t)
				d.Engine = fakeEngine(t, "failure")
				return d
			},
			send:  []tea.Msg{runLineMsg{"deploy redis"}},
			until: []string{"Retry", "Docker isn"},
		},
		{
			name: "docker",
			deps: func(t *testing.T) Deps {
				d := baseDeps(t)
				d.Docker = fakeDocker(t, "")
				return d
			},
			send:  []tea.Msg{runLineMsg{"docker"}},
			until: []string{"acme-api-1", "acme-redis-1"},
			wide:  []string{"3.10%"},
		},
		{
			name:  "fleet-test-json",
			deps:  func(t *testing.T) Deps { return fleetDeps(t, true) },
			send:  []tea.Msg{fleetHelp(true), fleetFixture, runLineMsg{"fleet test"}},
			until: []string{"1 of 2 failed", "r run again"},
		},
		{
			name:  "fleet-test-text",
			deps:  func(t *testing.T) Deps { return fleetDeps(t, false) },
			send:  []tea.Msg{fleetHelp(false), fleetFixture, runLineMsg{"fleet test"}},
			until: []string{"2 of 4 failed", "r run again"},
		},
		{
			name:  "server-actions",
			deps:  func(t *testing.T) Deps { return fleetDeps(t, true) },
			send:  []tea.Msg{fleetHelp(true), fleetFixture, byrickyHealth, runLineMsg{"server byricky"}},
			until: []string{"Restart", "muster 0.7.1"},
		},
		{
			name:  "server-actions-old-cli",
			deps:  func(t *testing.T) Deps { return fleetDeps(t, false) },
			send:  []tea.Msg{fleetHelp(false), fleetFixture, byrickyHealth, runLineMsg{"server byricky"}},
			until: []string{"Restart", "newer muster"},
		},
		{
			name:  "home-servers",
			deps:  func(t *testing.T) Deps { return fleetDeps(t, true) },
			send:  []tea.Msg{fleetHelp(true), fleetFixture, byrickyHealth},
			until: []string{"byricky", "2/2 healthy"},
		},
		{
			name:  "welcome",
			deps:  serverDeps,
			send:  []tea.Msg{fleetMsg{}},
			until: []string{"m u s t e r", "Set up this folder"},
		},
		{
			name:  "palette",
			deps:  baseDeps,
			send:  append(append([]tea.Msg{}, anyMsgs(fixtures())...), tea.KeyMsg{Type: tea.KeyCtrlK}),
			until: []string{"Commands", "Search"},
		},
		{
			name:  "deploy-done",
			deps:  baseDeps,
			send:  []tea.Msg{runLineMsg{"deploy api"}},
			until: []string{"Deployed 1 service", "full log"},
		},
		{
			name:  "explain",
			deps:  func(t *testing.T) Deps { return deps07(t, "approved") },
			send:  []tea.Msg{runLineMsg{"explain api"}},
			until: []string{"Likely cause:", "Fix available"},
		},
		{
			name:  "doctor",
			deps:  baseDeps,
			send:  []tea.Msg{runLineMsg{"doctor"}},
			until: []string{"Fine", "run again"},
		},
		{
			name:  "settings",
			deps:  baseDeps,
			send:  []tea.Msg{runLineMsg{"settings"}},
			until: []string{"Colour"},
		},
		{
			name: "onboarding",
			deps: func(t *testing.T) Deps {
				dir, _ := filepath.Abs("testdata/sample-compose")
				return Deps{Cwd: dir, Engine: fakeEngine(t, ""), Now: func() time.Time { return fixedNow }, NoAutoLoad: true, Branch: "main"}
			},
			send:  []tea.Msg{runLineMsg{"setup"}, tea.KeyMsg{Type: tea.KeyEnter}},
			until: []string{"Create", "Customize"},
		},
	}
}

func TestGolden(t *testing.T) {
	for _, sc := range scenarios() {
		for _, sz := range sizes {
			t.Run(fmt.Sprintf("%s-%dx%d", sc.name, sz.w, sz.h), func(t *testing.T) {
				m := New(sc.deps(t))
				tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(sz.w, sz.h))
				for _, msg := range sc.send {
					tm.Send(msg)
				}
				until := sc.until
				if sz.w >= 120 {
					until = append(append([]string{}, until...), sc.wide...)
				}
				teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
					for _, u := range until {
						if !bytes.Contains(b, []byte(u)) {
							return false
						}
					}
					return true
				}, teatest.WithDuration(8*time.Second), teatest.WithCheckInterval(20*time.Millisecond))
				// Let any trailing messages from the same batch land.
				time.Sleep(150 * time.Millisecond)
				tm.Send(snapshotMsg{})
				if err := tm.Quit(); err != nil {
					t.Fatal(err)
				}
				fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(Model)
				fm.Shutdown()
				golden.RequireEqual(t, []byte(plain(fm.View())))
			})
		}
	}
}

func anyMsgs(xs []any) []tea.Msg {
	out := make([]tea.Msg, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// snapshotMsg is a no-op used to flush the event loop before snapshotting.
type snapshotMsg struct{}
