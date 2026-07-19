package engine

import (
	"context"
	"testing"
	"time"

	"factory/internal/event"
	"factory/internal/ports"
	"factory/internal/run"
)

func TestReapOnceDestroysOldCompletedOnly(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	// Completed run with a live env.
	doneID, err := h.e.CreateRun(ctx, Ticket{Title: "done"}, "loop")
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
		&event.RunCreated{TicketTitle: "ab", FlowName: "loop"},
		&event.EnvRequested{}, &event.EnvReady{EnvID: string(envID)},
		&event.RunAbandoned{StalledAt: "test", Reason: "edge exhausted"},
	} {
		if _, err := h.store.Append(ctx, "r-ab", p); err != nil {
			t.Fatal(err)
		}
	}

	// Inside the window: nothing reaped.
	if err := h.e.ReapOnce(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if len(h.envp.Destroyed) != 0 {
		t.Fatalf("reaped inside window: %v", h.envp.Destroyed)
	}

	// Outside the window: only the completed run's env goes.
	time.Sleep(5 * time.Millisecond)
	if err := h.e.ReapOnce(ctx, time.Millisecond); err != nil {
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
	if err := h.e.ReapOnce(ctx, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if len(h.envp.Destroyed) != 1 {
		t.Errorf("second pass destroyed more: %v", h.envp.Destroyed)
	}
}
