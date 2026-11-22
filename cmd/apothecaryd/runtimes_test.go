package main

import (
	"slices"
	"testing"

	"apothecary/internal/platform/config"
	"apothecary/internal/ports/envport"
)

// The boot accepts exactly the runtimes the port knows: config may not import
// the port, so this holds its copy of the names to it.
func TestBootRuntimesAreThePorts(t *testing.T) {
	var port []string
	for _, r := range envport.Runtimes() {
		port = append(port, string(r))
	}
	boot := slices.Clone(config.SandboxRuntimes)
	slices.Sort(boot)
	if !slices.Equal(boot, port) {
		t.Errorf("config.SandboxRuntimes = %v, envport.Runtimes() = %v", config.SandboxRuntimes, port)
	}
}
