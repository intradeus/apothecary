package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/engine"
)

// memLimits is an in-memory engine.LimitsStore.
type memLimits struct {
	mu         sync.Mutex
	runs, envs int
	set        bool
}

func (m *memLimits) LoadLimits(context.Context) (int, int, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runs, m.envs, m.set, nil
}

func (m *memLimits) SaveLimits(_ context.Context, runs, envs int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs, m.envs, m.set = runs, envs, true
	return nil
}

func TestLimitsAreAnAdminSetting(t *testing.T) {
	x := newEnv(t, engine.Config{})
	store := &memLimits{}
	x.srv.Limits = engine.StoredLimits{Store: store}

	// Never set: the engine defaults.
	res, body := x.do(t, "GET", "/api/limits", nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"max_concurrent_runs":3`) || !strings.Contains(string(body), `"max_environments":6`) {
		t.Fatalf("defaults: %d %s", res.StatusCode, body)
	}
	// The admin sets them; -1 is "no cap".
	res, body = x.do(t, "PUT", "/api/limits", map[string]any{"max_concurrent_runs": 5, "max_environments": -1}, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"max_concurrent_runs":5`) || !strings.Contains(string(body), `"max_environments":-1`) {
		t.Fatalf("set: %d %s", res.StatusCode, body)
	}
	if l, _ := (engine.StoredLimits{Store: store}).Limits(context.Background()); l.MaxConcurrentRuns != 5 || l.MaxEnvironments != -1 {
		t.Fatalf("stored = %+v", l)
	}
	// Zero and below -1 are refused.
	for _, bad := range []map[string]any{{"max_concurrent_runs": 0, "max_environments": 2}, {"max_concurrent_runs": 2, "max_environments": -2}} {
		if res, _ := x.do(t, "PUT", "/api/limits", bad, nil); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%v: status %d", bad, res.StatusCode)
		}
	}

	// A non-admin reads them but may not change them.
	now := time.Now().UTC()
	if err := x.users.CreateUser(context.Background(), auth.User{ID: "u2", Handle: "bob", Email: "bob@example.test", Name: "Bob",
		CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	tok, _, err := x.srv.Auth.NewToken(context.Background(), "u2", "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	hdr := map[string]string{"Authorization": "Bearer " + tok}
	if res, _ := x.do(t, "GET", "/api/limits", nil, hdr); res.StatusCode != http.StatusOK {
		t.Errorf("non-admin read: %d", res.StatusCode)
	}
	if res, _ := x.do(t, "PUT", "/api/limits", map[string]any{"max_concurrent_runs": 1, "max_environments": 1}, hdr); res.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin write: %d", res.StatusCode)
	}
}
