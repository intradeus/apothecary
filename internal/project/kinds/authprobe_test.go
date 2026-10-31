package kinds

import (
	"apothecary/internal/account/providers"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGoGateway answers like opencode.ai/inference/go (observed live): the
// model is checked first, then the credential, then the body — and no
// /models. It fails the test if a probe ever carries a message to infer on.
func fakeGoGateway(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("x-opencode-session") == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Model    string            `json:"model"`
			Messages []json.RawMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) > 0 {
			t.Errorf("auth probe sent %d messages — it would spend tokens", len(body.Messages))
		}
		switch {
		case body.Model != "go-model-7":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"server_error","message":"Upstream request failed: Model is unavailable."}}`))
		case r.Header.Get("Authorization") != "Bearer good":
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":{"type":"server_error","message":"Upstream request failed: Invalid credential %s"}}`,
				strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"messages: must not be empty"}}`))
		}
	}))
}

func stubCatalog(t *testing.T, doc string) {
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(doc))
	}))
	oldURL := modelsCatalogURL
	modelsCatalogURL = catalog.URL
	modelsCache.Lock()
	modelsCache.catalog = nil
	modelsCache.Unlock()
	t.Cleanup(func() {
		catalog.Close()
		modelsCatalogURL = oldURL
		modelsCache.Lock()
		modelsCache.catalog = nil
		modelsCache.Unlock()
	})
}

// The run that prompted this: a revoked opencode-go key passed the model
// check (catalog fallback) and every stage then died on "Invalid credential".
func TestOpencodeCheckAuth(t *testing.T) {
	gw := fakeGoGateway(t)
	defer gw.Close()
	stubCatalog(t, fmt.Sprintf(`{"opencode-go":{"name":"OpenCode Go","api":%q,"models":{"go-model-7":{}}},
		"anthropic":{"name":"Anthropic","models":{}}}`, gw.URL))

	check := func(creds []providers.Credential, models ...string) AuthResult {
		h, _ := Opencode{}.New(withCreds(creds...), nil)
		return h.(AuthChecker).CheckAuth(context.Background(), models)
	}
	goKey := func(k string) []providers.Credential { return []providers.Credential{apiKey("opencode-go", k)} }

	if r := check(goKey("good"), "opencode-go/go-model-7"); len(r.Rejected)+len(r.Unverified) != 0 {
		t.Errorf("good key: %+v", r)
	}

	r := check(goKey("revoked-key"), "opencode-go/go-model-7")
	if len(r.Rejected) != 1 || !strings.Contains(r.Rejected[0], "Invalid credential") || !strings.Contains(r.Rejected[0], "opencode-go/go-model-7") {
		t.Fatalf("revoked key: %+v", r)
	}
	if strings.Contains(r.Rejected[0], "revoked-key") {
		t.Errorf("the key leaked into the verdict: %q", r.Rejected[0])
	}

	r = check(goKey("good"), "opencode-go/go-model-5")
	if len(r.Rejected) != 1 || !strings.Contains(r.Rejected[0], "Model is unavailable") {
		t.Errorf("retired model: %+v", r)
	}

	// A login needs no probe — the keeper proved its refresh token — and a
	// provider the flow does not use is not probed at all.
	r = check([]providers.Credential{login("openai-chatgpt", "a", nil), apiKey("opencode-go", "revoked")}, "openai/alpha-5")
	if len(r.Rejected) != 0 || len(r.Unverified) != 0 {
		t.Errorf("login: %+v", r)
	}
	// A credential the keeper gave up on is a refusal.
	h, _ := Opencode{}.New(Config{Credentials: Credentials{Problems: map[string]string{"c": `credential "ChatGPT" is disabled: invalid_grant`}}}, nil)
	if r := h.(AuthChecker).CheckAuth(context.Background(), []string{"openai/x"}); len(r.Rejected) != 1 || !strings.Contains(r.Rejected[0], "disabled") {
		t.Errorf("disabled: %+v", r)
	}
}

// The integration's Test button: no flow, so every auth.json provider is
// probed — the gateway with a model borrowed from the catalog.
func TestOpencodeCheckCredentials(t *testing.T) {
	gw := fakeGoGateway(t)
	defer gw.Close()
	check := func(catalog, key string) AuthResult {
		stubCatalog(t, catalog)
		h, _ := Opencode{}.New(withCreds(apiKey("opencode-go", key)), nil)
		return h.(AuthChecker).CheckCredentials(context.Background())
	}
	served := fmt.Sprintf(`{"opencode-go":{"name":"OpenCode Go","api":%q,"models":{"go-model-7":{}}}}`, gw.URL)

	if r := check(served, "good"); len(r.Rejected)+len(r.Unverified) != 0 {
		t.Errorf("good key: %+v", r)
	}
	r := check(served, "revoked-key")
	if len(r.Rejected) != 1 || !strings.Contains(r.Rejected[0], "opencode provider opencode-go") ||
		strings.Contains(r.Rejected[0], "revoked-key") {
		t.Errorf("revoked key: %+v", r)
	}

	// The borrowed model is retired: the gateway refuses it before looking
	// at the key, so the key is neither passed nor failed.
	retired := fmt.Sprintf(`{"opencode-go":{"name":"OpenCode Go","api":%q,"models":{"go-model-5":{}}}}`, gw.URL)
	r = check(retired, "revoked-key")
	if len(r.Rejected) != 0 || len(r.Unverified) != 1 || !strings.Contains(r.Unverified[0], "go-model-5") {
		t.Errorf("retired probe model: %+v", r)
	}
}

func TestJudgeAuthProbe(t *testing.T) {
	for _, c := range []struct {
		kind     probeKind
		status   int
		msg      string
		rejected bool
		err      bool
	}{
		{probeMetadata, 200, "", false, false},
		{probeMetadata, 401, "invalid x-api-key", true, false},
		{probeMetadata, 403, "", true, false},
		{probeMetadata, 402, "insufficient credits", true, false},
		{probeMetadata, 400, "", false, true}, // a GET refused for its form says nothing about the key
		{probeMetadata, 404, "", false, true},
		{probeMetadata, 503, "", false, true},
		{probeEmptyChat, 400, "messages: must not be empty", false, false},
		{probeEmptyChat, 400, "Upstream request failed: Model is unavailable.", true, false},
		{probeEmptyChat, 429, "slow down", false, true},
	} {
		rejected, _, err := judge(c.kind, c.status, c.msg)
		if rejected != c.rejected || (err != nil) != c.err {
			t.Errorf("judge(%d, %d, %q) = %v, %v", c.kind, c.status, c.msg, rejected, err)
		}
	}
}
