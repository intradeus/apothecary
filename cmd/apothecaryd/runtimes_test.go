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

// The daemon-door warning follows the runtimes' trait: any runtime the
// deployment offers whose sandbox root is node root, the default or listed.
func TestUncontainedRuntime(t *testing.T) {
	for _, c := range []struct {
		def    string
		listed []string
		want   envport.Runtime
	}{
		{"", nil, envport.RuntimePrivileged},
		{"privileged", nil, envport.RuntimePrivileged},
		{"sysbox", nil, ""},
		{"kata", []string{"kata", "sysbox"}, ""},
		{"sysbox", []string{"sysbox", "privileged"}, envport.RuntimePrivileged},
	} {
		if got := uncontainedRuntime(c.def, c.listed); got != c.want {
			t.Errorf("%q %v = %q, want %q", c.def, c.listed, got, c.want)
		}
	}
}
