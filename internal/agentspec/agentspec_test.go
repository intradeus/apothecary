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
