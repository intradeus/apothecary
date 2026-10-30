package modelcheck

import "testing"

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
