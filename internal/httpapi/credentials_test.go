package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/providers"
	"apothecary/internal/authsink"
	"apothecary/internal/confstore"
	"apothecary/internal/engine"
	"apothecary/internal/run"
)

// Credentials are their owner's: another user can neither read, test,
// rename nor delete one, and no response ever carries a key or a token.
func TestCredentialsAPIIsOwnerScopedAndNeverEchoes(t *testing.T) {
	x := newConfEnv(t)
	ctx := context.Background()

	resp, data := x.do(t, "POST", "/api/credentials", map[string]any{"provider": "openrouter", "key": "sk-or-SECRET-KEY"}, nil)
	if resp.StatusCode != http.StatusCreated || strings.Contains(string(data), "sk-or-SECRET-KEY") {
		t.Fatalf("create = %d: %s", resp.StatusCode, data)
	}
	var pc confstore.ProviderCredential
	_ = json.Unmarshal(data, &pc)
	if pc.Provider != "openrouter" || pc.Kind != providers.KindAPIKey || pc.UserID != testUser {
		t.Errorf("created = %+v", pc)
	}
	if resp, _ := x.do(t, "POST", "/api/credentials", map[string]any{"provider": "bespoke", "key": "k"}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an unmanaged provider = %d", resp.StatusCode)
	}

	// Import keeps what the table knows and lists what it refused.
	resp, data = x.do(t, "POST", "/api/credentials/import", map[string]any{"text": `{"openai":{"type":"api","key":"sk-IMPORTED"},"bespoke":{"type":"api","key":"k"}}`}, nil)
	var imp CredentialImportResult
	_ = json.Unmarshal(data, &imp)
	if resp.StatusCode != http.StatusCreated || len(imp.Created) != 1 || len(imp.Rejected) != 1 || strings.Contains(string(data), "sk-IMPORTED") {
		t.Errorf("import = %d: %s", resp.StatusCode, data)
	}

	resp, data = x.do(t, "GET", "/api/credentials", nil, nil)
	if resp.StatusCode != 200 || strings.Contains(string(data), "sk-or-SECRET-KEY") || strings.Contains(string(data), "sk-IMPORTED") {
		t.Errorf("list = %d: %s", resp.StatusCode, data)
	}

	// Another person sees none of it.
	if err := x.users.CreateUser(ctx, auth.User{ID: "u2", Handle: "bob", Email: "bob@example.test", Name: "Bob",
		CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	bob, _, err := x.srv.Auth.NewToken(ctx, "u2", "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	asBob := map[string]string{"Authorization": "Bearer " + bob}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/credentials/" + pc.ID}, {"PUT", "/api/credentials/" + pc.ID},
		{"DELETE", "/api/credentials/" + pc.ID}, {"POST", "/api/credentials/" + pc.ID + "/test"},
	} {
		resp, _ := x.do(t, c.method, c.path, map[string]any{"name": "mine now"}, asBob)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("bob %s %s = %d, want 404", c.method, c.path, resp.StatusCode)
		}
	}
	if resp, data := x.do(t, "GET", "/api/credentials", nil, asBob); resp.StatusCode != 200 || strings.TrimSpace(string(data)) != "[]" {
		t.Errorf("bob's list = %d: %s", resp.StatusCode, data)
	}

	// Without the sign-in runner wired, signing in says so.
	if resp, _ := x.do(t, "POST", "/api/credentials/signin", map[string]any{"provider": "anthropic"}, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("sign-in start unwired = %d", resp.StatusCode)
	}

	// The providers list is the closed set, narrowed per harness.
	resp, data = x.do(t, "GET", "/api/providers?harness=codex", nil, nil)
	var ps []ProviderInfo
	_ = json.Unmarshal(data, &ps)
	ids := []string{}
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	if resp.StatusCode != 200 || strings.Join(ids, ",") != "openai,openai-chatgpt" {
		t.Errorf("codex providers = %v", ids)
	}
}

// A run's auth broker answers only to that run's broker token: no token, a
// user's token, another run's broker token or the shim's run token are all
// refused before anything is served.
func TestAuthBrokerTakesOnlyItsRunsToken(t *testing.T) {
	var sink *authsink.Sink
	x := newConfEnvWith(t, func(d *engine.Deps) {
		sink = authsink.New(&run.Reader{Store: d.Store}, d.Resolver, []byte("instance-key"), nil)
		d.AuthSink = sink
	})
	path := "/api/runs/run-a/auth-broker/v1/snapshot"
	for name, hdr := range map[string]map[string]string{
		"none":          {"Authorization": ""},
		"another run's": {"Authorization": "Bearer " + sink.BrokerToken("run-b")},
		"garbage":       {"Authorization": "Bearer nope"},
	} {
		req, _ := http.NewRequest(http.MethodGet, x.ts.URL+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s token = %d, want 401", name, resp.StatusCode)
		}
	}
	// Its own token gets past the guard — to a run that does not exist here.
	req, _ := http.NewRequest(http.MethodGet, x.ts.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+sink.BrokerToken("run-a"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("own token = %d, want past the guard (404: no such run)", resp.StatusCode)
	}
}
