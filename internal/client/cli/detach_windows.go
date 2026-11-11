//go:build windows

package cli

import (
	"os/exec"
	"syscall"
)

// daemonCmd runs the daemon detached from the console, in a process group
// of its own: closing the terminal window does not end it.
func daemonCmd(exe string) *exec.Cmd {
	const detachedProcess, newProcessGroup = 0x00000008, 0x00000200
	cmd := exec.Command(exe, daemonCommand)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | newProcessGroup, HideWindow: true}
	return cmd
}
