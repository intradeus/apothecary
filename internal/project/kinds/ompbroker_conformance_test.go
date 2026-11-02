//go:build conformance

package kinds

// The broker face against the real pinned omp (docs/model-providers-plan.md:
// omp's wire schemas are strict and unversioned, so only omp itself can say
// the engine speaks them). How these tests run: conformance_test.go.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOmpConformance(t *testing.T) {
	bin := toolsBin(t, "omp")
	wantVersion(t, bin, OmpVersion)
	h, _ := Omp{}.New(withCreds(
		login("openai-chatgpt", "chatgpt-access-conformance", map[string]string{"account_id": "acct-c", "email": "c@example.test"}),
		login("anthropic", "anthropic-access-conformance", map[string]string{"email": "c@example.test"}),
	), nil)
	const token = "broker-token-conformance"
	var seen []string
	mux := http.NewServeMux()
	broker := h.(AuthBrokered).Broker(func(string) {})
	// The control: a broker serving no login.
	emptyBroker := (&ompHarness{}).Broker(func(string) {})
	mux.HandleFunc("/api/runs/empty/auth-broker/", func(w http.ResponseWriter, r *http.Request) {
		inner := r.Clone(r.Context())
		inner.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/runs/empty/auth-broker")
		emptyBroker.ServeHTTP(w, inner)
	})
	mux.HandleFunc("/api/runs/r1/auth-broker/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		seen = append(seen, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/api/runs/r1/auth-broker"))
		inner := r.Clone(r.Context())
		inner.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/runs/r1/auth-broker")
		broker.ServeHTTP(w, inner)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	runOn := func(runID string, args ...string) (string, error) {
		return runBin(t, harnessEnv(t.TempDir(), "OMP_SKIP_SETUP=1",
			"OMP_AUTH_BROKER_URL="+srv.URL+"/api/runs/"+runID+"/auth-broker",
			"OMP_AUTH_BROKER_TOKEN="+token,
		), "", bin, args...)
	}
	run := func(args ...string) (string, error) { return runOn("r1", args...) }
	// "Available" models are the ones omp holds a credential for: listing
	// them makes omp load its credentials — here, from the engine's broker.
	out2, err := run("models", "--json")
	t.Logf("omp models --json: %d bytes; broker requests: %v", len(out2), seen)
	if err != nil {
		t.Fatalf("omp failed with the engine's broker: %v\n%s", err, tail(out2, 2000))
	}
	snapshot := false
	for _, r := range seen {
		snapshot = snapshot || r == "GET /v1/snapshot"
	}
	if !snapshot {
		t.Fatalf("omp never asked the engine's broker for a snapshot (requests %v):\n%s", seen, tail(out2, 2000))
	}
	for _, want := range []string{"openai-codex", "anthropic"} {
		if !strings.Contains(out2, `"`+want+`"`) && !strings.Contains(out2, want+"/") {
			t.Errorf("omp lists no %s model: it did not accept the login it was served", want)
		}
	}
	// The control: with no login served, omp lists none of those models —
	// so the ones above came from the logins the engine served.
	none, err := runOn("empty", "models", "--json")
	if err != nil {
		t.Fatalf("omp with an empty broker: %v\n%s", err, tail(none, 2000))
	}
	if strings.Contains(none, `"openai-codex"`) || strings.Contains(none, "openai-codex/") {
		t.Errorf("omp lists openai-codex models with no login: the check above proves nothing")
	}
	// What omp exports is what it stored: never a refresh token.
	exp, err := run("auth", "export")
	t.Logf("omp auth export: %s", tail(exp, 600))
	if strings.Contains(exp, "refresh-chatgpt-access-conformance") || strings.Contains(exp, "refresh-anthropic-access-conformance") {
		t.Error("omp holds a refresh token it was never sent")
	}
}
