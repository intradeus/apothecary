// Package wsname is the naming policy for run workspaces and the branches
// derived from ticket titles. It is the one place that says what a run's
// container is called, so the provisioner, egress control, the log tail and
// the SSH door all agree on the name — even before the env reports ready.
package wsname

import (
	"strings"

	"apothecary/internal/run"
)

// ForRun returns the run's env id, or — while it is still provisioning
// (EnvReady not yet emitted) — the deterministic workspace name the provisioner
// will use. This lets egress control and the workspace-log tail work DURING the
// build, not only after the env is ready. Provisioners whose env id isn't this
// name simply see a name that doesn't match, degrading to their pre-ready state.
func ForRun(v run.View, runID string) string {
	if v.EnvID != "" {
		return v.EnvID
	}
	nameBranch := v.BranchBase
	if nameBranch == "" {
		nameBranch = v.Branch
	}
	return For(nameBranch, runID)
}

// For is the human-friendly workspace/container name for a run: an
// "apo-" marker, the slugified branch, and a run-id tail to keep two
// runs that share a branch (branches are free-text per ticket, so not unique)
// on distinct names — which keeps reuse-by-name a same-run operation. Capped
// at 32 chars; adapters use it verbatim.
func For(branch, runID string) string {
	suffix := RunIDTail(runID)
	s := Slug(branch, 32-len("apo-")-1-len(suffix)) // room for prefix + "-" + suffix
	parts := []string{"apo"}
	if s != "" {
		parts = append(parts, s)
	}
	if suffix != "" {
		parts = append(parts, suffix)
	}
	return strings.Join(parts, "-")
}

// tailLen is how much of a run id tells two runs of one ticket apart. A
// ULID's tail is random in 32 letters, so two runs on a base collide once in
// about a million; two characters did once in a thousand.
const tailLen = 4

// RunIDTail is the per-run suffix: the end of the lowercased run id. Shared
// by the branch name (base + "-" + tail) and the workspace name so both stay
// aligned and unique per run.
func RunIDTail(runID string) string {
	s := strings.ToLower(runID)
	if len(s) > tailLen {
		s = s[len(s)-tailLen:]
	}
	return s
}

// Slug lowercases s and keeps only [a-z0-9], collapsing every other run of
// characters into one dash, with no leading or trailing dash, stopping once
// maxLen bytes are written.
func Slug(s string, maxLen int) string {
	var b strings.Builder
	prevDash := true // suppress leading dash
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
		if b.Len() >= maxLen {
			break
		}
	}
	return strings.TrimRight(b.String(), "-")
}
