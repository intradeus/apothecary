package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
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
func seedRun(t *testing.T, h *harness, runID, flowName, hash string, payloads ...event.Payload) envport.EnvID {
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
	return envID
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
	deadline := time.Now().Add(5 * time.Second)
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

// D1: the crash hit after an agent stage's stage_completed and before its
// routing. Recovery routes it the way the live loop would have: the verdict
// is recorded (once), the budget is checked, then on_success or on_reject.
func TestRecoveryAfterCompletionRecordsTheVerdictAndChecksTheBudget(t *testing.T) {
	const doc = `name: rv
version: 2
parameters: { harness: fake, model: fake/model, max_cost_usd: 1.0 }
main:
  - { id: review, type: agent, harness: $harness, model: $model, agent: reviewer, output: { verdict: { to: artifacts/verdict.json, type: verdict } } }
`
	recoverAt := func(t *testing.T, cost float64) (*harness, []event.Event) {
		fl := mustFlow(t, doc)
		h := newHarness(t, fl)
		env := seedRun(t, h, "r-d1", "rv", fl.Hash,
			&event.PhaseStarted{Phase: "main", By: "human"},
			&event.StageStarted{StageID: "review", Iteration: 1},
			&event.StageCompleted{StageID: "review", TurnsUsed: 1, Usage: &event.Usage{CostUSD: cost}},
		)
		h.writeFile(t, env, "artifacts/verdict.json", `{"verdict": "APPROVE", "summary": "fine"}`)
		if err := h.e.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		settled(t, h, "r-d1")
		h.e.Wait("r-d1")
		return h, h.events(t, "r-d1")
	}
	t.Run("over budget", func(t *testing.T) {
		_, evs := recoverAt(t, 5)
		ab, _ := findPayload[*event.RunAbandoned](evs)
		if ab == nil || !strings.Contains(ab.Reason, "budget exceeded") {
			t.Errorf("run_abandoned = %+v, want abandoned over the budget: %v", ab, eventTypes(evs))
		}
	})
	t.Run("within budget", func(t *testing.T) {
		h, evs := recoverAt(t, 0.1)
		if n := countType(evs, event.TypeVerdictReceived); n != 1 {
			t.Errorf("verdict_received = %d, want 1: %v", n, eventTypes(evs))
		}
		if v := h.view(t, "r-d1"); v.State != run.Completed {
			t.Errorf("state = %s: %v", v.State, eventTypes(evs))
		}
	})
}

// A retry that crashed before its prompt begins now, in the stage's session:
// it gets its whole prompt, rejection included, on a clean slate, not a
// "carry on" that would lose them.
func TestARetryCrashedBeforeItsPromptGetsItsPrompt(t *testing.T) {
	retried := retryWorld(t, []event.Payload{&event.StageStarted{StageID: "plan", Iteration: 2}})
	if retried.stale {
		t.Error("the attempt started on the rejected attempt's output")
	}
}

// The same retry crashed just after its prompt was logged: nothing says the
// agent read it, so it is sent again rather than told to carry on.
func TestARetryCrashedAfterItsPromptGetsItAgain(t *testing.T) {
	retryWorld(t, []event.Payload{
		&event.StageStarted{StageID: "plan", Iteration: 2},
		&event.AgentPrompted{StageID: "plan", Iteration: 2, Kind: event.PromptStage, Text: "plan it, better"},
	})
}

type retried struct{ stale bool }

// retryWorld recovers a run rejected at its gate, its log ending in tail, and
// checks the second attempt went into the first one's session with the
// rejection.
func retryWorld(t *testing.T, tail []event.Payload) retried {
	t.Helper()
	fl := mustFlow(t, strings.Replace(gatedRecoveryDoc, "agent: planner,",
		"agent: planner, input: { rejection: { from: edge.verdict, type: verdict, optional: true } },", 1))
	h := newHarness(t, fl)
	var stale atomic.Bool
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		_, err := h.envp.ReadFile(context.Background(), call.Env, "artifacts/plan.md")
		stale.Store(err == nil)
		h.writeFile(t, call.Env, "artifacts/plan.md", "the new plan\n")
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}
	env := seedRun(t, h, "r-d11", "gated", fl.Hash, append([]event.Payload{
		&event.PhaseStarted{Phase: "main", By: "human"},
		&event.StageStarted{StageID: "plan", Iteration: 1},
		&event.AgentPrompted{StageID: "plan", Iteration: 1, Kind: event.PromptStage, Text: "plan it"},
		&event.StageCompleted{StageID: "plan", TurnsUsed: 1, SessionID: "first-attempt"},
		&event.StageStarted{StageID: "plan_gate", Iteration: 1},
		&event.PingRaised{PingID: "ping-d11", Type: PingGate, Question: "Approve the plan?", StageID: "plan_gate"},
		&event.PingAnswered{PingID: "ping-d11", Answer: "reject", Feedback: "too vague"},
		&event.EdgeTaken{From: "plan_gate", Edge: flow.EdgeReject, To: "plan", Feed: "too vague"},
	}, tail...)...)
	h.writeFile(t, env, "artifacts/plan.md", "the rejected plan\n")
	if err := h.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.waitPing(t, PingGate, "ping-d11")
	calls := h.runner.ResumeCalls
	if len(calls) != 1 || calls[0].Session != "first-attempt" {
		t.Fatalf("resumes = %+v, want the attempt in the stage's session, once", calls)
	}
	if !strings.Contains(calls[0].Reply, "too vague") {
		t.Errorf("the retry's prompt lost the rejection:\n%s", calls[0].Reply)
	}
	return retried{stale: stale.Load()}
}

// Likewise a command stage: a re-execution after a crash starts from a clean
// slate, or the output check passes on what an earlier attempt wrote.
func TestARecoveredCommandDoesNotPassOnAStaleOutput(t *testing.T) {
	doc := `name: stale
version: 5
parameters: { harness: fake, model: fake/model }
main:
  - { id: gen, type: command, run: "./gen.sh", output: { report: { to: artifacts/report.md, type: file } }, on_fail: abort }
`
	fl := mustFlow(t, doc)
	h := newHarness(t, fl)
	env := seedRun(t, h, "r-stale", "stale", fl.Hash,
		&event.PhaseStarted{Phase: "main", By: "human"},
		&event.StageStarted{StageID: "gen", Iteration: 2},
	)
	h.writeFile(t, env, "artifacts/report.md", "an earlier attempt's report\n")
	if err := h.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	settled(t, h, "r-stale")
	if v := h.view(t, "r-stale"); v.State != run.Abandoned {
		t.Errorf("state = %s, want abandoned: the script wrote nothing: %v", v.State, eventTypes(h.events(t, "r-stale")))
	}
}
