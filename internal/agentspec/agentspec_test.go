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
		{"ok", []InputDecl{{Name: "plan", Type: TypeFile}, {Name: "feedback", Type: TypeFailure, Optional: true}}, []OutputDecl{{Name: "p", Type: TypeFile}}, ""},
		{"schema alone", []InputDecl{{Name: "plan", Schema: "plan"}}, []OutputDecl{{Name: "p", Schema: "plan"}}, ""},
		{"schema narrows a structured type", nil, []OutputDecl{{Name: "v", Type: TypeVerdict, Schema: "strict-verdict"}}, ""},
		{"dup input", []InputDecl{{Name: "plan", Type: TypeFile}, {Name: "plan", Type: TypeFile}}, nil, "declared twice"},
		{"bad input name", []InputDecl{{Name: "Plan", Type: TypeFile}}, nil, "lowercase slug"},
		{"long input desc", []InputDecl{{Name: "plan", Type: TypeFile, Description: long}}, nil, "exceeds"},
		{"untyped input", []InputDecl{{Name: "plan"}}, nil, "type or schema is required"},
		{"unknown input type", []InputDecl{{Name: "plan", Type: "mood"}}, nil, "unknown type"},
		{"schema on a value input", []InputDecl{{Name: "t", Type: TypeTicket, Schema: "s"}}, nil, "a schema describes a file"},
		{"bad schema name", []InputDecl{{Name: "plan", Schema: "Plan"}}, nil, "lowercase slug"},
		{"dup output name", nil, []OutputDecl{{Name: "p", Type: TypeFile}, {Name: "p", Type: TypeFile}}, "declared twice"},
		{"bad output name", nil, []OutputDecl{{Name: "P", Type: TypeFile}}, "lowercase slug"},
		{"long output desc", nil, []OutputDecl{{Name: "p", Type: TypeFile, Description: long}}, "exceeds"},
		{"untyped output", nil, []OutputDecl{{Name: "p"}}, "type or schema is required"},
		{"output path", nil, []OutputDecl{{Name: "p", Type: TypeFile, LegacyPath: "artifacts/p.md"}}, "path is the stage's to say"},
		{"value output", nil, []OutputDecl{{Name: "t", Type: TypeTicket}}, "value the engine provides"},
		{"file beside a schema", nil, []OutputDecl{{Name: "p", Type: TypeFile, Schema: "plan"}}, "drop type"},
		{"file beside a schema on an input", []InputDecl{{Name: "p", Type: TypeFile, Schema: "plan"}}, nil, "drop type"},
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

// TestCheckShape: one port's type and schema, wherever it is declared.
func TestCheckShape(t *testing.T) {
	cases := []struct {
		name       string
		typ        string
		schema     string
		outputOnly bool
		want       string // "" = must pass
	}{
		{"file", TypeFile, "", false, ""},
		{"structured", TypeCommitMessage, "", true, ""},
		{"value input", TypeChangeset, "", false, ""},
		{"schema alone", "", "plan", true, ""},
		{"neither", "", "", false, "type or schema is required"},
		{"unknown", "mood", "", false, "unknown type"},
		{"value output", TypeLink, "", true, "value the engine provides"},
		{"schema on value", TypeFailure, "plan", false, "a schema describes a file"},
		{"file and schema", TypeFile, "plan", false, "drop type"},
		{"bad schema", "", "../x", false, "lowercase slug"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckShape(tc.typ, tc.schema, tc.outputOnly)
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
			{Name: "notes", Type: TypeFile},
			{Name: "verdict", Type: TypeVerdict, Description: "APPROVE or REJECT"},
		},
	}
	if err := Validate(spec); err != nil {
		t.Fatal(err)
	}
	v := spec.VerdictOutput()
	if v == nil || v.Name != "verdict" {
		t.Fatalf("VerdictOutput = %+v", v)
	}
	// An agent that renders no decision says so by having none.
	plain := &Spec{Name: "builder", Outputs: []OutputDecl{{Name: "notes", Type: TypeFile}}}
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
		{"unknown type", []OutputDecl{{Name: "o", Type: "mood"}}, "unknown type"},
		{"two verdicts", []OutputDecl{
			{Name: "a", Type: TypeVerdict},
			{Name: "b", Type: TypeVerdict},
		}, "at most one verdict"},
		{"two commit messages", []OutputDecl{
			{Name: "a", Type: TypeCommitMessage},
			{Name: "b", Type: TypeCommitMessage},
		}, "at most one commit_message"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := &Spec{Name: "x", Prompt: "p", Outputs: tc.outputs}
			if err := Validate(spec); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}

	// Many plain files are fine: only a structured type is one per agent.
	many := &Spec{Name: "x", Prompt: "p", Outputs: []OutputDecl{{Name: "a", Type: TypeFile}, {Name: "b", Type: TypeFile}}}
	if err := Validate(many); err != nil {
		t.Errorf("two file outputs: %v", err)
	}
}
