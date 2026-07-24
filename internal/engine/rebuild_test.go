package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// rebuilderEnvp adds the WorkspaceRebuilder capability to the fake, with a
// gate so a test can hold a rebuild in flight and observe concurrent ops.
type rebuilderEnvp struct {
	*fake.EnvProvisioner
	calls   int32
	started chan struct{} // closed the first time Rebuild is entered
	release chan struct{} // Rebuild blocks until this is closed (nil = no block)
	err     error
	once    sync.Once
}

func (r *rebuilderEnvp) Rebuild(ctx context.Context, spec ports.EnvSpec) error {
	atomic.AddInt32(&r.calls, 1)
	if r.started != nil {
		r.once.Do(func() { close(r.started) })
	}
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.err
}

// completedWithEnv creates a run and drives it to completion with a live env.
func completedWithEnv(t *testing.T, h *harness) string {
	t.Helper()
	id, err := h.e.CreateRun(context.Background(), Ticket{Title: "t"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	if v := h.view(t, id); v.State != run.Completed || v.EnvID == "" {
		t.Fatalf("setup: %+v", v)
	}
	return id
}

func hasEvent(evs []event.Event, ty event.Type) bool {
	for _, e := range evs {
		if e.Type == ty {
			return true
		}
	}
	return false
}

func TestRebuildEmitsEventsAndPreservesState(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	id := completedWithEnv(t, h)
	before := h.view(t, id)
	h.res.Envp = &rebuilderEnvp{EnvProvisioner: h.envp}

	if err := h.e.RebuildEnv(context.Background(), id); err != nil {
		t.Fatalf("RebuildEnv: %v", err)
	}
	evs, _ := h.store.Events(context.Background(), id, 0)
	if !hasEvent(evs, event.TypeEnvRebuildStarted) || !hasEvent(evs, event.TypeEnvRebuildCompleted) {
		t.Error("rebuild must emit started + completed")
	}
	after := h.view(t, id)
	if after.State != run.Completed || after.EnvID != before.EnvID {
		t.Errorf("rebuild changed lifecycle: state %s->%s env %q->%q",
			before.State, after.State, before.EnvID, after.EnvID)
	}
}

func TestRebuildUnsupportedProvisioner(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	id := completedWithEnv(t, h) // h.res.Envp stays the plain fake (no Rebuild)
	err := h.e.RebuildEnv(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("want unsupported error, got %v", err)
	}
}

func TestRebuildRefusesActiveRun(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()
	// A run with a live handle (loop goroutine) — no need to actually run it.
	if _, err := h.store.Append(ctx, "r1",
		&event.RunCreated{ProjectID: testProject, FlowName: "loop"},
		&event.EnvReady{EnvID: "env1"},
	); err != nil {
		t.Fatal(err)
	}
	h.e.mu.Lock()
	h.e.handles["r1"] = &runHandle{}
	h.e.mu.Unlock()
	// Remove the stub handle before the harness's Shutdown cleanup runs (it
	// would deref the handle's nil fields). defer beats t.Cleanup ordering.
	defer func() {
		h.e.mu.Lock()
		delete(h.e.handles, "r1")
		h.e.mu.Unlock()
	}()
	h.res.Envp = &rebuilderEnvp{EnvProvisioner: h.envp}

	err := h.e.RebuildEnv(ctx, "r1")
	if !errors.Is(err, ErrRunActive) {
		t.Fatalf("want ErrRunActive, got %v", err)
	}
}

func TestRebuildOpGateMutualExclusion(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	id := completedWithEnv(t, h)
	reb := &rebuilderEnvp{EnvProvisioner: h.envp, started: make(chan struct{}), release: make(chan struct{})}
	h.res.Envp = reb

	done := make(chan error, 1)
	go func() { done <- h.e.RebuildEnv(context.Background(), id) }()
	<-reb.started // first rebuild has claimed the gate and is blocking

	// A second rebuild while the first holds the gate is refused.
	if err := h.e.RebuildEnv(context.Background(), id); !errors.Is(err, ErrEnvOpInFlight) {
		t.Fatalf("second rebuild: want ErrEnvOpInFlight, got %v", err)
	}
	close(reb.release)
	if err := <-done; err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	if reb.calls != 1 {
		t.Errorf("Rebuild called %d times, want 1 (the second was gated out)", reb.calls)
	}
}

// The reaper must SKIP a run whose env has a rebuild in flight, not race the
// destroy against it (TODO-17c).
func TestReaperSkipsRebuildingEnv(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	id := completedWithEnv(t, h)
	reb := &rebuilderEnvp{EnvProvisioner: h.envp, started: make(chan struct{}), release: make(chan struct{})}
	h.res.Envp = reb

	done := make(chan error, 1)
	go func() { done <- h.e.RebuildEnv(context.Background(), id) }()
	<-reb.started

	// Reaper runs while the rebuild holds the gate — must not destroy.
	time.Sleep(5 * time.Millisecond) // ensure the completed-window elapses
	if err := h.e.ReapOnce(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if len(h.envp.Destroyed) != 0 {
		t.Fatalf("reaper destroyed a rebuilding env: %v", h.envp.Destroyed)
	}
	close(reb.release)
	<-done
}

func TestEnvBadgesOverlayAndTTL(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	clk := time.Unix(1000, 0)
	h.e.clock = func() time.Time { return clk }

	h.e.envStatusMu.Lock()
	h.e.envStatus["r1"] = ports.EnvStopped
	h.e.envStatusMu.Unlock()

	// An in-flight op overlays the reconciled docker status.
	rel, err := h.e.beginEnvOp("r1", "rebuilding")
	if err != nil {
		t.Fatal(err)
	}
	if got := h.e.EnvBadges()["r1"]; got != "rebuilding" {
		t.Errorf("badge = %q, want rebuilding", got)
	}
	// Failing the op leaves an "error" badge...
	rel(errors.New("boom"))
	if got := h.e.EnvBadges()["r1"]; got != "error" {
		t.Errorf("badge = %q, want error", got)
	}
	// ...which ages out after the TTL, falling back to the docker status.
	clk = clk.Add(envOpErrorTTL + time.Second)
	if got := h.e.EnvBadges()["r1"]; got != "stopped" {
		t.Errorf("badge after TTL = %q, want stopped (fallback)", got)
	}
}
