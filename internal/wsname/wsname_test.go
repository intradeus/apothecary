package wsname

import (
	"strings"
	"testing"
)

// For is the human-friendly workspace/container name policy: an
// apo- marker, the slugified branch, and a short run-id tail for uniqueness,
// all capped at 32 chars.
func TestWorkspaceName(t *testing.T) {
	// Short branch fits whole: exact, no truncation. Suffix is the run-id's
	// last 2 chars.
	if got := For("fix/login", "TESTRUNab"); got != "apo-fix-login-ab" {
		t.Errorf("short branch = %q, want apo-fix-login-ab", got)
	}

	// Empty or unsluggable branch ⇒ marker + run tail only (still valid,
	// still unique per run).
	for _, b := range []string{"", "///", "   ", "-"} {
		if got := For(b, "TESTRUNab"); got != "apo-ab" {
			t.Errorf("branch %q = %q, want apo-ab", b, got)
		}
	}

	// Long branch: capped at 32, keeps the apo- prefix and the -tail suffix,
	// with no doubled or trailing dashes at the truncation seam.
	long := For("fix/ticket-1234-fix-some-very-long-thing", "TESTRUNab")
	if len(long) > 32 {
		t.Errorf("len(%q) = %d, want ≤32", long, len(long))
	}
	if !strings.HasPrefix(long, "apo-fix-ticket-1234-fix") || !strings.HasSuffix(long, "-ab") {
		t.Errorf("long branch = %q", long)
	}
	if strings.Contains(long, "--") || strings.HasSuffix(strings.TrimSuffix(long, "-ab"), "-") {
		t.Errorf("malformed dashes in %q", long)
	}

	// Same branch, different runs ⇒ distinct names (the tail disambiguates),
	// so reuse-by-name can never cross runs.
	if a, b := For("main", "TESTRUNaa"), For("main", "TESTRUNbb"); a == b {
		t.Errorf("same branch, different runs collided: %q", a)
	}
}
