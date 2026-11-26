package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/fleet"
	"github.com/Muster-dev/muster-tui/internal/prompt"
	tea "github.com/charmbracelet/bubbletea"
)

// needsArg reports whether line names a command that still needs its
// required argument (so Enter on a completion shouldn't run it yet).
func needsArg(line string) bool {
	f := strings.Fields(strings.TrimPrefix(line, "/"))
	if len(f) != 1 {
		return false
	}
	c, ok := prompt.Lookup(strings.ToLower(f[0]))
	if !ok {
		return false
	}
	switch c.Args {
	case prompt.ArgMachine, prompt.ArgContainer, prompt.ArgSub:
		return true
	}
	return false
}

// execute parses and runs a command line.
func (m *Model) execute(line string, fromPrompt bool) tea.Cmd {
	ctx := m.env.State.PromptContext()
	p, err := prompt.Parse(line, ctx)
	if err != nil {
		var pe *prompt.Error
		if errors.As(err, &pe) {
			if fromPrompt {
				m.prompt.errInput, m.prompt.errMsg, m.prompt.suggestion = line, pe.Msg, pe.Suggestion
				m.prompt.popup = false
				return nil
			}
			return m.setToast(lvlErr, pe.Error())
		}
		return m.setToast(lvlErr, err.Error())
	}
	if fromPrompt {
		m.prompt.hist.Add(line)
		m.prompt.Clear()
	}
	return m.run(p)
}

func (m *Model) needProject() tea.Cmd {
	if m.env.State.Project != nil {
		return nil
	}
	return m.setToast(lvlWarn, "No muster.json here — type setup to create one")
}

func (m *Model) needEngine() tea.Cmd {
	if m.env.Engine != nil {
		return nil
	}
	msg := "The muster CLI isn't installed"
	if m.env.EngineErr != nil {
		msg = m.env.EngineErr.Error()
	}
	return m.setToast(lvlErr, msg)
}

// pickService resolves the single service a command acts on.
func (m *Model) pickService(p prompt.Parsed) (string, tea.Cmd) {
	if len(p.Args) > 0 {
		return p.Args[0], nil
	}
	keys := m.env.State.ServiceKeys()
	if len(keys) == 1 {
		return keys[0], nil
	}
	// Use the service whose screen is open.
	if s, ok := m.activeScreen().(*serviceScreen); ok {
		return s.svc, nil
	}
	m.prompt.SetValue(p.Cmd.Name + " ")
	m.prompt.refresh(m.env.State.PromptContext())
	m.focus = focusPrompt
	return "", m.setToast(lvlInfo, "Which service? "+strings.Join(keys, ", "))
}

func (m *Model) run(p prompt.Parsed) tea.Cmd {
	e := m.env
	switch p.Cmd.Name {
	case "quit":
		if t := e.Tasks.Foreground(); t != nil {
			m.overlay = newConfirm(confirmMsg{title: "Quit while “" + t.Label + "” is running?", body: []string{"It will be cancelled."}, yes: "Quit", onYes: func() tea.Msg { e.Tasks.CancelAll(); return tea.QuitMsg{} }})
			return nil
		}
		e.Tasks.CancelAll()
		return tea.Quit
	case "help":
		m.overlay = newHelp(e, m.activeScreen())
		return nil
	case "palette":
		m.overlay = newPalette(e, m.activeScreen())
		return nil
	case "back":
		return m.back()
	case "dashboard":
		return m.open(m.screens["dashboard"])

	case "deploy", "dry-run":
		if c := firstCmd(m.needProject(), m.needEngine()); c != nil {
			return c
		}
		svcs := p.Args
		if len(svcs) == 1 && (svcs[0] == "all" || svcs[0] == "everything") {
			svcs = nil
		}
		if n := len(e.State.HookChanges); n > 0 && !p.Has("--dry-run") {
			return m.open(newApproval(e, "deploy "+strings.Join(p.Args, " ")))
		}
		if d, ok := m.screens["deploy"].(*deployScreen); ok && d.running() {
			return m.setToast(lvlWarn, "A deploy is already running — Esc cancels it")
		}
		return m.open(newDeploy(e, svcs, p.Has("--dry-run"), p.Has("--force"), len(svcs) > 0))
	case "retry", "skip", "abort":
		if d, ok := m.screens["deploy"].(*deployScreen); ok {
			m.active = "deploy"
			ns, cmd := d.recover(p.Cmd.Name)
			m.screens["deploy"] = ns
			return cmd
		}
		if p.Cmd.Name == "abort" {
			if t := e.Tasks.Foreground(); t != nil {
				e.Tasks.Cancel(t.ID)
				return m.setToast(lvlWarn, "Cancelled: "+t.Label)
			}
		}
		return m.setToast(lvlInfo, "Nothing to "+p.Cmd.Name)
	case "rollback":
		if c := firstCmd(m.needProject(), m.needEngine()); c != nil {
			return c
		}
		svc, c := m.pickService(p)
		if svc == "" {
			return c
		}
		if d, ok := m.screens["deploy"].(*deployScreen); ok && d.failedService() == svc {
			m.active = "deploy"
			ns, cmd := d.recover("rollback")
			m.screens["deploy"] = ns
			return cmd
		}
		eng := e.Eng()
		m.overlay = newConfirm(confirmMsg{
			title: "Roll back " + svc + "?",
			body:  []string{"Runs .muster/hooks/" + svc + "/rollback.sh to restore the previous version."},
			yes:   "Roll back",
			onYes: nav(newOutput(e, "rollback:"+svc, "Rollback · "+svc, true, func(ctx context.Context) (<-chan string, func() error, error) {
				return eng.Rollback(ctx, svc)
			})),
		})
		return nil
	case "restart":
		if c := m.needProject(); c != nil {
			return c
		}
		svc, c := m.pickService(p)
		if svc == "" {
			return c
		}
		return m.restart(svc)
	case "logs":
		if c := m.needProject(); c != nil {
			return c
		}
		svc, c := m.pickService(p)
		if svc == "" {
			return c
		}
		return m.open(newServiceLogs(e, svc))
	case "open":
		if c := m.needProject(); c != nil {
			return c
		}
		svc, c := m.pickService(p)
		if svc == "" {
			return c
		}
		return m.open(newServiceScreen(e, svc))
	case "status":
		if c := m.needProject(); c != nil {
			return c
		}
		return tea.Batch(e.loadStatus(), m.setToast(lvlInfo, "Checking health…"), m.open(m.screens["dashboard"]))
	case "history":
		if c := m.needProject(); c != nil {
			return c
		}
		s := newHistoryScreen(e)
		if len(p.Args) > 0 {
			s.svcFilter = p.Args[0]
		}
		return m.open(s)
	case "doctor":
		if c := firstCmd(m.needProject(), m.needEngine()); c != nil {
			return c
		}
		return m.open(newDoctorScreen(e, p.Has("--fix")))
	case "docker":
		return m.open(newDockerScreen(e, 0))
	case "images":
		return m.open(newDockerScreen(e, 1))
	case "volumes":
		return m.open(newDockerScreen(e, 2))
	case "prune":
		s := newDockerScreen(e, 3)
		what := "images"
		if len(p.Args) > 0 {
			what = p.Args[0]
		}
		s.pendingPrune = what
		return m.open(s)
	case "exec":
		if e.Docker == nil {
			return m.setToast(lvlErr, "Docker isn't installed")
		}
		return m.execProcess(e.Docker.ShellCmd(p.Args[0]), func(err error) tea.Msg {
			return execDone("Shell in "+p.Args[0], err)
		})
	case "servers":
		return m.open(newServers(e))
	case "ssh":
		mc, ok := e.State.Fleet.Find(p.Args[0])
		if !ok {
			return m.setToast(lvlErr, "No machine called "+p.Args[0])
		}
		return m.execProcess(fleet.SSHCmd(mc), func(err error) tea.Msg { return execDone("SSH to "+mc.Name, err) })
	case "fleet deploy", "fleet status", "fleet test", "fleet rollback":
		if c := m.needEngine(); c != nil {
			return c
		}
		return m.open(fleetOutput(e, strings.TrimPrefix(p.Cmd.Name, "fleet "), p.Args))
	case "fleet add":
		s := newServers(e)
		s.startAdd(p.Args)
		return m.open(s)
	case "cloud login", "cloud logout":
		if c := m.needEngine(); c != nil {
			return c
		}
		args := strings.Fields(p.Cmd.Name)
		return m.execProcess(e.Eng().Interactive(args...), func(err error) tea.Msg {
			return tea.Batch(refresh("fleet"), func() tea.Msg { return execDone("muster "+p.Cmd.Name, err) })()
		})
	case "cloud status":
		if c := m.needEngine(); c != nil {
			return c
		}
		eng := e.Eng()
		return m.open(newOutput(e, "cloud", "Fleet Cloud", false, func(ctx context.Context) (<-chan string, func() error, error) {
			return rawLines(ctx, eng.Command(ctx, "cloud", "status"))
		}))
	case "setup":
		if e.State.Project != nil {
			return m.setToast(lvlInfo, "This folder already has "+shortPath(e.State.Project.Path)+" — settings changes it")
		}
		return m.open(newOnboarding(e, e.Cwd))
	case "settings":
		s := newSettings(e)
		if len(p.Args) > 0 {
			s.focusService(p.Args[0])
		}
		return m.open(s)
	case "approve":
		if c := m.needProject(); c != nil {
			return c
		}
		return m.open(newApproval(e, ""))
	case "edit":
		if c := m.needProject(); c != nil {
			return c
		}
		svc, c := m.pickService(p)
		if svc == "" {
			return c
		}
		hook := "deploy"
		if len(p.Args) > 1 {
			hook = p.Args[1]
		}
		return m.editFile(e.State.Project.HookPath(svc, hook))
	case "cleanup":
		if c := firstCmd(m.needProject(), m.needEngine()); c != nil {
			return c
		}
		eng := e.Eng()
		return m.open(newOutput(e, "cleanup", "Cleanup", true, func(ctx context.Context) (<-chan string, func() error, error) {
			return rawLines(ctx, eng.Command(ctx, "cleanup"))
		}))
	}
	return m.setToast(lvlWarn, "`"+p.Cmd.Name+"` isn't available here yet")
}

func firstCmd(cmds ...tea.Cmd) tea.Cmd {
	for _, c := range cmds {
		if c != nil {
			return c
		}
	}
	return nil
}

// execDone reports the result of a suspended interactive process.
func execDone(what string, err error) tea.Msg {
	if err != nil {
		var xe *exec.ExitError
		if errors.As(err, &xe) {
			return toastMsg{level: lvlWarn, text: fmt.Sprintf("%s exited with %d", what, xe.ExitCode())}
		}
		return toastMsg{level: lvlErr, text: what + ": " + err.Error()}
	}
	return toastMsg{level: lvlOK, text: what + " finished"}
}

// editFile suspends the TUI and opens path in $EDITOR.
func (m *Model) editFile(path string) tea.Cmd {
	editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
	parts := strings.Fields(editor)
	cmd := exec.Command(parts[0], append(parts[1:], path)...)
	return m.execProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return toastMsg{level: lvlErr, text: "Editor: " + err.Error()}
		}
		return reloadConfigMsg{}
	})
}

// restart restarts the service's container when one exists, else redeploys.
func (m *Model) restart(svc string) tea.Cmd {
	e := m.env
	if e.Docker != nil {
		for _, c := range e.State.Containers {
			if c.ComposeService() == svc || c.Name() == svc {
				id, name := c.ID, c.Name()
				d := e.Docker
				_, cmd := e.Tasks.Run("restarting "+name, "", true, func(ctx context.Context) tea.Msg {
					if err := d.Do(ctx, "restart", id); err != nil {
						return toastMsg{level: lvlErr, text: "Restart failed: " + err.Error()}
					}
					return toastMsg{level: lvlOK, text: "Restarted " + name}
				})
				return tea.Batch(cmd, m.setToast(lvlInfo, "Restarting "+name+"…"))
			}
		}
	}
	return tea.Batch(m.setToast(lvlInfo, "No container found for "+svc+" — redeploying it instead"), runLine("deploy "+svc))
}
