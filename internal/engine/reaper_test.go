package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// statusErrEnvp fails every Status probe (e.g. an expired cluster credential),
// delegating everything else to the fake.
type statusErrEnvp struct{ *fake.EnvProvisioner }

func (statusErrEnvp) Status(context.Context, ports.EnvID) (ports.EnvStatus, error) {
	return "", errors.New("provisioner unreachable: signed out")
}

func TestReapOnceDestroysOldCompletedOnly(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	// Completed run with a live env.
	doneID, err := h.e.CreateRun(ctx, Ticket{Title: "done"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(doneID)
	if v := h.view(t, doneID); v.State != run.Completed || v.EnvID == "" {
		t.Fatalf("setup: %+v", v)
	}

	// Abandoned run with a live env (must never be reaped).
	envID, _ := h.envp.Provision(ctx, ports.EnvSpec{RunID: "r-ab"})
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "ab", FlowName: "loop", ProjectID: testProject},
		&event.EnvRequested{}, &event.EnvReady{EnvID: string(envID)},
		&event.RunAbandoned{StalledAt: "test", Reason: "edge exhausted"},
	} {
		if _, err := h.store.Append(ctx, "r-ab", p); err != nil {
			t.Fatal(err)
		}
	}

	// Inside the window: nothing reaped.
	if err := h.e.ReapOnce(ctx, Window(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(h.envp.Destroyed) != 0 {
		t.Fatalf("reaped inside window: %v", h.envp.Destroyed)
	}

	// Outside the window: only the completed run's env goes.
	time.Sleep(5 * time.Millisecond)
	if err := h.e.ReapOnce(ctx, Window(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if len(h.envp.Destroyed) != 1 {
		t.Fatalf("destroyed = %v, want exactly the completed run's env", h.envp.Destroyed)
	}
	if v := h.view(t, doneID); v.EnvID != "" {
		t.Error("env_destroyed not appended for reaped run")
	}
	if v := h.view(t, "r-ab"); v.EnvID == "" {
		t.Error("abandoned run's env must never be reaped")
	}

	// Idempotent: second pass has nothing left to do.
	if err := h.e.ReapOnce(ctx, Window(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if len(h.envp.Destroyed) != 1 {
		t.Errorf("second pass destroyed more: %v", h.envp.Destroyed)
	}
}

func TestReconcileEnvsReflectsLiveContainerStatus(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	// A finished run keeps its env until reaped — a running container.
	id, err := h.e.CreateRun(ctx, Ticket{Title: "live"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	v := h.view(t, id)
	if v.EnvID == "" {
		t.Fatalf("setup: run has no env: %+v", v)
	}

	// Before any pass the map is empty — the read path falls back to env_id.
	if _, ok := h.e.envState.Statuses()[id]; ok {
		t.Fatal("status present before first reconcile")
	}

	if err := h.e.envState.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.e.envState.Statuses()[id]; got != ports.EnvRunning {
		t.Fatalf("after reconcile: got %q, want running", got)
	}

	// Stop the container out-of-band (no event) — the reconciler must catch it.
	h.envp.Env(ports.EnvID(v.EnvID)).Status = ports.EnvStopped
	if err := h.e.envState.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.e.envState.Statuses()[id]; got != ports.EnvStopped {
		t.Fatalf("after out-of-band stop: got %q, want stopped", got)
	}
	// The event log is untouched: env_id still set, no phantom EnvDestroyed.
	if got := h.view(t, id); got.EnvID != v.EnvID {
		t.Errorf("reconcile mutated the projection: env_id %q -> %q", v.EnvID, got.EnvID)
	}
}

// A failed status probe must record EnvUnknown, NOT carry a stale "running"
// forward — the bug where expired cluster credentials froze dead workspaces as
// green "live" indefinitely.
func TestReconcileEnvsUnknownWhenProbeFails(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	id, err := h.e.CreateRun(ctx, Ticket{Title: "orphan"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)

	// First pass sees the container running.
	if err := h.e.envState.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.e.envState.Statuses()[id]; got != ports.EnvRunning {
		t.Fatalf("setup: got %q, want running", got)
	}

	// Provisioner becomes unreachable (expired session). Status now errors.
	h.res.Envp = statusErrEnvp{h.envp}
	if err := h.e.envState.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.e.envState.Statuses()[id]; got != ports.EnvUnknown {
		t.Fatalf("after probe failure: got %q, want unknown (must NOT carry stale running)", got)
	}
}
