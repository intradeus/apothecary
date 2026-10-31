package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AuthChecker is an OPTIONAL Harness capability: prove, from the engine,
// that the providers accept the configured credentials — WITHOUT spending
// tokens (an authenticated metadata endpoint, or a request the provider
// rejects before any inference runs). Static Test() only proves the auth
// material parses; a revoked or mistyped key otherwise surfaces mid-run, as
// an agent that "ended its turn without any output".
//
// models are the flow's model ids (provider/model) this harness will run,
// so only the providers a run actually routes through are probed.
type AuthChecker interface {
	CheckAuth(ctx context.Context, models []string) AuthResult
	// CheckCredentials probes EVERY configured credential, with no flow in
	// hand (the integration's "Test" button). A model a gateway no longer
	// serves is not a credential verdict, so it is only unverified.
	CheckCredentials(ctx context.Context) AuthResult
}

// AuthResult is what probing a harness's credentials found.
type AuthResult struct {
	// Rejected: the provider refused the credential (or the model) — a run
	// would fail when the stage opens its session.
	Rejected []string
	// Unverified: no zero-token probe exists for the provider, or it could
	// not be reached. Not a verdict on the credential.
	Unverified []string
}

var (
	_ AuthChecker = (*opencodeHarness)(nil)
	_ AuthChecker = (*codexHarness)(nil)
	_ AuthChecker = (*ompHarness)(nil)
)

// authProbe is one zero-token request that only an accepted credential gets
// past. kind picks how its status is read (see judge).
type authProbe struct {
	kind   probeKind
	method string
	url    string
	header map[string]string
	body   string
}

type probeKind int

const (
	// probeMetadata: an authenticated GET (a model list, a key-info
	// endpoint). 2xx proves the credential.
	probeMetadata probeKind = iota
	// probeEmptyChat: a chat completion with no messages. Gateways check the
	// model and the credential before validating the body, so a 4xx that is
	// not about auth or the model proves the credential — and no inference
	// ran. max_tokens 1 bounds the cost should a provider ever accept it.
	probeEmptyChat
)

// openaiModelsProbe is the OpenAI API's authenticated model list.
func openaiModelsProbe(key string) authProbe {
	return authProbe{kind: probeMetadata, method: http.MethodGet,
		url:    "https://api.openai.com/v1/models",
		header: map[string]string{"Authorization": "Bearer " + key}}
}

// providerAuthProbe returns the probe for an api-key credential of a
// models.dev provider, ok=false when there is none worth trusting. model is
// the bare model id the flow routes through it.
func providerAuthProbe(providerID string, p catalogProvider, key, model string) (authProbe, bool) {
	bearer := map[string]string{"Authorization": "Bearer " + key}
	switch providerID {
	case "anthropic":
		return authProbe{kind: probeMetadata, method: http.MethodGet,
			url:    "https://api.anthropic.com/v1/models?limit=1",
			header: map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01"}}, true
	case "openai":
		return openaiModelsProbe(key), true
	case "openrouter":
		// Its /models is public — it answers a bogus key with 200. /key
		// describes the calling key and needs a valid one.
		return authProbe{kind: probeMetadata, method: http.MethodGet,
			url: "https://openrouter.ai/api/v1/key", header: bearer}, true
	case "opencode", "opencode-go":
		// OpenCode's gateways serve no /models (404). An empty chat is
		// refused with 401 "Invalid credential" for a bad key and 400
		// "Model is unavailable" for a retired model, both before inference.
		if p.API == "" {
			return authProbe{}, false
		}
		h := map[string]string{
			"Authorization":      "Bearer " + key,
			"Content-Type":       "application/json",
			"x-opencode-session": "apothecary-authcheck",
		}
		body, _ := json.Marshal(map[string]any{"model": model, "messages": []any{}, "max_tokens": 1})
		return authProbe{kind: probeEmptyChat, method: http.MethodPost,
			url: strings.TrimRight(p.API, "/") + "/chat/completions", header: h, body: string(body)}, true
	}
	// Other OpenAI-compatible providers: their model list is authenticated
	// (groq, mistral, xai, deepseek, …). A public one would read as a pass —
	// add it above with a key-specific endpoint, like openrouter.
	if p.API == "" {
		return authProbe{}, false
	}
	return authProbe{kind: probeMetadata, method: http.MethodGet,
		url: strings.TrimRight(p.API, "/") + "/models", header: bearer}, true
}

// authProbeTimeout bounds one probe; the callers bound the whole check.
const authProbeTimeout = 10 * time.Second

// run sends the probe and reads the verdict: rejected=true with a reason
// when the provider refused, err when the probe said nothing either way.
// The key never appears in either: provider error bodies may echo it.
func (pr authProbe) run(ctx context.Context, key string) (rejected bool, reason string, err error) {
	ctx, cancel := context.WithTimeout(ctx, authProbeTimeout)
	defer cancel()
	var body io.Reader
	if pr.body != "" {
		body = strings.NewReader(pr.body)
	}
	req, err := http.NewRequestWithContext(ctx, pr.method, pr.url, body)
	if err != nil {
		return false, "", err
	}
	for k, v := range pr.header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, "", scrubKey(err, key)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return judge(pr.kind, resp.StatusCode, strings.ReplaceAll(errorMessage(raw), key, "***"))
}

// judge maps a probe's status to a verdict.
func judge(kind probeKind, status int, msg string) (bool, string, error) {
	detail := fmt.Sprintf("%d", status)
	if msg != "" {
		detail += " " + msg
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return true, "credential rejected (" + detail + ")", nil
	case status == http.StatusPaymentRequired:
		return true, "no credit left (" + detail + ")", nil
	case status >= 200 && status < 300:
		return false, "", nil
	}
	if kind == probeEmptyChat && (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity) {
		if strings.Contains(strings.ToLower(msg), "model") && strings.Contains(strings.ToLower(msg), "unavailable") {
			return true, "model refused (" + detail + ")", nil
		}
		return false, "", nil // the body was refused, after the credential passed
	}
	return false, "", fmt.Errorf("inconclusive (%s)", detail)
}

// errorMessage pulls the usual {"error":{"message":…}} (or {"error":"…"})
// out of a provider error body, else a trimmed prefix of it.
func errorMessage(raw []byte) string {
	var b struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &b) == nil && len(b.Error) > 0 {
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(b.Error, &e) == nil && e.Message != "" {
			return e.Message
		}
		var s string
		if json.Unmarshal(b.Error, &s) == nil && s != "" {
			return s
		}
	}
	return clip(strings.TrimSpace(string(raw)), 200)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// scrubKey keeps a credential out of a transport error (url.Error quotes the
// URL, and a provider could take its key in the query).
func scrubKey(err error, key string) error {
	if key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), key, "***"))
}
