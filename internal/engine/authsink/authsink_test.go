package authsink

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"apothecary/internal/engine/redact"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

// A refreshed login is pushed only into the workspaces whose run uses that
// credential (rule 2: by credential id, never by provider or kind), and the
// run's redactor knows the new token before the file holding it is written
// (rule 4).
func TestPushScopesAndRedactsFirst(t *testing.T) {
	e := New(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	type seeded struct {
		env      envport.EnvID
		redacted bool // the token was already known when the file was written
	}
	var writes []seeded
	sink := func(ids []string, env envport.EnvID) (*resolution.Resolution, *redact.Redactor) {
		r := redact.New()
		res := &resolution.Resolution{CredentialIDs: ids}
		res.SeedAuth = func(_ context.Context, env envport.EnvID, register func(string)) error {
			register("fresh-access-token-123")
			writes = append(writes, seeded{env: env, redacted: r.Scrub("fresh-access-token-123") != "fresh-access-token-123"})
			return nil
		}
		return res, r
	}
	resA, redA := sink([]string{"cred-a", "cred-shared"}, "env-a")
	resB, redB := sink([]string{"cred-b"}, "env-b")
	e.Set("run-a", resA, "env-a", redA)
	e.Set("run-b", resB, "env-b", redB)

	e.Push(context.Background(), "cred-shared")
	if len(writes) != 1 || writes[0].env != "env-a" {
		t.Fatalf("pushed into %v, want only run-a's workspace", writes)
	}
	if !writes[0].redacted {
		t.Error("the token was written before the run's redactor knew it")
	}
	if out := redA.Scrub("log: fresh-access-token-123"); strings.Contains(out, "fresh-access-token-123") {
		t.Errorf("run-a's output still shows the pushed token: %q", out)
	}
	if out := redB.Scrub("fresh-access-token-123"); out != "fresh-access-token-123" {
		t.Error("run-b's redactor learned a token its workspace never received")
	}

	e.Drop("run-a")
	writes = nil
	e.Push(context.Background(), "cred-shared")
	if len(writes) != 0 {
		t.Error("a stopped run's workspace was pushed into")
	}
}

// The broker token is per run, stable for a given instance key, and nothing
// without one.
func TestBrokerToken(t *testing.T) {
	if New(nil, nil, nil, nil).BrokerToken("r1") != "" {
		t.Error("a token without an instance key")
	}
	e := New(nil, nil, []byte("instance-key"), nil)
	a, b := e.BrokerToken("r1"), e.BrokerToken("r2")
	if a == "" || a == b {
		t.Errorf("tokens %q %q: want distinct per run", a, b)
	}
	again := New(nil, nil, []byte("instance-key"), nil)
	if again.BrokerToken("r1") != a {
		t.Error("the token changed across a restart with the same instance key")
	}
	res := &resolution.Resolution{ProviderEnv: map[string]string{"K": "v"}, AuthBroker: func(func(string)) http.Handler { return nil }}
	env := e.ProviderEnv(res, "r1", "http://engine:8080")
	if env["OMP_AUTH_BROKER_URL"] != "http://engine:8080/api/runs/r1/auth-broker" || env["OMP_AUTH_BROKER_TOKEN"] != a || env["K"] != "v" {
		t.Errorf("provider env = %v", env)
	}
	if _, leaked := res.ProviderEnv["OMP_AUTH_BROKER_TOKEN"]; leaked {
		t.Error("the resolution's own env was modified")
	}
}

// One workspace failing to take a refreshed login is logged and leaves the
// others served: Push has no one to report to.
func TestPushIsBestEffortPerWorkspace(t *testing.T) {
	var logged strings.Builder
	e := New(nil, nil, nil, slog.New(slog.NewTextHandler(&logged, nil)))
	var seeded []envport.EnvID
	seed := func(_ context.Context, env envport.EnvID, _ func(string)) error {
		seeded = append(seeded, env)
		if env == "env-broken" {
			return errors.New("container gone")
		}
		return nil
	}
	e.Set("run-broken", &resolution.Resolution{CredentialIDs: []string{"c"}, SeedAuth: seed}, "env-broken", redact.New())
	e.Set("run-ok", &resolution.Resolution{CredentialIDs: []string{"c"}, SeedAuth: seed}, "env-ok", redact.New())
	// Reads its login some other way: nothing to rewrite.
	e.Set("run-noseed", &resolution.Resolution{CredentialIDs: []string{"c"}}, "env-noseed", redact.New())

	e.Push(context.Background(), "c")
	slices.Sort(seeded)
	if !slices.Equal(seeded, []envport.EnvID{"env-broken", "env-ok"}) {
		t.Errorf("seeded = %v", seeded)
	}
	if out := logged.String(); !strings.Contains(out, "container gone") || !strings.Contains(out, "run-broken") {
		t.Errorf("the failure was not logged: %s", out)
	}
}

// A zero Sink is usable: Set must not panic on a nil map, and nil logger
// means the default.
func TestZeroSink(t *testing.T) {
	var s Sink
	s.Set("r1", &resolution.Resolution{}, "env", redact.New())
	if s.get("r1") == nil {
		t.Error("the run was not recorded")
	}
	if s.log() == nil {
		t.Error("no default logger")
	}
	if s.BrokerToken("r1") != "" {
		t.Error("a broker token without a key")
	}
}

func TestBrokerTokenNeedsARun(t *testing.T) {
	if got := New(nil, nil, []byte("key"), nil).BrokerToken(""); got != "" {
		t.Errorf("token for no run = %q", got)
	}
}

// No broker means the project's env unchanged, the same map; nothing about
// a broker reaches the workspace. A nil env still gets the broker's two keys.
func TestProviderEnvWithoutABroker(t *testing.T) {
	keyed := New(nil, nil, []byte("key"), nil)
	plain := &resolution.Resolution{ProviderEnv: map[string]string{"K": "v"}}
	if env := keyed.ProviderEnv(plain, "r1", "http://e"); len(env) != 1 || env["K"] != "v" {
		t.Errorf("env = %v", env)
	}
	broker := &resolution.Resolution{AuthBroker: func(func(string)) http.Handler { return nil }}
	keyless := New(nil, nil, nil, nil)
	if env := keyless.ProviderEnv(broker, "r1", "http://e"); len(env) != 0 {
		t.Errorf("a broker with no way to authenticate leaked: %v", env)
	}
	env := keyed.ProviderEnv(broker, "r1", "http://e")
	if env["OMP_AUTH_BROKER_URL"] != "http://e"+AuthBrokerPath("r1") || env["OMP_AUTH_BROKER_TOKEN"] == "" {
		t.Errorf("env = %v", env)
	}
}

// brokerRig is a sink over a one-run store.
func brokerRig(t *testing.T, res *resolution.Resolution, tail ...event.Payload) (*Sink, *run.Reader) {
	t.Helper()
	store := event.NewMemStore()
	ps := append([]event.Payload{&event.RunCreated{ProjectID: "p1"}}, tail...)
	if _, err := store.Append(context.Background(), "r1", ps...); err != nil {
		t.Fatal(err)
	}
	runs := &run.Reader{Store: store}
	return New(runs, resolution.StaticResolver{R: res}, []byte("key"), nil), runs
}

func TestRunAuthBrokerServesALiveRun(t *testing.T) {
	var released int
	var served string
	res := &resolution.Resolution{Release: func() { released++ }}
	res.AuthBroker = func(register func(string)) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			register("broker-access-token")
			served = "yes"
			w.WriteHeader(http.StatusNoContent)
		})
	}
	s, _ := brokerRig(t, res)
	red := redact.New()
	s.Set("r1", res, "env", red)

	h, release, err := s.RunAuthBroker(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent || served != "yes" {
		t.Errorf("served %d", rec.Code)
	}
	// The token it hands out is known to the run's redactor from then on.
	if out := red.Scrub("broker-access-token"); strings.Contains(out, "broker-access-token") {
		t.Errorf("broker token not registered: %q", out)
	}
	if released != 0 {
		t.Errorf("released while serving (%d)", released)
	}
	release()
	if released != 1 {
		t.Errorf("released %d times", released)
	}
}

// The broker may be asked before the workspace registered with the sink: it
// serves, with nothing to redact into.
func TestRunAuthBrokerBeforeTheWorkspaceRegistered(t *testing.T) {
	res := &resolution.Resolution{}
	res.AuthBroker = func(register func(string)) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { register("t") })
	}
	s, _ := brokerRig(t, res)
	h, release, err := s.RunAuthBroker(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestRunAuthBrokerRefuses(t *testing.T) {
	ctx := context.Background()
	t.Run("an ended run has no business with live credentials", func(t *testing.T) {
		res := &resolution.Resolution{AuthBroker: func(func(string)) http.Handler { return nil }}
		s, _ := brokerRig(t, res, &event.RunCompleted{})
		_, _, err := s.RunAuthBroker(ctx, "r1")
		if err == nil || !strings.Contains(err.Error(), "run r1 has ended") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a harness that reads no login", func(t *testing.T) {
		var released int
		s, _ := brokerRig(t, &resolution.Resolution{Release: func() { released++ }})
		_, _, err := s.RunAuthBroker(ctx, "r1")
		if err == nil || !strings.Contains(err.Error(), "reads no login") {
			t.Errorf("err = %v", err)
		}
		if released != 1 {
			t.Errorf("released %d times", released)
		}
	})
	t.Run("an unknown run", func(t *testing.T) {
		s, _ := brokerRig(t, &resolution.Resolution{})
		if _, _, err := s.RunAuthBroker(ctx, "ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a project that will not resolve", func(t *testing.T) {
		s, runs := brokerRig(t, nil)
		s.Resolver = resolution.StaticResolver{}
		s.Runs = runs
		if _, _, err := s.RunAuthBroker(ctx, "r1"); !errors.Is(err, resolution.ErrUnresolvable) {
			t.Errorf("err = %v", err)
		}
	})
}
