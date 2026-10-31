package kinds

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"apothecary/internal/account/providers"
)

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The snapshot and refresh answers match omp 18.13.1's strict wire schemas
// (packages/ai/src/auth-broker/wire-schemas.ts): exactly these keys, the
// refresh sentinel in place of every refresh token, and a login's tokens
// registered with the run's redactor before they are served.
func TestOmpBrokerWire(t *testing.T) {
	h, _ := Omp{}.New(withCreds(
		apiKey("anthropic", "sk-ant"),
		login("openai-chatgpt", "chatgpt-acc", map[string]string{"account_id": "acct-1", "email": "me@x"}),
		login("github-copilot", "gho_tok", nil),
	), nil)
	var registered []string
	srv := httptest.NewServer(h.(AuthBrokered).Broker(func(s string) { registered = append(registered, s) }))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&raw)
	resp.Body.Close()
	if got := keysOf(raw); !slices.Equal(got, []string{"credentials", "generatedAt", "generation", "refresher", "serverNowMs"}) {
		t.Fatalf("snapshot keys = %v", got)
	}
	if got := keysOf(raw["refresher"].(map[string]any)); !slices.Equal(got, []string{"enabled", "intervalMs", "nextSweepInMs", "skewMs"}) {
		t.Errorf("refresher keys = %v", got)
	}
	creds := raw["credentials"].([]any)
	if len(creds) != 2 {
		t.Fatalf("snapshot holds %d entries, want the two logins (keys ride provider.env)", len(creds))
	}
	providersSeen := map[string]map[string]any{}
	for _, e := range creds {
		entry := e.(map[string]any)
		if got := keysOf(entry); !slices.Equal(got, []string{"credential", "id", "identityKey", "provider", "rotatesInMs"}) {
			t.Errorf("entry keys = %v", got)
		}
		c := entry["credential"].(map[string]any)
		if c["type"] != "oauth" || c["refresh"] != remoteRefreshSentinel || c["access"] == "" || c["expires"].(float64) <= 0 {
			t.Errorf("credential = %v", c)
		}
		for k := range c {
			if !slices.Contains([]string{"type", "refresh", "access", "expires", "accountId", "email", "projectId", "enterpriseUrl", "apiEndpoint"}, k) {
				t.Errorf("credential carries %q, which omp's schema does not know", k)
			}
		}
		providersSeen[entry["provider"].(string)] = entry
	}
	if _, ok := providersSeen["openai-codex"]; !ok {
		t.Errorf("providers = %v, want omp's ids (openai-codex, github-copilot)", keysOf(map[string]any{"x": providersSeen}))
	}
	if e := providersSeen["openai-codex"]; e["identityKey"] != "acct-1" {
		t.Errorf("identity = %v", e["identityKey"])
	}
	body, _ := json.Marshal(raw)
	if strings.Contains(string(body), "refresh-chatgpt-acc") || strings.Contains(string(body), "sk-ant") {
		t.Errorf("the snapshot leaks a refresh token or a key: %s", body)
	}
	if !slices.Contains(registered, "chatgpt-acc") || !slices.Contains(registered, "gho_tok") {
		t.Errorf("registered = %v", registered)
	}

	// Unchanged since the client's generation: 304 with the same ETag.
	gen := strconv.FormatInt(int64(raw["generation"].(float64)), 10)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/snapshot", nil)
	req.Header.Set("If-None-Match", `"`+gen+`"`)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified || resp.Header.Get("ETag") != `"`+gen+`"` {
		t.Errorf("conditional snapshot = %d %q", resp.StatusCode, resp.Header.Get("ETag"))
	}

	// A refresh answers the entry (no rotatesInMs); an unknown id is a 404.
	id := strconv.FormatInt(brokerID("openai-chatgpt-login"), 10)
	resp, _ = http.Post(srv.URL+"/v1/credential/"+id+"/refresh?reason=auth-recovery", "", nil)
	var ref map[string]map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&ref)
	resp.Body.Close()
	if resp.StatusCode != 200 || !slices.Equal(keysOf(ref["entry"]), []string{"credential", "id", "identityKey", "provider"}) {
		t.Errorf("refresh = %d %v", resp.StatusCode, ref)
	}
	for path, want := range map[string]int{
		"/v1/credential/1/refresh":          404,
		"/v1/credential":                    403, // a workspace adds no credential
		"/v1/credential/" + id + "/disable": 200,
		"/v1/usage/observed":                200,
	} {
		resp, _ := http.Post(srv.URL+path, "application/json", strings.NewReader(`{}`))
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("POST %s = %d, want %d", path, resp.StatusCode, want)
		}
	}
	for path, want := range map[string]int{"/v1/healthz": 200, "/v1/snapshot/stream": 404, "/v1/credentials/disabled": 200, "/v1/nothing": 404} {
		resp, _ := http.Get(srv.URL + path)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
	}
}

// A ChatGPT login lists what its plan offers, and a flow model outside the
// plan is refused before any workspace exists.
func TestCodexAccountModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer acc" || r.Header.Get("chatgpt-account-id") != "acct-1" || r.URL.Query().Get("client_version") != CodexVersion {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"slug":"alpha-codex","visibility":"list"},{"slug":"beta","visibility":"list"},{"slug":"hidden","visibility":"hide"}]}`))
	}))
	defer srv.Close()
	old := chatgptModelsURL
	chatgptModelsURL = srv.URL
	defer func() { chatgptModelsURL = old }()

	h, _ := Codex{}.New(withCreds(login("openai-chatgpt", "acc", map[string]string{"account_id": "acct-1", "email": "me@x"})), nil)
	models, err := h.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"openai/alpha-codex", "openai/beta"}) {
		t.Errorf("models = %v", ids)
	}
	r := h.(AuthChecker).CheckAuth(context.Background(), []string{"openai/alpha-codex", "openai/gamma"})
	if len(r.Rejected) != 1 || !strings.Contains(r.Rejected[0], "openai/gamma") || !strings.Contains(r.Rejected[0], "not offered") {
		t.Errorf("check = %+v", r)
	}
	refused, _ := Codex{}.New(withCreds(login("openai-chatgpt", "stale", map[string]string{"account_id": "acct-1"})), nil)
	if r := refused.(AuthChecker).CheckCredentials(context.Background()); len(r.Rejected) != 1 || !strings.Contains(r.Rejected[0], "sign in again") {
		t.Errorf("refused login = %+v", r)
	}
	_ = providers.KindOAuth
}
