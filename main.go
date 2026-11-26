// muster-tui is the interactive terminal UI for muster. The bash `muster` CLI
// stays the engine and the headless interface (`muster <cmd> --json`).
package main

import (
	"fmt"
	"os"
	"path/filepath"

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
      --set-token <t>  save a MUSTER_TOKEN for the CLI's --json commands
  -v, --version        print the version
  -h, --help           show this help

Inside the TUI type commands at the prompt (deploy, logs api, docker, help),
press / for completions, Ctrl+K for every action, ? for keys.
`

func main() {
	dir := ""
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
	if err := run(dir); err != nil {
		fmt.Fprintf(os.Stderr, "muster-tui: %v\n", err)
		os.Exit(1)
	}
}

func run(dir string) error {
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

	deps := tui.Deps{Cwd: cwd, Version: version, HistoryPath: filepath.Join(config.MusterHome(), "tui_history")}

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
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	final, err := p.Run()
	if fm, ok := final.(tui.Model); ok {
		fm.Shutdown()
	}
	return err
}
