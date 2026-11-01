//go:build coverflush

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// Coverage reaches GOCOVERDIR on a clean exit, and a test stops a long-lived
// shim with a signal. Only `task cover:go` builds this in: production has no
// such signal.
func init() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGUSR2)
	go func() {
		<-stop
		os.Exit(0)
	}()
}
