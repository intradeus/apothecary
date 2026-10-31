//go:build ompconformance

package kinds

// The broker face against the real pinned omp (docs/model-providers-plan.md:
// omp's wire schemas are strict and unversioned, so only omp itself can say
// the engine speaks them). Not part of `go test ./...`: it needs the omp
// binary of the tools bundle.
//
//	OMP_BIN=/path/to/omp go test -tags ompconformance -run TestOmpConformance ./internal/project/kinds/
//
// The tools image carries it at /tools/gnu/bin/omp (glibc) and
// /tools/musl/bin/omp.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestOmpConformance(t *testing.T) {
	bin := os.Getenv("OMP_BIN")
	if bin == "" {
		t.Skip("OMP_BIN not set")
	}
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), OmpVersion) {
		t.Fatalf("OMP_BIN is %q, want omp %s: %v", strings.TrimSpace(string(out)), OmpVersion, err)
	}
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
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = []string{
			"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH"), "OMP_SKIP_SETUP=1", "NO_COLOR=1",
			"OMP_AUTH_BROKER_URL=" + srv.URL + "/api/runs/" + runID + "/auth-broker",
			"OMP_AUTH_BROKER_TOKEN=" + token,
		}
		b, err := cmd.CombinedOutput()
		return string(b), err
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

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
