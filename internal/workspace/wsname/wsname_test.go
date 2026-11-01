package wsname

import (
	"strings"
	"testing"

	"apothecary/internal/run"
)

// For is the human-friendly workspace/container name policy: an
// apo- marker, the slugified branch, and a short run-id tail for uniqueness,
// all capped at 32 chars.
func TestWorkspaceName(t *testing.T) {
	// Short branch fits whole: exact, no truncation. Suffix is the run-id's
	// last 4 chars.
	if got := For("fix/login", "TESTRUNabcd"); got != "apo-fix-login-abcd" {
		t.Errorf("short branch = %q, want apo-fix-login-ab", got)
	}

	// Empty or unsluggable branch ⇒ marker + run tail only (still valid,
	// still unique per run).
	for _, b := range []string{"", "///", "   ", "-"} {
		if got := For(b, "TESTRUNabcd"); got != "apo-abcd" {
			t.Errorf("branch %q = %q, want apo-ab", b, got)
		}
	}

	// Long branch: capped at 32, keeps the apo- prefix and the -tail suffix,
	// with no doubled or trailing dashes at the truncation seam.
	long := For("fix/ticket-1234-fix-some-very-long-thing", "TESTRUNabcd")
	if len(long) > 32 {
		t.Errorf("len(%q) = %d, want ≤32", long, len(long))
	}
	if !strings.HasPrefix(long, "apo-fix-ticket-1234-fix") || !strings.HasSuffix(long, "-abcd") {
		t.Errorf("long branch = %q", long)
	}
	if strings.Contains(long, "--") || strings.HasSuffix(strings.TrimSuffix(long, "-abcd"), "-") {
		t.Errorf("malformed dashes in %q", long)
	}

	// Same branch, different runs ⇒ distinct names (the tail disambiguates),
	// so reuse-by-name can never cross runs.
	if a, b := For("main", "TESTRUNaa"), For("main", "TESTRUNbb"); a == b {
		t.Errorf("same branch, different runs collided: %q", a)
	}
}

func TestForRun(t *testing.T) {
	// A ready env: its id is used verbatim, whatever the branch.
	if got := ForRun(run.View{EnvID: "env-abc", Branch: "feat/x"}, "RUN1"); got != "env-abc" {
		t.Errorf("ready env = %q", got)
	}
	// Still provisioning: the deterministic name from BranchBase.
	if got := ForRun(run.View{BranchBase: "feat/login", Branch: "feat/login-xy"}, "TESTRUNabcd"); got != "apo-feat-login-abcd" {
		t.Errorf("base = %q", got)
	}
	// No BranchBase: falls back to Branch.
	if got := ForRun(run.View{Branch: "hotfix/y"}, "TESTRUNabcd"); got != "apo-hotfix-y-abcd" {
		t.Errorf("branch fallback = %q", got)
	}
}

func TestRunIDTail(t *testing.T) {
	if got := RunIDTail("ABCDEFGH"); got != "efgh" {
		t.Errorf("tail = %q", got)
	}
	if got := RunIDTail("XY"); got != "xy" {
		t.Errorf("short id = %q", got)
	}
	if got := RunIDTail(""); got != "" {
		t.Errorf("empty = %q", got)
	}
}

func TestSlug(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"Feat/Login Page", 40, "feat-login-page"},
		{"  --weird__name--  ", 40, "weird-name"},
		{"ALLCAPS", 40, "allcaps"},
		{"a/b/c/d", 3, "a-b"},
		{"...", 40, ""},
	} {
		if got := Slug(c.in, c.max); got != c.want {
			t.Errorf("Slug(%q,%d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}
