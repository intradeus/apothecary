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
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workspace/envstate"
)

// rebuilderEnvp adds the WorkspaceRebuilder capability to the fake, with a
// gate so a test can hold a rebuild in flight and observe concurrent ops.
type rebuilderEnvp struct {
	*fake.EnvProvisioner
	lastSpec envport.EnvSpec
	calls    int32
	started  chan struct{} // closed the first time Rebuild is entered
	release  chan struct{} // Rebuild blocks until this is closed (nil = no block)
	err      error
	once     sync.Once
}

func (r *rebuilderEnvp) Rebuild(ctx context.Context, spec envport.EnvSpec) error {
	r.lastSpec = spec
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
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "t"}, "loop", testProject)
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

	if err := h.e.RebuildEnv(context.Background(), id, false); err != nil {
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
	err := h.e.RebuildEnv(context.Background(), id, false)
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

	err := h.e.RebuildEnv(ctx, "r1", false)
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
	go func() { done <- h.e.RebuildEnv(context.Background(), id, false) }()
	<-reb.started // first rebuild has claimed the gate and is blocking

	// A second rebuild while the first holds the gate is refused.
	if err := h.e.RebuildEnv(context.Background(), id, false); !errors.Is(err, envstate.ErrInFlight) {
		t.Fatalf("second rebuild: want envstate.ErrInFlight, got %v", err)
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
	go func() { done <- h.e.RebuildEnv(context.Background(), id, false) }()
	<-reb.started

	// Reaper runs while the rebuild holds the gate — must not destroy.
	time.Sleep(5 * time.Millisecond) // ensure the completed-window elapses
	if err := h.e.ReapOnce(context.Background(), Window(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if len(h.envp.Destroyed) != 0 {
		t.Fatalf("reaper destroyed a rebuilding env: %v", h.envp.Destroyed)
	}
	close(reb.release)
	<-done
}

// "Rebuild without cache" has to reach the provisioner, and a plain rebuild
// has to keep the cache: silently throwing it away would cost the full build
// every time someone recreated a container.
func TestRebuildPassesNoCacheThrough(t *testing.T) {
	for _, noCache := range []bool{true, false} {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		id := completedWithEnv(t, h)
		rb := &rebuilderEnvp{EnvProvisioner: h.envp}
		h.res.Envp = rb
		if err := h.e.RebuildEnv(context.Background(), id, noCache); err != nil {
			t.Fatalf("RebuildEnv(noCache=%v): %v", noCache, err)
		}
		if rb.lastSpec.NoCache != noCache {
			t.Errorf("provisioner saw NoCache=%v, want %v", rb.lastSpec.NoCache, noCache)
		}
		// A re-clone after a reap enters the run's branch like the first
		// one: the pushed branch, else forked off the source branch
		// (testProject's default branch); no merge request head here.
		if rb.lastSpec.SourceBranch != "main" || rb.lastSpec.Branch == "" || rb.lastSpec.Fetch != "" {
			t.Errorf("rebuild spec branch %q source %q fetch %q, want the run branch off main", rb.lastSpec.Branch, rb.lastSpec.SourceBranch, rb.lastSpec.Fetch)
		}
	}
}

// A rebuild's bootstrap and provisioner read the checkouts from the view:
// the merge request's head for the primary, every extra with its own.
func TestViewCheckoutsForRebuild(t *testing.T) {
	v := run.View{Checkouts: []event.Checkout{
		{Name: "lib", Dir: "repos/lib", URL: "u", Branch: "b", Source: "s", Fetch: "refs/merge-requests/2/head"},
		{Name: "repo", Dir: "repo", Primary: true, Fetch: "refs/merge-requests/7/head"},
	}}
	if got := viewPrimaryFetch(v); got != "refs/merge-requests/7/head" {
		t.Errorf("primary fetch = %q", got)
	}
	if got := viewPrimaryFetch(run.View{}); got != "" {
		t.Errorf("no checkouts: fetch = %q", got)
	}
	extras := viewExtras(v)
	if len(extras) != 1 || extras[0].Name != "lib" || extras[0].Fetch != "refs/merge-requests/2/head" || extras[0].Source != "s" {
		t.Errorf("extras = %+v", extras)
	}
}
