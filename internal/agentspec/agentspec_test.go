package agentspec

import (
	"strings"
	"testing"
)

func TestParseReviewer(t *testing.T) {
	doc := `name: reviewer
description: adversarial code reviewer
read_only: true
allow_bash: true
prompt: |
  You are an adversarial code reviewer for this repository.
`
	s, err := Parse("reviewer", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if !s.ReadOnly || !s.AllowBash || s.Description == "" || !strings.Contains(s.Prompt, "adversarial") {
		t.Errorf("spec = %+v", s)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct{ name, stem, doc, want string }{
		{"stem mismatch", "builder", "name: planner\nprompt: p\n", "filename stem"},
		{"missing name", "x", "prompt: p\n", "name is required"},
		{"missing prompt", "x", "name: x\n", "prompt is required"},
		{"unknown field", "x", "name: x\nprompt: p\ntools: [bash]\n", "tools"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.stem, []byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestParseWithDecls(t *testing.T) {
	doc := `name: builder
prompt: |
  build it
inputs:
  - { name: plan, description: the plan }
  - { name: feedback, optional: true }
outputs:
  - { name: patch, description: the change, path: artifacts/patch.md }
`
	s, err := Parse("builder", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Inputs) != 2 || s.Inputs[0].Name != "plan" || !s.Inputs[1].Optional {
		t.Errorf("inputs = %+v", s.Inputs)
	}
	if len(s.Outputs) != 1 || s.Outputs[0].Path != "artifacts/patch.md" {
		t.Errorf("outputs = %+v", s.Outputs)
	}
}

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
		{"feedback not optional", []InputDecl{{Name: "feedback"}}, nil, "must be optional"},
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
