package ref

import (
	"errors"
	"testing"
)

// Examples is every form of the grammar, shared with the UI's formatter
// (cpui/src/lib/domain/ref.test.ts holds the same list).
var examples = []struct {
	s string
	r Ref
}{
	{"review#5", Ref{Stage: "review", Attempt: 5}},
	{"stage-2#3", Ref{Stage: "stage-2", Attempt: 3}},
	{"fix_ci#12", Ref{Stage: "fix_ci", Attempt: 12}},
	{"settle.resolve#2", Ref{Stage: "settle.resolve", Attempt: 2}},
	{"decision#2", Ref{Stage: "decision", Attempt: 2}},
	{"run#1", Ref{Stage: "run", Attempt: 1}},
	{"build#3/decision#1", Ref{Stage: "build", Attempt: 3, Kind: Decision, N: 1}},
	{"build#3/note#2", Ref{Stage: "build", Attempt: 3, Kind: Note, N: 2}},
	{"run#1/decision#1", Ref{Stage: "run", Attempt: 1, Kind: Decision, N: 1}},
	{"run", Ref{}},
	{"run/decision#4", Ref{Kind: Decision, N: 4}},
}

// Every form round-trips: Parse reads what the formatters write.
func TestRoundTrip(t *testing.T) {
	for _, c := range examples {
		got, err := Parse(c.s)
		if err != nil || got != c.r {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", c.s, got, err, c.r)
		}
		if s := c.r.String(); s != c.s {
			t.Errorf("%+v.String() = %q, want %q", c.r, s, c.s)
		}
	}
	if Attempt("stage-2", 3) != "stage-2#3" || In("build#3", Note, 2) != "build#3/note#2" || In("", Decision, 4) != "run/decision#4" {
		t.Error("the formatters")
	}
	if r, _ := Parse("build#3/note#2"); r.AttemptRef() != "build#3" || r.IsRun() {
		t.Errorf("attempt of an entry = %q", r.AttemptRef())
	}
	if r, _ := Parse("run/decision#4"); r.AttemptRef() != "" || !r.IsRun() {
		t.Error("the run's entry names an attempt")
	}
}

// What the grammar does not produce is refused, with the grammar.
func TestParseRefuses(t *testing.T) {
	for _, s := range []string{
		"", "review", "review#", "review#0", "review#05", "review#-1", "review#1#2", "review-5",
		"#5", "Review#5", "2fix#1", "review#5/", "review#5/note", "review#5/note#0",
		"review#5/verdict#1", "review#5/note#1/x", "run/note#1", "run/decision", "run#",
		"decision#1/decision#1/x", "review#5 ", " review#5", "a..b#1", ".a#1",
	} {
		if _, err := Parse(s); !errors.Is(err, ErrMalformed) {
			t.Errorf("Parse(%q) = %v, want ErrMalformed", s, err)
		}
	}
}
