package tui

import "os/exec"

// newExec builds an interactive command for tea.ExecProcess.
func newExec(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }
