//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// daemonCmd runs the daemon in a session of its own: no controlling
// terminal, so closing the terminal (its SIGHUP) does not reach it.
func daemonCmd(exe string) *exec.Cmd {
	cmd := exec.Command(exe, daemonCommand)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}
