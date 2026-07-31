package engine

import (
	"encoding/json"
	"strings"
)

// redactor scrubs the secrets the engine itself injected into a workspace out
// of anything that leaves it: a failing stage's output tail, the retry feed an
// agent reads, and the persisted stage log (PLAN5 M21).
//
// Value-based, not pattern-based, and deliberately so. The engine KNOWS every
// credential it wrote — the git token, the provider keys, the per-run bearer —
// so matching on those exact strings is precise: no regex guessing, no false
// positives mangling ordinary output, and it catches a token however the tool
// that leaked it chose to print it (a URL, a header, an error message).
//
// It cannot catch a secret the engine never supplied (one the repo's own
// tooling prints). Pattern-based scrubbing would be a second, independent
// layer; this one closes the hole the factory itself opens.
type redactor struct {
	// values are the literal secrets to remove, longest first so a token that
	// contains another (a URL embedding a password) is replaced whole rather
	// than leaving a mangled remainder.
	values []redactValue
}

type redactValue struct {
	secret string
	label  string // what replaces it: [redacted:<label>]
}

// minSecretLen guards against redacting something meaninglessly short. A
// one- or two-character "secret" would turn ordinary output into confetti,
// and anything that short is not a credential worth protecting.
const minSecretLen = 6

// newRedactor builds the scrubber for a run from its resolved configuration
// plus the run's bearer token. Empty and trivially short values are skipped.
func newRedactor(res *Resolution, tokens ...string) *redactor {
	r := &redactor{}
	if res != nil {
		r.add(res.GitPass, "git-token")
		for k, v := range res.ProviderEnv {
			r.add(v, strings.ToLower(k))
		}
		for _, s := range res.HarnessSecrets {
			// The blob itself (something cats auth.json) AND every string
			// inside it: what actually leaks is the key, not the wrapper.
			r.add(s, "harness-auth")
			for _, leaf := range jsonLeaves(s) {
				r.add(leaf, "harness-auth")
			}
		}
	}
	for _, t := range tokens {
		r.add(t, "run-token")
	}
	// Longest first: a value that contains another must be matched before its
	// substring, or the outer match would find only a partially-scrubbed string.
	for i := 1; i < len(r.values); i++ {
		for j := i; j > 0 && len(r.values[j].secret) > len(r.values[j-1].secret); j-- {
			r.values[j], r.values[j-1] = r.values[j-1], r.values[j]
		}
	}
	return r
}

// jsonLeaves returns every string value nested anywhere in a JSON document.
// A harness's auth.json is a vendor-shaped envelope — {"openrouter":{"type":
// "api","key":"sk-or-…"}} — and the credential is a leaf, so scrubbing only
// the whole blob would miss the one string a tool is actually likely to echo.
// Non-JSON input yields nothing (the blob is still redacted whole).
//
// Every long-enough leaf is treated as sensitive, including incidental ones
// like a base URL: over-redacting inside a secrets file is the right default,
// and minSecretLen already drops the noise ("api", "type").
func jsonLeaves(doc string) []string {
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(n any) {
		switch t := n.(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			// Keys are field names, not secrets; only values recurse.
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

func (r *redactor) add(secret, label string) {
	if len(secret) < minSecretLen {
		return
	}
	for _, v := range r.values {
		if v.secret == secret {
			return
		}
	}
	r.values = append(r.values, redactValue{secret: secret, label: label})
}

// scrub replaces every known secret in s. Safe on a nil receiver so callers
// need no guard.
func (r *redactor) scrub(s string) string {
	if r == nil || s == "" {
		return s
	}
	for _, v := range r.values {
		if strings.Contains(s, v.secret) {
			s = strings.ReplaceAll(s, v.secret, "[redacted:"+v.label+"]")
		}
	}
	return s
}
