package engine

import (
	"context"
	"testing"
	"time"

	"apothecary/internal/event"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
)

// Crash recovery regressions: each test seeds the log a crash would leave
// and checks that Recover takes the run on from exactly there.

const gatedRecoveryDoc = `name: gated
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: plan,      type: agent, harness: $harness, model: $model, agent: planner, output: { plan: { to: artifacts/plan.md, type: file } }, on_success: plan_gate }
  - { id: plan_gate, type: human_gate, question: "Approve the plan?", input: { doc: { from: artifacts/plan.md, type: file } }, on_success: done,
      on_reject: { to: plan, max: 3 } }
`

// seedRun appends payloads to runID's log after a run_created for flow fl,
// with a live workspace.
func seedRun(t *testing.T, h *harness, runID, flowName, hash string, payloads ...event.Payload) {
	t.Helper()
	ctx := context.Background()
	envID, err := h.envp.Provision(ctx, envport.EnvSpec{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	head := []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: flowName, ProjectID: testProject, FlowHash: hash, Branch: "apothecary/" + runID},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: string(envID)},
	}
	for _, p := range append(head, payloads...) {
		if _, err := h.store.Append(ctx, runID, p); err != nil {
			t.Fatal(err)
		}
	}
}

// D8: a gate answered while its phase was parked, the phase resumed, and the
// crash hit before the routing was recorded. The answer is on the log;
// recovery routes by it instead of asking the question again.
func TestRecoveredResumeRoutesTheRecordedGateAnswer(t *testing.T) {
	fl := mustFlow(t, gatedRecoveryDoc)
	h := newHarness(t, fl)
	seedRun(t, h, "r-d8", "gated", fl.Hash,
		&event.PhaseStarted{Phase: "main", By: "human"},
		&event.StageStarted{StageID: "plan", Iteration: 1},
		&event.StageCompleted{StageID: "plan", TurnsUsed: 1},
		&event.StageStarted{StageID: "plan_gate", Iteration: 1},
		&event.PingRaised{PingID: "ping-d8", Type: PingGate, Question: "Approve the plan?", StageID: "plan_gate", Files: []string{"artifacts/plan.md"}},
		&event.PhaseParked{Phase: "main", StageID: "plan_gate", PingID: "ping-d8"},
		&event.PingAnswered{PingID: "ping-d8", Answer: "approve"},
		&event.PhaseResumed{Phase: "main", StageID: "plan_gate"},
	)
	if err := h.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	settled(t, h, "r-d8")
	evs := h.events(t, "r-d8")
	if n := countType(evs, event.TypePingRaised); n != 1 {
		t.Errorf("ping_raised = %d, want the one already answered: %v", n, eventTypes(evs))
	}
	if v := h.view(t, "r-d8"); v.State != run.Completed {
		t.Errorf("state = %s, want completed by the recorded approval: %v", v.State, eventTypes(evs))
	}
}

// settled waits (bounded) for the run to end or to ask a second question —
// a run that wrongly re-asks parks on it and never ends by itself.
func settled(t *testing.T, h *harness, runID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs := h.events(t, runID)
		if run.Reduce(evs).State.Terminal() || countType(evs, event.TypePingRaised) > 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}
