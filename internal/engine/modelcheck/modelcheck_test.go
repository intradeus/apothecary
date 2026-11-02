package modelcheck

import (
	"context"
	"slices"
	"testing"

	"apothecary/internal/flow"
	"apothecary/internal/project/resolution"
)

func TestAgentAccepts(t *testing.T) {
	for _, c := range []struct {
		advertised []string
		model      string
		want       bool
	}{
		{[]string{"acme/alpha-2"}, "acme/alpha-2", true},
		{[]string{"alpha-codex/high", "alpha-codex/low"}, "acme/alpha-codex", true}, // codex: bare + effort
		{[]string{"alpha-5"}, "acme/alpha-5", true},
		{[]string{"acme/alpha-2"}, "acme/alpha-1", false},   // another version is not a match
		{[]string{"alpha-codex/high"}, "acme/alpha", false}, // a prefix is not a match
	} {
		if got := Accepts(c.advertised, c.model); got != c.want {
			t.Errorf("Accepts(%v, %q) = %v", c.advertised, c.model, got)
		}
	}
}

// A harness option naming a model is checked like the stage's own model:
// listed by the harness, and its credentials probed.
func TestOptionModelsAreChecked(t *testing.T) {
	fl, err := flow.Parse("t.yaml", []byte(`name: t
version: 5
agents:
  - { name: a, prompt: p }
main:
  - { id: s, type: agent, agent: a, harness: omp, model: acme/a, harness_options: { omp: { advisor: acme/nope } } }
`))
	if err != nil {
		t.Fatal(err)
	}
	var probed []string
	res := &resolution.Resolution{
		Models: func(_ context.Context, kind string) ([]string, error) { return []string{"acme/a"}, nil },
		CheckAuth: func(_ context.Context, kind string, models []string) ([]string, []string) {
			if kind == "omp" {
				probed = models
			}
			return nil, nil
		},
	}
	got := Models(context.Background(), res, fl)
	want := []string{`stage "s": harness_options.omp.advisor "acme/nope" is not available to harness omp`}
	if !slices.Equal(got.Missing, want) {
		t.Errorf("missing = %q, want %q", got.Missing, want)
	}
	Auth(context.Background(), res, fl.Stages)
	if !slices.Equal(probed, []string{"acme/a", "acme/nope"}) {
		t.Errorf("auth probed %v for omp", probed)
	}
}
