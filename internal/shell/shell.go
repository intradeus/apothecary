// Package shell holds the two string helpers every piece of code that builds
// a workspace command line needs: quoting a value for POSIX sh, and keeping
// the end of a long output. They live apart from the engine so every package
// that needs them, adapters included, shares one definition.
package shell

import "strings"

// Quote single-quotes s for POSIX sh: safe against $, backticks,
// spaces and embedded quotes.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Tail returns the last n bytes of s (all of s when it is shorter).
func Tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
