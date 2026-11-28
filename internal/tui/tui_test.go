package tui

import (
	"strings"
	"testing"

	"github.com/Muster-dev/muster-tui/internal/engine"
	tea "github.com/charmbracelet/bubbletea"
)

func feed(m Model, msgs ...tea.Msg) Model {
	for _, msg := range msgs {
		nm, _ := m.Update(msg)
		m = nm.(Model)
	}
	return m
}

func keys(s string) []tea.Msg {
	var out []tea.Msg
	for _, r := range s {
		if r == ' ' {
			out = append(out, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			continue
		}
		out = append(out, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return out
}

// feedRun sends msg and then the message its command produces (one level:
// enough for a palette pick that runs a command line).
func feedRun(m Model, msg tea.Msg) Model {
	nm, cmd := m.Update(msg)
	m = nm.(Model)
	if cmd != nil {
		if out := cmd(); out != nil {
			if _, ok := out.(tea.BatchMsg); !ok {
				m = feed(m, out)
			}
		}
	}
	return m
}

func TestResizeToTinyNeverPanics(t *testing.T) {
	d := baseDeps(t)
	d.Docker = fakeDocker(t, "down")
	m := New(d)
	var msgs []tea.Msg
	for _, f := range fixtures() {
		msgs = append(msgs, f)
	}
	m = feed(m, msgs...)
	screens := []func(e *Env) Screen{
		func(e *Env) Screen { return newDashboard(e) },
		func(e *Env) Screen { return newDeploy(e, []string{"api", "web"}, false, false, false) },
		func(e *Env) Screen { return newDockerScreen(e, 0) },
		func(e *Env) Screen { return newSettings(e) },
		func(e *Env) Screen { return newServers(e) },
		func(e *Env) Screen { return newHistoryScreen(e) },
		func(e *Env) Screen { return newDoctorScreen(e, false) },
		func(e *Env) Screen { return newServiceScreen(e, "api") },
		func(e *Env) Screen { return newApproval(e, "") },
		func(e *Env) Screen { return newOnboarding(e, e.Cwd) },
		func(e *Env) Screen { return newOutput(e, "x", "Output", false, nil) },
	}
	sizes := [][2]int{{0, 0}, {1, 1}, {2, 1}, {10, 3}, {20, 5}, {59, 15}, {60, 16}, {61, 17}, {71, 20}, {72, 20}, {80, 24}, {100, 30}, {140, 40}, {300, 100}}
	for _, mk := range screens {
		s := mk(m.env)
		m.screens[s.Key()] = s
		m.active = s.Key()
		for _, sz := range sizes {
			m = feed(m, tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			checkFrame(t, s.Key(), m.View(), sz[0], sz[1])
			m.overlay = newHelp(m.env, s)
			checkFrame(t, s.Key()+"+help", m.View(), sz[0], sz[1])
			m.overlay = newPalette(m.env, s, nil, "")
			checkFrame(t, s.Key()+"+palette", m.View(), sz[0], sz[1])
			m.overlay = nil
			m.env.Fullscreen = !m.env.Fullscreen
			checkFrame(t, s.Key()+"+fullscreen", m.View(), sz[0], sz[1])
		}
	}
	m = feed(m, tea.WindowSizeMsg{Width: 40, Height: 10})
	if !strings.Contains(m.View(), "Terminal too small") {
		t.Fatal("expected the too-small view")
	}
}

// checkFrame asserts a frame is exactly w×h so the terminal never scrolls.
func checkFrame(t *testing.T, name, view string, w, h int) {
	t.Helper()
	if w == 0 || h == 0 {
		return
	}
	lines := strings.Split(view, "\n")
	if len(lines) != h {
		t.Fatalf("%s at %dx%d: %d lines", name, w, h, len(lines))
	}
	for i, l := range lines {
		if lw := width(l); lw > w {
			t.Fatalf("%s at %dx%d: line %d is %d wide: %q", name, w, h, i, lw, l)
		}
	}
}

func TestPaletteCompletionAndDidYouMean(t *testing.T) {
	m := New(baseDeps(t))
	m = feed(m, tea.WindowSizeMsg{Width: 100, Height: 30}, tea.KeyMsg{Type: tea.KeyCtrlK})
	p, ok := m.overlay.(*palette)
	if !ok {
		t.Fatal("ctrl+k should open the palette")
	}
	m = feed(m, keys("lo")...)
	if p.sel >= len(p.shown) || p.shown[p.sel].label != "Logs" {
		t.Fatalf("first match: %+v", p.shown)
	}
	// Logs needs a service: Enter fills the input and lists them.
	m = feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if p.input.Value() != "logs " || m.overlay == nil {
		t.Fatalf("enter should ask for the service, got %q", p.input.Value())
	}
	m = feed(m, keys("ap")...)
	if p.shown[p.sel].line != "logs api" {
		t.Fatalf("service completion: %+v", p.shown[p.sel])
	}
	m = feedRun(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.overlay != nil || m.active != "logs:api" {
		t.Fatalf("logs api should open (active %s)", m.active)
	}

	m = feed(m, tea.KeyMsg{Type: tea.KeyCtrlK})
	p = m.overlay.(*palette)
	m = feed(m, keys("rollbak wbe")...)
	p.sel = len(p.shown) // no row: run what was typed
	m = feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if p.suggestion != "rollback web" {
		t.Fatalf("suggestion %q (%s)", p.suggestion, p.errMsg)
	}
	if !strings.Contains(m.View(), "did you mean") {
		t.Fatal("the palette should offer the fix")
	}
	m = feed(m, tea.KeyMsg{Type: tea.KeyTab})
	if p.input.Value() != "rollback web" {
		t.Fatalf("tab should apply the suggestion, got %q", p.input.Value())
	}
}

func TestPaletteRecentAndQuit(t *testing.T) {
	m := New(baseDeps(t))
	m = feed(m, tea.WindowSizeMsg{Width: 100, Height: 30}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = feed(m, keys("status")...)
	m = feedRun(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.overlay != nil {
		t.Fatal("running a command closes the palette")
	}
	m = feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	p := m.overlay.(*palette)
	if len(p.shown) < 2 || p.shown[0].label != "Recent" || p.shown[1].line != "status" {
		t.Fatalf("recent commands first: %+v", p.shown[:2])
	}
	m = feed(m, tea.KeyMsg{Type: tea.KeyCtrlC}) // closes the palette
	if m.overlay != nil || m.quitArmed {
		t.Fatal("first ctrl+c should close the palette")
	}
	m = feed(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.quitArmed || !strings.Contains(m.View(), "ctrl+c again") {
		t.Fatal("second ctrl+c should arm quit")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("third ctrl+c should quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("expected tea.Quit")
	}
}

func TestDeployStateMachineSkipAndAbort(t *testing.T) {
	m := New(baseDeps(t))
	e := m.env
	d := newDeploy(e, []string{"redis", "api", "web"}, false, false, true)
	d.started = fixedNow
	d.phase = phRunning
	d.taskID = 42
	d.runs[0].state = rsRunning
	ev := func(js string) { d.onEvent(engine.ParseDeployLine(js, false)) }
	ev(`{"event":"start","service":"redis","log_file":"/p/l.log"}`)
	ev(`{"event":"log","service":"redis","line":"Error: port is already allocated"}`)
	ev(`{"event":"done","service":"redis","status":"failed","exit_code":1}`)
	d.onTaskEnd(false)
	if d.phase != phFailed || d.runs[0].state != rsFailed {
		t.Fatalf("phase=%v state=%v", d.phase, d.runs[0].state)
	}
	if diag := strings.Join(diagnose(d.runs[0]), " "); !strings.Contains(diag, "port") {
		t.Fatalf("diagnosis %q", diag)
	}
	// Skip moves on to api.
	d.recover("skip")
	if d.cur != 1 || d.runs[0].state != rsSkipped || d.runs[1].state != rsRunning {
		t.Fatalf("after skip cur=%d states=%v,%v", d.cur, d.runs[0].state, d.runs[1].state)
	}
	e.Tasks.Cancel(d.taskID)
	// api: deployed but unhealthy → failed with recovery.
	ev(`{"event":"done","service":"api","status":"success"}`)
	ev(`{"event":"health","service":"api","status":"unhealthy"}`)
	d.onTaskEnd(false)
	if d.phase != phFailed || d.runs[1].health != "unhealthy" {
		t.Fatalf("unhealthy should stop: %v", d.phase)
	}
	d.recover("abort")
	if d.phase != phAborted || d.runs[2].state != rsNotRun {
		t.Fatalf("abort: %v %v", d.phase, d.runs[2].state)
	}
	if v := d.View(80, 20); !strings.Contains(v, "Deploy stopped") {
		t.Fatalf("summary missing:\n%s", v)
	}
}

func TestDeployCancelMarksRemainingNotRun(t *testing.T) {
	m := New(baseDeps(t))
	d := newDeploy(m.env, []string{"redis", "api"}, false, false, true)
	d.phase, d.started = phRunning, fixedNow
	d.runs[0].state = rsRunning
	d.onTaskEnd(true)
	if d.phase != phAborted || d.runs[0].state != rsCanceled || d.runs[1].state != rsNotRun {
		t.Fatalf("%v %v %v", d.phase, d.runs[0].state, d.runs[1].state)
	}
}

func TestDeployAllAsksFirst(t *testing.T) {
	m := New(baseDeps(t))
	m = feed(m, tea.WindowSizeMsg{Width: 100, Height: 30}, runLineMsg{"deploy"})
	d, ok := m.screens["deploy"].(*deployScreen)
	if !ok || d.phase != phConfirm {
		t.Fatal("deploying everything should ask first")
	}
	if v := m.View(); !strings.Contains(v, "Deploy 4 services") || !strings.Contains(v, "dry run first") {
		t.Fatalf("confirm view:\n%s", v)
	}
	m = feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	d = m.screens["deploy"].(*deployScreen)
	if !d.dry || d.phase != phRunning || m.overlay != nil {
		t.Fatalf("d should start a dry run (dry=%v phase=%v)", d.dry, d.phase)
	}
	m.Shutdown()
}

func TestLogsSearchFilterAndJump(t *testing.T) {
	m := New(baseDeps(t))
	m = feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	s := newOutput(m.env, "t", "Logs · api", false, nil)
	s.lines = []string{"booting", "WARN disk at 80%", "listening on :8080", "ERROR db timeout", "request ok", "ERROR db timeout again"}
	s.done = true
	if got := len(s.visible()); got != 6 {
		t.Fatal(got)
	}
	s.level = 2
	if got := len(s.visible()); got != 2 {
		t.Fatalf("errors only: %d", got)
	}
	s.level = 0
	s.query = "timeout"
	s.jumpMatch(1, true)
	if s.cursor != 3 {
		t.Fatalf("first match at %d", s.cursor)
	}
	s.jumpMatch(1, false)
	if s.cursor != 5 {
		t.Fatalf("next match at %d", s.cursor)
	}
	s.cursor = 0
	s.jumpError(1)
	if s.cursor != 3 {
		t.Fatalf("jump to error %d", s.cursor)
	}
	if v := s.View(60, 10); !strings.Contains(v, "/timeout") {
		t.Fatalf("filter tag missing:\n%s", v)
	}
}

func TestUnifiedDiff(t *testing.T) {
	a := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	b := []string{"a", "b", "C", "d", "e", "f", "g", "h", "i"}
	d := strings.Join(unifiedDiff(a, b, 1), "\n")
	want := "@@ -2,3 +2,3 @@\n b\n-c\n+C\n d\n@@ -8,1 +8,2 @@\n h\n+i"
	if d != want {
		t.Fatalf("got:\n%s\nwant:\n%s", d, want)
	}
}

func TestSettingsValidationBlocksSave(t *testing.T) {
	m := New(baseDeps(t))
	s := newSettings(m.env)
	s.focusService("api")
	// find the port field and set an invalid value
	for i, f := range s.form.fields {
		if f.key == "health.port" {
			s.form.sel = i
		}
	}
	s.form.fields[s.form.sel].value = "99999"
	s.commit()
	s.validate()
	if len(s.errs) == 0 || s.form.fields[s.form.sel].err == "" {
		t.Fatal("expected a validation error on the port field")
	}
	cmd := s.save()
	msg := cmd()
	if tm, ok := msg.(toastMsg); !ok || tm.level != lvlErr {
		t.Fatalf("save should refuse: %#v", msg)
	}
}

func TestNoProjectStartsHomeAndDockerWorks(t *testing.T) {
	d := baseDeps(t)
	d.Project = nil
	d.Cwd = t.TempDir()
	m := New(d)
	if m.active != "dashboard" {
		t.Fatalf("active = %s", m.active)
	}
	m = feed(m, tea.WindowSizeMsg{Width: 100, Height: 30}, fleetMsg{})
	if v := plain(m.View()); !strings.Contains(v, "Set up this folder") || strings.Contains(v, "no project") {
		t.Fatalf("welcome:\n%s", v)
	}
	m = feed(m, tea.WindowSizeMsg{Width: 100, Height: 30}, runLineMsg{"docker"})
	if m.active != "docker" {
		t.Fatalf("docker should open outside a project, got %s", m.active)
	}
	m = feed(m, runLineMsg{"deploy"})
	if !strings.Contains(m.env.State.Toast, "setup") {
		t.Fatalf("deploy without a project should point at setup: %q", m.env.State.Toast)
	}
}

func TestEscDoesNotCancelBackgroundDeploy(t *testing.T) {
	m := New(baseDeps(t))
	m = feed(m, tea.WindowSizeMsg{Width: 100, Height: 30}, runLineMsg{"deploy api"})
	d := m.screens["deploy"].(*deployScreen)
	task := m.env.Tasks.Get(d.taskID)
	if task == nil {
		t.Fatal("deploy task should be registered")
	}
	m = feed(m, runLineMsg{"dashboard"}, tea.KeyMsg{Type: tea.KeyEsc})
	if task.Canceled() {
		t.Fatal("Esc on the dashboard cancelled a background deploy")
	}
	m = feed(m, runLineMsg{"abort"})
	if !task.Canceled() {
		t.Fatal("`abort` should cancel the deploy")
	}
	m.Shutdown()
}
