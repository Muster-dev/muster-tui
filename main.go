// muster-tui is the interactive terminal UI for muster. The bash `muster` CLI
// stays the engine and the headless interface (`muster <cmd> --json`).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Muster-dev/muster-tui/internal/auth"
	"github.com/Muster-dev/muster-tui/internal/config"
	"github.com/Muster-dev/muster-tui/internal/docker"
	"github.com/Muster-dev/muster-tui/internal/engine"
	"github.com/Muster-dev/muster-tui/internal/theme"
	"github.com/Muster-dev/muster-tui/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
)

var version = "1.0.0-dev"

const usage = `muster-tui — the interactive UI for muster

Usage:
  muster-tui [flags]          open the TUI for the project in this folder
  muster <command> [--json]   headless use goes through the muster CLI

Flags:
  -C, --dir <path>     run as if started in <path>
      --no-mouse       leave the mouse to the terminal (native text selection)
      --set-token <t>  save a MUSTER_TOKEN for the CLI's --json commands
  -v, --version        print the version
  -h, --help           show this help

Inside the TUI press ctrl+k (or / or :) for every action and to type commands
(deploy, logs api, docker), ? for keys. Click rows and hints with the mouse;
turn it off with --no-mouse, MUSTER_TUI_MOUSE=off or "tui_mouse": "off" in
~/.muster/settings.json.
`

func main() {
	dir := ""
	noMouse := false
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--version", "-v":
			fmt.Printf("muster-tui %s\n", version)
			return
		case "--help", "-h":
			fmt.Print(usage)
			return
		case "--set-token":
			if i+1 >= len(args) || args[i+1] == "" {
				fmt.Fprintln(os.Stderr, "usage: muster-tui --set-token <token>")
				os.Exit(2)
			}
			if err := auth.SaveToken(args[i+1]); err != nil {
				fmt.Fprintf(os.Stderr, "muster-tui: saving token: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Token saved.")
			return
		case "--no-mouse":
			noMouse = true
		case "-C", "--dir":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "usage: muster-tui --dir <path>")
				os.Exit(2)
			}
			dir = args[i+1]
			i++
		default:
			fmt.Fprintf(os.Stderr, "muster-tui: unknown argument %q\n\n%s", a, usage)
			os.Exit(2)
		}
	}
	if err := run(dir, noMouse || mouseOff()); err != nil {
		fmt.Fprintf(os.Stderr, "muster-tui: %v\n", err)
		os.Exit(1)
	}
}

// mouseOff reads MUSTER_TUI_MOUSE and the tui_mouse global setting.
func mouseOff() bool {
	switch strings.ToLower(os.Getenv("MUSTER_TUI_MOUSE")) {
	case "off", "0", "false", "no":
		return true
	case "on", "1", "true", "yes":
		return false
	}
	if g, err := config.LoadGlobal(); err == nil {
		if v, ok := g.Value("tui_mouse"); ok {
			if s, ok := v.(string); ok && strings.EqualFold(s, "off") {
				return true
			}
		}
	}
	return false
}

func run(dir string, noMouse bool) error {
	theme.Init()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if dir != "" {
		cwd, err = filepath.Abs(dir)
		if err != nil {
			return err
		}
	}

	deps := tui.Deps{Cwd: cwd, Version: version, HistoryPath: filepath.Join(config.MusterHome(), "tui_history"), NoMouse: noMouse}

	// Project: walk up from cwd. A broken muster.json is reported, not fatal.
	if path, err := config.Find(cwd); err == nil {
		p, err := config.Load(path)
		if err != nil {
			return err
		}
		deps.Project = p
	}

	token, _, tokErr := auth.LoadToken()
	if bin, err := engine.Locate(); err == nil {
		deps.Engine = engine.New(bin, token, cwd)
	} else {
		deps.EngineErr = err
	}
	if tokErr != nil {
		deps.TokenMissing = true
		deps.CanBootstrap = !auth.MachineHasTokens()
	}
	if d, err := docker.Locate(); err == nil {
		deps.Docker = d
	}

	m := tui.New(deps)
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if !noMouse {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(m, opts...)
	final, err := p.Run()
	if fm, ok := final.(tui.Model); ok {
		fm.Shutdown()
	}
	return err
}
