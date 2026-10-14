//go:build !linux

package main

import (
	"errors"
	"os"
	"os/exec"
)

// The shim only ever runs inside Linux workspaces; this stub keeps the package
// building (and its other tests running) on a developer's machine.
func startPTY(cmd *exec.Cmd, cols, rows int) (*os.File, error) {
	return nil, errors.New("terminals are only supported on linux")
}

func resizePTY(master *os.File, cols, rows int) error { return nil }
