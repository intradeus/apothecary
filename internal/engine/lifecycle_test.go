package engine

import (
	"context"
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

// Shutdown cancels every loop before waiting on any: three agents that each
// refuse to return until ALL of them have been cancelled finish only if the
// cancellations are not serialized behind the waits (which would hold the
// first one until awaitAgent's 60s grace gave up on it).
func TestShutdownCancelsEveryLoopBeforeWaiting(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	const n = 3
	var mu sync.Mutex
	cancelled := 0
	all := make(chan struct{})
	started := make(chan struct{}, n)
	h.runner.RunFunc = func(ctx context.Context, _ fake.RunCall) (ports.AgentResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		mu.Lock()
		cancelled++
		if cancelled == n {
			close(all)
		}
		mu.Unlock()
		<-all
		return ports.AgentResult{}, ctx.Err()
	}
	for i := 0; i < n; i++ {
		if _, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "loop", testProject); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d agents started", i, n)
		}
	}
	done := make(chan struct{})
	go func() { h.e.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not return: a loop was waited on before the others were cancelled")
	}
}

// A rebuild the previous process never finished is closed on boot: nothing
// is rebuilding it anymore, and the timeline must not show a rebuild that
// never ends.
func TestRecoverClosesInterruptedRebuild(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()
	id, err := h.e.CreateRun(ctx, Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	v := h.view(t, id)
	if v.State != run.Completed || v.EnvID == "" {
		t.Fatalf("setup: %+v", v)
	}
	// The crash: rebuild started, process died.
	if _, err := h.store.Append(ctx, id, &event.EnvRebuildStarted{EnvID: v.EnvID}); err != nil {
		t.Fatal(err)
	}
	if !h.view(t, id).Rebuilding {
		t.Fatal("setup: view does not read the open rebuild")
	}
	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	evs := h.events(t, id)
	last := evs[len(evs)-1]
	f, ok := last.Payload.(*event.EnvRebuildFailed)
	if !ok || !strings.Contains(f.Error, "interrupted") || f.EnvID != v.EnvID {
		t.Fatalf("last event = %s %+v, want env_rebuild_failed{interrupted}", last.Type, last.Payload)
	}
	if after := h.view(t, id); after.Rebuilding || after.State != run.Completed {
		t.Errorf("after recovery = %+v, want closed rebuild on a still-completed run", after)
	}
	// Idempotent: a second boot has nothing to close.
	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countType(h.events(t, id), event.TypeEnvRebuildFailed); got != 1 {
		t.Errorf("env_rebuild_failed appended %d times, want 1", got)
	}
}

// Every command-stage exec carries the stage's marker, so the adapter can kill
// what the previous attempt of the same stage left running and tag this one
// for the next sweep. The script itself is untouched.
func TestCommandStageCarriesItsMarker(t *testing.T) {
	doc := `name: c
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: test, type: command, run: "make test" }
`
	h := newHarness(t, mustFlow(t, doc))
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "c", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	var calls []fake.ExecCall
	for _, c := range h.envp.ExecCalls {
		if strings.Contains(c.Cmd.Script, "make test") {
			calls = append(calls, c)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("command stage execs = %d, want 1", len(calls))
	}
	cmd := calls[0].Cmd
	if want := commandMarker(runID, "test"); cmd.Marker != want || cmd.Marker != runID+":test" {
		t.Errorf("Marker = %q, want %q", cmd.Marker, want)
	}
	if strings.TrimSpace(cmd.Script) != "make test" {
		t.Errorf("script = %q, want the flow's command verbatim", cmd.Script)
	}
	// Nothing else the engine runs in the workspace carries a stage marker.
	for _, c := range h.envp.ExecCalls {
		if c.Cmd.Marker != "" && !strings.Contains(c.Cmd.Script, "make test") {
			t.Errorf("non-stage exec carries a marker: %q", c.Cmd.Script)
		}
	}
}

// viewStore is a MemStore that also keeps the runs projection (reduced on
// demand), counting the per-run log reads the engine still makes.
type viewStore struct {
	*event.MemStore
	logReads atomic.Int32
}

func (s *viewStore) Events(ctx context.Context, runID string, afterSeq int64) ([]event.Event, error) {
	s.logReads.Add(1)
	return s.MemStore.Events(ctx, runID, afterSeq)
}

func (s *viewStore) RunViews(ctx context.Context) ([]run.View, error) {
	ids, err := s.MemStore.RunIDs(ctx)
	if err != nil {
		return nil, err
	}
	var out []run.View
	for _, id := range ids {
		evs, err := s.MemStore.Events(ctx, id, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, run.Reduce(evs))
	}
	return out, nil
}

// With a store that keeps the projection, the pollers and boot recovery read
// it instead of every run's log — the whole point of having one.
func TestPollersReadTheProjection(t *testing.T) {
	var vs *viewStore
	h := newHarnessOn(t, func(m *event.MemStore) event.Store { vs = &viewStore{MemStore: m}; return vs }, mustFlow(t, loopFlowDoc))
	ctx := context.Background()
	id, err := h.e.CreateRun(ctx, Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	if h.view(t, id).State != run.Completed {
		t.Fatalf("setup: %+v", h.view(t, id))
	}
	vs.logReads.Store(0)
	if err := h.e.ReconcileEnvsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.e.ReapOnce(ctx, Window(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := h.e.SampleResourcesOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if n := vs.logReads.Load(); n != 0 {
		t.Errorf("%d event-log reads during reconcile/reap/sample/recover, want 0 (the projection answers them)", n)
	}
	if st := h.e.EnvStatuses()[id]; st != ports.EnvRunning {
		t.Errorf("reconciled status = %q, want running — the projection must carry env_id and project_id", st)
	}
}

// The reconciler records the config-dirty hint next to the status, and a
// rebuild clears it at once.
func TestReconcileRecordsConfigDirty(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()
	id, err := h.e.CreateRun(ctx, Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	envID := ports.EnvID(h.view(t, id).EnvID)
	if err := h.e.ReconcileEnvsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if h.e.EnvDirty()[id] {
		t.Fatal("clean workspace reads dirty")
	}
	h.envp.Dirty = map[ports.EnvID]bool{envID: true}
	if err := h.e.ReconcileEnvsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if !h.e.EnvDirty()[id] {
		t.Fatal("changed config not reported after reconcile")
	}
	h.res.Envp = &rebuilderEnvp{EnvProvisioner: h.envp}
	if err := h.e.RebuildEnv(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if h.e.EnvDirty()[id] {
		t.Error("a rebuild must clear the hint immediately")
	}
}
