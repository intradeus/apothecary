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
	if sig.Requires != RequiresForge {
		t.Errorf("requires = %q, want %q", sig.Requires, RequiresForge)
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
	for _, want := range []string{`"slug":"mr_merged"`, `"requires":"forge"`, `"title":`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("json missing %s: %s", want, b)
		}
	}
}

// Manual is the one beginning a person gives: it may begin the run (the
// entry) and a reaction phase, needs nothing of the project, and never fires
// on its own — so it is not debounced. The forge triggers begin reactions
// only.
func TestManual(t *testing.T) {
	sig := Get(Manual)
	if sig == nil {
		t.Fatalf("%s is not registered", Manual)
	}
	if !sig.Entry || !sig.Trigger {
		t.Errorf("entry %v trigger %v, want both", sig.Entry, sig.Trigger)
	}
	if sig.Requires != "" || sig.Needs != "" || sig.Debounced {
		t.Errorf("manual needs nothing: %+v", sig)
	}
	for _, slug := range []string{MRConflicts, MRReviewSettled} {
		if s := Get(slug); s.Entry || !s.Trigger {
			t.Errorf("%s: entry %v trigger %v", slug, s.Entry, s.Trigger)
		}
	}
}
