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
	"apothecary/internal/ports/envport"
	"apothecary/internal/workspace/resources"
)

// memLimits is an in-memory engine.LimitsStore.
type memLimits struct {
	mu         sync.Mutex
	runs, envs int
	sandbox    envport.SandboxPolicy
	set        bool
}

func (m *memLimits) LoadLimits(context.Context) (int, int, envport.SandboxPolicy, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runs, m.envs, m.sandbox, m.set, nil
}

func (m *memLimits) SaveLimits(_ context.Context, runs, envs int, sandbox envport.SandboxPolicy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs, m.envs, m.sandbox, m.set = runs, envs, sandbox, true
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
	tok, _, err := x.srv.Auth.NewToken(context.Background(), "u2", "t", nil, auth.Grant{})
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

// The sandbox sizing policy is part of the limits: an admin sets a default
// and a ceiling, a default above the ceiling is refused, and a workshop
// saved with a size above the ceiling is refused, naming it.
func TestSandboxSizesAreALimit(t *testing.T) {
	x := newConfEnv(t)
	store := &memLimits{}
	x.srv.Limits = engine.StoredLimits{Store: store}
	sizes := map[string]any{"default": map[string]any{"milli_cpu": 2000, "memory_mib": 4096},
		"ceiling": map[string]any{"milli_cpu": 8000, "memory_mib": 8192}}
	res, body := x.do(t, "PUT", "/api/limits", map[string]any{"max_concurrent_runs": 3, "max_environments": 6, "sandbox": sizes}, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ceiling":{"milli_cpu":8000,"memory_mib":8192}`) {
		t.Fatalf("set: %d %s", res.StatusCode, body)
	}
	bad := map[string]any{"default": map[string]any{"memory_mib": 9000}, "ceiling": map[string]any{"memory_mib": 8192}}
	if res, body := x.do(t, "PUT", "/api/limits", map[string]any{"max_concurrent_runs": 3, "max_environments": 6, "sandbox": bad}, nil); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "above the ceiling") {
		t.Errorf("default above the ceiling: %d %s", res.StatusCode, body)
	}

	id := x.makeProject(t, "sized", nil)
	p, err := x.conf.GetProject(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	body2 := projectBody{Name: p.Name, RepoAccountID: p.RepoAccountID, ProvisionerID: p.ProvisionerID, HarnessIDs: p.HarnessIDs,
		Repo: p.Repo, DefaultFlow: p.DefaultFlow, Workspace: p.Workspace}
	body2.Workspace.Size = envport.SandboxSize{MemoryMiB: 16384}
	res, data := x.do(t, "PUT", "/api/projects/"+id, body2, nil)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "8192 MiB") {
		t.Errorf("over the ceiling: %d %s", res.StatusCode, data)
	}
	body2.Workspace.Size = envport.SandboxSize{MilliCPU: 4000, MemoryMiB: 8192}
	res, data = x.do(t, "PUT", "/api/projects/"+id, body2, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(data), `"size":{"milli_cpu":4000,"memory_mib":8192}`) {
		t.Errorf("at the ceiling: %d %s", res.StatusCode, data)
	}
	// The ceiling is lowered under the workshop's size: saving the workshop
	// again with that size still works (the size is capped at provision);
	// raising it, or another field above the new ceiling, is refused.
	lower := map[string]any{"ceiling": map[string]any{"milli_cpu": 2000, "memory_mib": 4096}}
	if res, body := x.do(t, "PUT", "/api/limits", map[string]any{"max_concurrent_runs": 3, "max_environments": 6, "sandbox": lower}, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("lower: %d %s", res.StatusCode, body)
	}
	body2.Name = "sized-renamed" // edited after the ceiling came down
	if res, data := x.do(t, "PUT", "/api/projects/"+id, body2, nil); res.StatusCode != http.StatusOK {
		t.Errorf("an unchanged size above a lowered ceiling: %d %s", res.StatusCode, data)
	}
	body2.Workspace.Size.MemoryMiB = 6144
	if res, data := x.do(t, "PUT", "/api/projects/"+id, body2, nil); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "4096 MiB") {
		t.Errorf("a changed size above it: %d %s", res.StatusCode, data)
	}
	body2.Workspace.Size.MemoryMiB = 8192
	body2.Name = "sized-2"
	body2.Workspace.Size = envport.SandboxSize{MilliCPU: 9000}
	if res, data := x.do(t, "POST", "/api/projects", body2, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("create over the ceiling: %d %s", res.StatusCode, data)
	}
	// A number past what the store holds is a 400 naming the bound, never
	// a failed write.
	huge := map[string]any{"ceiling": map[string]any{"memory_mib": 1 << 40}}
	if res, body := x.do(t, "PUT", "/api/limits", map[string]any{"max_concurrent_runs": 3, "max_environments": 6, "sandbox": huge}, nil); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "memory must be") {
		t.Errorf("huge ceiling: %d %s", res.StatusCode, body)
	}
	body2.Workspace.Size = envport.SandboxSize{MemoryMiB: 64}
	if res, data := x.do(t, "POST", "/api/projects", body2, nil); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "between 512") {
		t.Errorf("below the floor: %d %s", res.StatusCode, data)
	}
}

// A workshop's sandbox usage is the sampler's record, empty without one.
func TestSandboxUsage(t *testing.T) {
	x := newConfEnv(t)
	id := x.makeProject(t, "used", nil)
	x.srv.Resources = nil
	if res, body := x.do(t, "GET", "/api/projects/"+id+"/sandbox-usage", nil, nil); res.StatusCode != http.StatusOK || string(body) != `{"runs":[]}`+"\n" {
		t.Errorf("no sampler: %d %q", res.StatusCode, body)
	}
	x.srv.Resources = &resources.Sampler{}
	if res, body := x.do(t, "GET", "/api/projects/"+id+"/sandbox-usage", nil, nil); res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"runs":[]`) {
		t.Errorf("nothing sampled: %d %q", res.StatusCode, body)
	}
}
