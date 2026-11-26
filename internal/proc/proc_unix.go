//go:build !windows

package proc

import (
	"os/exec"
	"syscall"
	"time"
)

// killGrace is how long a cancelled process group gets after SIGTERM before
// SIGKILL.
var killGrace = 1500 * time.Millisecond

func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pid := cmd.Process.Pid
		// Negative pid = the whole process group (hook scripts and the
		// docker/kubectl processes they started).
		if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
			return cmd.Process.Kill()
		}
		// A child forked while the signal was in flight can miss it and keep
		// our output pipe open; make sure the group is gone.
		go func() {
			time.Sleep(killGrace)
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}()
		return nil
	}
}
