package engine

import (
	"strings"
	"testing"
)

// The failure channel carries raw command output into events, prompts, gates
// and the on-disk stage log — so a token the engine itself injected must not
// survive the trip (PLAN5 M21).
func TestRedactorScrubsInjectedSecrets(t *testing.T) {
	res := &Resolution{
		GitPass:     "glpat-SUPERSECRETVALUE",
		ProviderEnv: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-abcdefghij"},
	}
	r := newRedactor(res, "run-token-abcdef")

	out := r.scrub("fatal: authentication failed for " +
		"https://oauth2:glpat-SUPERSECRETVALUE@forge.local/g/p.git\n" +
		"env ANTHROPIC_API_KEY=sk-ant-abcdefghij\nAuthorization: Bearer run-token-abcdef\n")

	for _, secret := range []string{"glpat-SUPERSECRETVALUE", "sk-ant-abcdefghij", "run-token-abcdef"} {
		if strings.Contains(out, secret) {
			t.Errorf("output still carries %q:\n%s", secret, out)
		}
	}
	for _, label := range []string{"[redacted:git-token]", "[redacted:anthropic_api_key]", "[redacted:run-token]"} {
		if !strings.Contains(out, label) {
			t.Errorf("output missing %s:\n%s", label, out)
		}
	}
	// Ordinary text is untouched: a scrubber that mangles normal output would
	// make failures unreadable.
	if !strings.Contains(out, "fatal: authentication failed for") {
		t.Errorf("scrub damaged ordinary output:\n%s", out)
	}
}

// A short value is not a credential worth protecting, and redacting it would
// shred ordinary output.
func TestRedactorIgnoresTrivialValues(t *testing.T) {
	r := newRedactor(&Resolution{ProviderEnv: map[string]string{"X": "ab"}}, "")
	if got := r.scrub("a rabbit ate ab"); got != "a rabbit ate ab" {
		t.Errorf("scrub = %q, want it untouched", got)
	}
}

// A nil redactor is safe: callers should need no guard.
func TestNilRedactorPassesThrough(t *testing.T) {
	var r *redactor
	if got := r.scrub("hello"); got != "hello" {
		t.Errorf("scrub = %q", got)
	}
}
