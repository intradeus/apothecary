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

// opencode's model keys never pass through provider.env — they live inside the
// auth.json blob — so the redactor has to reach INTO it. What a tool echoes is
// the key itself, not the envelope, so both must be scrubbed.
func TestRedactorScrubsHarnessAuthJSON(t *testing.T) {
	const authJSON = `{"openrouter":{"type":"api","key":"sk-or-v1-abcdefghijklmnop"},` +
		`"anthropic":{"type":"oauth","refresh":"rt-zyxwvutsrqponml"}}`
	r := newRedactor(&Resolution{HarnessSecrets: []string{authJSON}})

	out := r.scrub("opencode: 401 from provider using key sk-or-v1-abcdefghijklmnop\n" +
		"refresh token rt-zyxwvutsrqponml rejected\n")
	for _, secret := range []string{"sk-or-v1-abcdefghijklmnop", "rt-zyxwvutsrqponml"} {
		if strings.Contains(out, secret) {
			t.Errorf("auth.json leaf %q survived:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "[redacted:harness-auth]") {
		t.Errorf("missing redaction label:\n%s", out)
	}
	// The whole blob too (a `cat ~/.local/share/opencode/auth.json`).
	if got := r.scrub("contents: " + authJSON); strings.Contains(got, "sk-or-v1") {
		t.Errorf("whole blob survived:\n%s", got)
	}
	// Short structural values ("api", "type") must not become confetti.
	if got := r.scrub("the api type is oauth"); got != "the api type is oauth" {
		t.Errorf("structural JSON values were redacted: %q", got)
	}
}

// A non-JSON secret is still scrubbed whole — jsonLeaves just adds nothing.
func TestRedactorHandlesNonJSONHarnessSecret(t *testing.T) {
	r := newRedactor(&Resolution{HarnessSecrets: []string{"plain-secret-value"}})
	if got := r.scrub("token plain-secret-value here"); strings.Contains(got, "plain-secret-value") {
		t.Errorf("non-JSON secret survived: %q", got)
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
