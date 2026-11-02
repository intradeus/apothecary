package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/engine"
)

type pingFunc func(context.Context) error

func (f pingFunc) Ping(ctx context.Context) error { return f(ctx) }

// The probes need no account and say what an orchestrator acts on: alive
// always; ready only while the database answers and shutdown has not begun.
func TestProbesSayWhetherToRouteTraffic(t *testing.T) {
	var dbErr error
	s := &Server{DB: pingFunc(func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the readiness ping has no deadline")
		}
		return dbErr
	})}
	h := s.Handler()
	probe := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}

	if code, body := probe("/healthz"); code != http.StatusOK || body != `{"status":"ok"}` {
		t.Fatalf("healthz = %d %s", code, body)
	}
	if code, body := probe("/readyz"); code != http.StatusOK || body != `{"status":"ok"}` {
		t.Fatalf("readyz = %d %s", code, body)
	}
	// The cause stays in the log: the probe is public and a driver error
	// names the database's user, host and port.
	dbErr = errors.New("failed to connect to user=apothecary database=apothecary: 10.0.0.5:5432: connection refused")
	if code, body := probe("/readyz"); code != http.StatusServiceUnavailable || body != `{"status":"db","error":"database unreachable"}` {
		t.Fatalf("readyz, db down = %d %s", code, body)
	}
	dbErr = fmt.Errorf("ping: %w", context.DeadlineExceeded)
	if code, body := probe("/readyz"); code != http.StatusServiceUnavailable || body != `{"status":"db","error":"database timeout"}` {
		t.Fatalf("readyz, db slow = %d %s", code, body)
	}
	dbErr = nil
	s.Drain()
	if code, body := probe("/readyz"); code != http.StatusServiceUnavailable || body != `{"status":"draining"}` {
		t.Fatalf("readyz, draining = %d %s", code, body)
	}
	if code, _ := probe("/healthz"); code != http.StatusOK {
		t.Fatalf("healthz, draining = %d", code)
	}
}

// Without a database (bare servers) readiness is only the drain.
func TestReadyWithoutADatabase(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Server{}).Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz = %d %s", rec.Code, rec.Body)
	}
}

// The Grafana link is the admin's: it names the operator's backend.
func TestObservabilityIsTheAdminsLink(t *testing.T) {
	x := newEnv(t, engine.Config{})
	if res, body := x.do(t, "GET", "/api/observability", nil, nil); res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"grafana_url":""`) {
		t.Fatalf("unset: %d %s", res.StatusCode, body)
	}
	x.srv.GrafanaURL = "https://grafana.example.test/d/x"
	if res, body := x.do(t, "GET", "/api/observability", nil, nil); res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"grafana_url":"https://grafana.example.test/d/x"`) {
		t.Fatalf("set: %d %s", res.StatusCode, body)
	}

	now := time.Now().UTC()
	if err := x.users.CreateUser(context.Background(), auth.User{ID: "u2", Handle: "bob", Email: "bob@example.test", Name: "Bob",
		CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	tok, _, err := x.srv.Auth.NewToken(context.Background(), "u2", "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res, _ := x.do(t, "GET", "/api/observability", nil, map[string]string{"Authorization": "Bearer " + tok}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin: %d", res.StatusCode)
	}
}
