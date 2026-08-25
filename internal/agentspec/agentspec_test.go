package agentspec

import (
	"strings"
	"testing"
)

func TestValidateIO(t *testing.T) {
	long := strings.Repeat("x", descMax+1)
	cases := []struct {
		name    string
		inputs  []InputDecl
		outputs []OutputDecl
		want    string // "" = must pass
	}{
		{"ok", []InputDecl{{Name: "plan"}, {Name: "feedback", Optional: true}}, []OutputDecl{{Name: "p", Path: "artifacts/p.md"}}, ""},
		{"dup input", []InputDecl{{Name: "plan"}, {Name: "plan"}}, nil, "declared twice"},
		{"bad input name", []InputDecl{{Name: "Plan"}}, nil, "lowercase slug"},
		{"long input desc", []InputDecl{{Name: "plan", Description: long}}, nil, "exceeds"},
		{"dup output name", nil, []OutputDecl{{Name: "p", Path: "a"}, {Name: "p", Path: "b"}}, "declared twice"},
		{"bad output path abs", nil, []OutputDecl{{Name: "p", Path: "/etc/x"}}, "artifact path"},
		{"bad output path dotdot", nil, []OutputDecl{{Name: "p", Path: "../x"}}, "artifact path"},
		{"dup output path", nil, []OutputDecl{{Name: "a", Path: "x.md"}, {Name: "b", Path: "x.md"}}, "path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIO(tc.inputs, tc.outputs)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

// TestVerdictOutputDecl: a verdict output is what makes a flow stage's
// on_reject reachable, so the spec must model exactly one of them.
func TestVerdictOutputDecl(t *testing.T) {
	spec := &Spec{
		Name: "reviewer", Prompt: "review it",
		Outputs: []OutputDecl{
			{Name: "notes", Path: "artifacts/notes.md"},
			{Name: "verdict", Type: OutputTypeVerdict, Path: "artifacts/verdict.json", Description: "APPROVE or REJECT"},
		},
	}
	if err := Validate(spec); err != nil {
		t.Fatal(err)
	}
	v := spec.VerdictOutput()
	if v == nil || v.Name != "verdict" || v.Path != "artifacts/verdict.json" {
		t.Fatalf("VerdictOutput = %+v", v)
	}
	// An agent that renders no decision says so by having none.
	plain := &Spec{Name: "builder", Outputs: []OutputDecl{{Name: "notes", Path: "n.md"}}}
	if plain.VerdictOutput() != nil {
		t.Error("a file output must not read as a verdict")
	}
	if (*Spec)(nil).VerdictOutput() != nil {
		t.Error("nil spec must be safe")
	}

	cases := []struct {
		name    string
		outputs []OutputDecl
		want    string
	}{
		{"unknown type", []OutputDecl{{Name: "o", Type: "mood", Path: "o.md"}}, "unknown type"},
		{"two verdicts", []OutputDecl{
			{Name: "a", Type: OutputTypeVerdict, Path: "a.json"},
			{Name: "b", Type: OutputTypeVerdict, Path: "b.json"},
		}, "at most one verdict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := &Spec{Name: "x", Prompt: "p", Outputs: tc.outputs}
			if err := Validate(spec); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}
