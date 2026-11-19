//go:build !linux

package main

import (
	"errors"
	"os"
)

// writeUnder is Linux's: the assistant pod's sidecar runs nowhere else.
func writeUnder(string, string, []byte, os.FileMode) error {
	return errors.New("writes are Linux-only")
}
