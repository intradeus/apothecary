package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"apothecary/internal/event"
	"apothecary/internal/ports/envport"
	"apothecary/internal/resolution"
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

// D10: a run parked on the holder of a take-over is woken by the holder
// (a message, a release), never by a restart: recovery leaves it parked,
// boot after boot, without touching its log.
func TestRecoveryLeavesARunParkedOnItsHolder(t *testing.T) {
	fl := mustFlow(t, askFlowDoc)
	h := newHarness(t, fl)
	seedRun(t, h, "r-d10", "ask", fl.Hash,
		&event.PhaseStarted{Phase: "main", By: "human"},
		&event.StageStarted{StageID: "build", Iteration: 1},
		&event.HoldTaken{StageID: "build", Iteration: 1, UserID: "u1"},
		&event.StagePaused{StageID: "build", Iteration: 1, Reason: event.PauseHeld},
		&event.RunPaused{StageID: "build", Reason: event.PauseWaiting},
	)
	before := len(h.events(t, "r-d10"))
	for boot := range 2 {
		if err := h.e.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
		h.e.Wait("r-d10")
		if n := len(h.events(t, "r-d10")); n != before {
			t.Fatalf("boot %d appended %v", boot+1, eventTypes(h.events(t, "r-d10"))[before:])
		}
	}
	if v := h.view(t, "r-d10"); v.State != run.WaitingOnHuman || !v.Dormant {
		t.Errorf("state = %s dormant %v, want still parked on the holder", v.State, v.Dormant)
	}
}

// D9: a wait's timeout runs from the park. A crash after phase_resumed and
// before the wait's own result leaves recovery re-parking it — and the
// timeout still runs from the first park, not the re-park.
func TestRecoveredWaitKeepsItsTimeoutFromTheFirstPark(t *testing.T) {
	doc := strings.Replace(waitFlowDoc, "type: wait, for: mr_merged, on_fail: abort", "type: wait, for: mr_merged, timeout: 1h, on_fail: abort", 1)
	ph := newPhasedHarness(t, doc)
	runID := ph.start(t) // parked at the merged wait, at T0
	ph.e.Shutdown()
	// The crash window: the phase resumed, the wait's result not yet recorded.
	if _, err := ph.store.Append(context.Background(), runID, &event.PhaseResumed{Phase: "main", StageID: "merged"}); err != nil {
		t.Fatal(err)
	}
	ph.clock.Advance(30 * time.Minute)

	h2 := newHarnessSource(t, ph.flows, ph.flowOf(t, "phased"))
	h2.store, h2.envp, h2.repoHost = ph.store, ph.envp, ph.repoHost
	h2.res.Envp, h2.res.RepoHost = ph.envp, ph.repoHost
	h2.runner.RunFunc = ph.runner.RunFunc
	h2.res.Runner = h2.runner
	h2.e = New(Deps{Config: Config{MRPollInterval: 5 * time.Millisecond}, Store: ph.store,
		Resolver: resolution.StaticResolver{R: h2.res}, Flows: ph.flows, Settings: h2.settings, Clock: ph.clock.Now})
	t.Cleanup(h2.e.Shutdown)
	if err := h2.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the wait to park again", func() bool {
		return countType(h2.events(t, runID), event.TypePhaseParked) == 2
	})
	// An hour and a minute after the first park: the timeout has run out.
	ph.clock.Advance(31 * time.Minute)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h2.view(t, runID).State != run.Abandoned {
		time.Sleep(5 * time.Millisecond)
	}
	if v := h2.view(t, runID); v.State != run.Abandoned {
		t.Fatalf("state = %s an hour after the first park, want abandoned by the wait's timeout: %v", v.State, eventTypes(h2.events(t, runID)))
	}
	if failed, _ := findPayload[*event.StageFailed](h2.events(t, runID)); failed == nil || failed.StageID != "merged" || failed.Reason != event.ReasonTimeout {
		t.Errorf("failed = %+v", failed)
	}
}
