package cli

import (
	"os/exec"
	"runtime"
	"strings"
)

type Cli interface {
	Check() bool
	Execute(command ...string)
	Script(detached bool, filepath string, args ...string)
}

// GetShell returns a shell based on the string passed in (bash|powershell|cmd). If string is empty or invalid, it will return the default shell for the OS.
func GetShell(shell string, debug bool) Cli {
	var (
		shellMap = map[string]Cli{
			"bash":       &Bash{Debug: debug},
			"powershell": &Powershell{Debug: debug},
			"cmd":        &WindowsCMD{Debug: debug},
		}
	)

	selectedShell, ok := shellMap[strings.ToLower(shell)]

	// If no shell is specified, we default to CMD for windows and bash for linux/osx.
	if !ok {
		if runtime.GOOS == "windows" {
			selectedShell = &WindowsCMD{Debug: debug}
		} else {
			selectedShell = &Bash{Debug: debug}
		}
	}

	// Validate that the shell is available on the system.
	checked := selectedShell.Check()
	if checked {
		return selectedShell
	}
	return nil
}

// Where checks if a program is available on the system.
func Where(program string) bool {
	_, err := exec.LookPath(program)
	if err != nil {
		return false
	}
	return true
}
