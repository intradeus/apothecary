package engine

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"apothecary/internal/ports"
)

// A refreshed login is pushed only into the workspaces whose run uses that
// credential (rule 2: by credential id, never by provider or kind), and the
// run's redactor knows the new token before the file holding it is written
// (rule 4).
func TestPushCredentialScopesAndRedactsFirst(t *testing.T) {
	e := &Engine{}
	type seeded struct {
		env      ports.EnvID
		redacted bool // the token was already known when the file was written
	}
	var writes []seeded
	sink := func(ids []string, env ports.EnvID) (*Resolution, *redactor) {
		r := &redactor{}
		res := &Resolution{CredentialIDs: ids}
		res.SeedAuth = func(_ context.Context, env ports.EnvID, register func(string)) error {
			register("fresh-access-token-123")
			writes = append(writes, seeded{env: env, redacted: r.scrub("fresh-access-token-123") != "fresh-access-token-123"})
			return nil
		}
		return res, r
	}
	resA, redA := sink([]string{"cred-a", "cred-shared"}, "env-a")
	resB, redB := sink([]string{"cred-b"}, "env-b")
	e.setAuthSink("run-a", resA, "env-a", redA)
	e.setAuthSink("run-b", resB, "env-b", redB)
	e.log = slog.New(slog.NewTextHandler(io.Discard, nil))

	e.PushCredential(context.Background(), "cred-shared")
	if len(writes) != 1 || writes[0].env != "env-a" {
		t.Fatalf("pushed into %v, want only run-a's workspace", writes)
	}
	if !writes[0].redacted {
		t.Error("the token was written before the run's redactor knew it")
	}
	if out := redA.scrub("log: fresh-access-token-123"); strings.Contains(out, "fresh-access-token-123") {
		t.Errorf("run-a's output still shows the pushed token: %q", out)
	}
	if out := redB.scrub("fresh-access-token-123"); out != "fresh-access-token-123" {
		t.Error("run-b's redactor learned a token its workspace never received")
	}

	e.dropAuthSink("run-a")
	writes = nil
	e.PushCredential(context.Background(), "cred-shared")
	if len(writes) != 0 {
		t.Error("a stopped run's workspace was pushed into")
	}
}

// The broker token is per run, stable for a given instance key, and nothing
// without one.
func TestBrokerToken(t *testing.T) {
	e := &Engine{}
	if e.BrokerToken("r1") != "" {
		t.Error("a token without an instance key")
	}
	e.SetAuthBrokerKey([]byte("instance-key"))
	a, b := e.BrokerToken("r1"), e.BrokerToken("r2")
	if a == "" || a == b {
		t.Errorf("tokens %q %q: want distinct per run", a, b)
	}
	again := &Engine{}
	again.SetAuthBrokerKey([]byte("instance-key"))
	if again.BrokerToken("r1") != a {
		t.Error("the token changed across a restart with the same instance key")
	}
	res := &Resolution{ProviderEnv: map[string]string{"K": "v"}, AuthBroker: func(func(string)) http.Handler { return nil }}
	env := e.providerEnvFor(res, "r1", "http://engine:8080")
	if env["OMP_AUTH_BROKER_URL"] != "http://engine:8080/api/runs/r1/auth-broker" || env["OMP_AUTH_BROKER_TOKEN"] != a || env["K"] != "v" {
		t.Errorf("provider env = %v", env)
	}
	if _, leaked := res.ProviderEnv["OMP_AUTH_BROKER_TOKEN"]; leaked {
		t.Error("the resolution's own env was modified")
	}
}
