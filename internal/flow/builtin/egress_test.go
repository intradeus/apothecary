package builtin

import (
	"slices"
	"testing"
)

func TestEgressDefaults(t *testing.T) {
	got := EgressDefaults()
	if len(got) < 20 {
		t.Fatalf("egress defaults = %d entries, want a broad list", len(got))
	}
	// Comments and blanks are stripped; a few representative hosts survive.
	for _, want := range []string{"registry-1.docker.io", "pypi.org", "github.com", "*.docker.io", "download.docker.com"} {
		if !slices.Contains(got, want) {
			t.Errorf("egress defaults missing %q", want)
		}
	}
	// No comment lines or blanks leak through.
	for _, h := range got {
		if h == "" || h[0] == '#' {
			t.Errorf("egress defaults contains a non-host line %q", h)
		}
	}
	// Deduped.
	seen := map[string]bool{}
	for _, h := range got {
		if seen[h] {
			t.Errorf("egress defaults has duplicate %q", h)
		}
		seen[h] = true
	}
}
