// Package livetest is how a live suite (a `//go:build live` test: a real
// cluster, real images, docker) says it cannot run here. Locally a skip is
// right: a workspace or a laptop may lack what the suite needs, and the
// skip says what. In CI it is not: CI's sandbox-test sets StrictEnv, and
// every skip fails instead, so a suite cannot go green by not running.
// internal/devtools/livetests holds every live suite to this package.
package livetest

import (
	"os"
	"testing"
)

// StrictEnv set to 1 turns every Skip into a failure.
const StrictEnv = "APOTHECARY_LIVE_STRICT"

// Strict reports whether StrictEnv is set.
func Strict() bool { return os.Getenv(StrictEnv) == "1" }

// Skip skips t with why, or fails it under StrictEnv.
func Skip(t testing.TB, format string, args ...any) {
	t.Helper()
	if Strict() {
		t.Fatalf(StrictEnv+"=1, so this fails instead of skipping: "+format, args...)
		return
	}
	t.Skipf(format, args...)
}
