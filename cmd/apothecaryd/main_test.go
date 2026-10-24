package main

import (
	"testing"

	"apothecary/internal/flow"
)

// The -dev sandbox flow is seeded on every first dev boot: it must parse in
// the grammar this build speaks, or `serve -dev` refuses to start.
func TestDevFlowParses(t *testing.T) {
	f, err := flow.Parse("dev", []byte(devFlowYAML))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range f.Warnings {
		t.Errorf("warning: %s", w)
	}
}
