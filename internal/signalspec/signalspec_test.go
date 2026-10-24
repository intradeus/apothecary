package signalspec

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRegistryIntegrity: the registry is compiled-in data the flow parser
// validates against, so a malformed entry is a build-time bug.
func TestRegistryIntegrity(t *testing.T) {
	reg := Registry()
	if len(reg) == 0 {
		t.Fatal("empty signal registry")
	}
	seen := map[string]bool{}
	for _, sig := range reg {
		if sig.Slug == "" || strings.ContainsAny(sig.Slug, " /$") {
			t.Errorf("signal slug %q is not a plain identifier", sig.Slug)
		}
		if seen[sig.Slug] {
			t.Errorf("duplicate signal slug %q", sig.Slug)
		}
		seen[sig.Slug] = true
		if sig.Title == "" {
			t.Errorf("signal %q has no title (the editor's picker shows it)", sig.Slug)
		}
	}
	// Registry returns a copy: a caller sorting it must not corrupt the source.
	reg[0].Slug = "mutated"
	if Get("mutated") != nil {
		t.Error("Registry() leaked the backing array")
	}
}

func TestMRMerged(t *testing.T) {
	sig := Get(MRMerged)
	if sig == nil {
		t.Fatalf("%s is not registered", MRMerged)
	}
	if sig.Requires != RequiresRepository {
		t.Errorf("requires = %q, want %q", sig.Requires, RequiresRepository)
	}
	// Needs is what lets the engine say "you never opened an MR" instead of
	// waiting forever on something that can never happen.
	if sig.Needs != NeedsMR {
		t.Errorf("needs = %q, want %q", sig.Needs, NeedsMR)
	}
	if Get("nope") != nil {
		t.Error("Get returned an unregistered signal")
	}
	if !strings.Contains(Slugs(), MRMerged) {
		t.Errorf("Slugs() = %q", Slugs())
	}
	b, err := json.Marshal(sig)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"slug":"mr_merged"`, `"requires":"repository"`, `"title":`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("json missing %s: %s", want, b)
		}
	}
}

// Beginnings: run_start is how a run begins, manual a person's act; neither
// needs anything of the project nor is debounced. Every other signal is
// observed on the run's merge requests — what it does is the flow's call.
func TestKinds(t *testing.T) {
	rs := Get(RunStart)
	if rs == nil || rs.Kind != KindStart || rs.Observed() || rs.Requires != "" || rs.Needs != "" || rs.Debounced {
		t.Errorf("run_start = %+v", rs)
	}
	m := Get(Manual)
	if m == nil || m.Kind != KindHand || m.Observed() || m.Requires != "" || m.Needs != "" || m.Debounced {
		t.Errorf("manual = %+v", m)
	}
	for _, s := range Registry() {
		if s.Slug == RunStart || s.Slug == Manual {
			continue
		}
		if !s.Observed() || s.Requires != RequiresRepository || s.Needs != NeedsMR {
			t.Errorf("%s = %+v: an observed merge-request signal", s.Slug, s)
		}
	}
	if Get("mr_opened") != nil {
		t.Error("mr_opened is a run_start on a merge request, not a signal")
	}
}
