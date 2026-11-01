package engine

// A run has one cursor, always (docs/flow-language.md §1): one chain holds
// it, moving or parked at a gate or a wait, or it stands at the hub. Only an
// outcome takes it from a chain, and a take-over keeps it even from an
// outcome. A confirmation asked at the hub does not hold it.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

var ticketT = runinput.Ticket{Title: "Phased"}

// checkOneCursor walks a log: a chain opens only at the hub, unless it is an
// outcome taking the cursor, and every stage starts inside the open chain.
func checkOneCursor(t *testing.T, fl *flow.Flow, evs []event.Event) {
	t.Helper()
	open := ""
	for _, ev := range evs {
		switch p := ev.Payload.(type) {
		case *event.PhaseStarted:
			ph := fl.Phase(p.Phase)
			if open != "" && (ph == nil || !ph.IsOutcome()) {
				t.Errorf("seq %d: %s opened while %s held the cursor: %v", ev.Seq, p.Phase, open, eventTypes(evs))
			}
			open = p.Phase
		case *event.PhaseCompleted:
			if p.Phase != open {
				t.Errorf("seq %d: %s completed while %s held the cursor", ev.Seq, p.Phase, open)
			}
			open = ""
		case *event.StageStarted:
			st := fl.Stage(p.StageID)
			if st == nil {
				continue // a provisioning log, not a stage of the flow
			}
			if ph := fl.PhaseOf(st); ph != nil && ph.ID != open {
				t.Errorf("seq %d: stage %s of %s ran while %q held the cursor", ev.Seq, p.StageID, ph.ID, open)
			}
		}
	}
}

// Every watcher that can open on the same tick still opens one at a time.
func TestWatchersThatHoldTogetherTakeTurns(t *testing.T) {
	const doc = `name: phased
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: redactor
    prompt: describe it
    outputs:
      - { name: mr, type: file }
main:
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } } }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }
watchers:
  - id: first
    on: { signal: mr_head_moved, max: 1 }
    stages:
      - { id: one, type: command, run: "./one.sh" }
      - { id: one_b, type: command, run: "./one_b.sh" }
  - id: second
    on: { signal: mr_mergeable, max: 1 }
    stages:
      - { id: two, type: command, run: "./two.sh" }
outcomes:
  - { id: shipped, on: mr_merged, then: done }
`
	ph := newPhasedHarness(t, doc)
	runID := ph.start(t)
	ph.repoHost.SetHead(1, "someone-else")
	ph.repoHost.SetMergeableNow(1, true)
	ph.waitPhaseCompleted(t, runID, "first", 1)
	ph.waitPhaseCompleted(t, runID, "second", 1)
	ph.repoHost.SetMRState(1, repoport.MRMerged)
	ph.waitState(t, runID, run.Completed)
	checkOneCursor(t, ph.flowOf(t, "phased"), ph.events(t, runID))
}

// A confirmation is a question at the hub, not a chain: the run stays free
// for another chain while it is open, and the question is still there when
// that chain ends.
func TestAConfirmationHoldsNoCursor(t *testing.T) {
	ph := newPhasedHarness(t, strings.Replace(phasedFlowDoc, "watchers:\n", `watchers:
  - id: by_hand
    on: manual
    stages:
      - { id: hand, type: command, run: "./hand.sh" }
`, 1))
	runID := ph.start(t)
	ph.repoHost.SetTargetSHA(1, "t1")
	ph.repoHost.SetMergeable(1, repoport.MergeConflicts, "h1")
	ask := ph.waitPing(t, event.PingPhaseConfirm)
	ph.waitIdle(t, runID)

	if err := ph.e.StartPhase(runID, "by_hand", ""); err != nil {
		t.Fatalf("a chain while a confirmation is open: %v", err)
	}
	ph.waitPhaseCompleted(t, runID, "by_hand", 1)
	if len(ph.execsOf("./sync.sh")) != 0 {
		t.Fatal("the watcher opened on its own question")
	}
	still := false
	for _, p := range ph.e.PendingPings() {
		still = still || p.ID == ask.ID
	}
	if !still {
		t.Fatal("the confirmation closed when another chain ran")
	}
	if err := ph.e.AnswerPing(ask.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	ph.waitPhaseCompleted(t, runID, "conflicts", 1)
	checkOneCursor(t, ph.flowOf(t, "phased"), ph.events(t, runID))
}

// An outcome ends the run with a confirmation still open: the question goes
// with the run, and nothing a late answer could do is left.
func TestAnOutcomeWithdrawsAnOpenConfirmation(t *testing.T) {
	ph := newPhasedHarness(t, phasedFlowDoc)
	runID := ph.start(t)
	ph.repoHost.SetTargetSHA(1, "t1")
	ph.repoHost.SetMergeable(1, repoport.MergeConflicts, "h1")
	ask := ph.waitPing(t, event.PingPhaseConfirm)
	ph.repoHost.SetMRState(1, repoport.MRMerged)
	ph.waitState(t, runID, run.Completed)
	ph.e.Wait(runID)
	for _, p := range ph.e.PendingPings() {
		if p.ID == ask.ID {
			t.Fatal("a question outlived its run")
		}
	}
	if err := ph.e.AnswerPing(ask.ID, PingAnswer{Answer: "approve"}); err == nil {
		t.Error("a withdrawn question took an answer")
	}
	if len(ph.execsOf("./sync.sh")) != 0 {
		t.Error("the watcher ran after the run ended")
	}
	checkOneCursor(t, ph.flowOf(t, "phased"), ph.events(t, runID))
}

// The confirmation of an outcome that says start: confirm waits for the hub:
// it never interrupts a chain.
func TestAConfirmedOutcomeWaitsForTheHub(t *testing.T) {
	ph := newPhasedHarness(t, strings.Replace(mainGateDoc(`{ id: final, type: human_gate, question: "Merge?" }`),
		"on: mr_merged\n    then: done", "on: { signal: mr_merged, start: confirm }\n    then: done", 1))
	runID, err := ph.e.CreateRun(context.Background(), ticketT, "phased", testProject)
	if err != nil {
		t.Fatal(err)
	}
	gate := ph.waitPing(t, event.PingGate)
	ph.repoHost.SetMRState(1, repoport.MRMerged)
	if err := ph.e.OutcomesOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if logHas(ph.events(t, runID), func(p *event.PhaseStarted) bool { return p.Phase == "shipped" }) {
		t.Fatal("an outcome that asks first took a parked chain")
	}
	if err := ph.e.AnswerPing(gate.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	ask := ph.waitPing(t, event.PingPhaseConfirm, gate.ID)
	if err := ph.e.AnswerPing(ask.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	ph.waitState(t, runID, run.Completed)
	checkOneCursor(t, ph.flowOf(t, "phased"), ph.events(t, runID))
}

// The outcome pass reads every run and skips the one it cannot read: one
// broken run does not stop the others' ends.
func TestOutcomePassSkipsWhatItCannotRead(t *testing.T) {
	ctx := context.Background()
	t.Run("the host cannot be read", func(t *testing.T) {
		ph, runID := outcomeHarness(t)
		ph.repoHost.SetMRStateErr(errors.New("host down"))
		if err := ph.e.StartPhase(runID, "long", ""); err != nil {
			t.Fatal(err)
		}
		if err := ph.e.OutcomesOnce(ctx); err != nil {
			t.Fatalf("one run's failure failed the pass: %v", err)
		}
		ph.repoHost.SetMRStateErr(nil)
		ph.repoHost.SetMRState(1, repoport.MRMerged)
		ph.waitState(t, runID, run.Completed)
	})
	t.Run("the views cannot be listed", func(t *testing.T) {
		ph, runID := outcomeHarness(t)
		ph.e.runs = &run.Reader{Store: failingViews{Store: ph.store, err: errors.New("db down")}}
		wantErrText(t, ph.e.OutcomesOnce(ctx), "db down")
		ph.e.runs = &run.Reader{Store: ph.store}
		ph.repoHost.SetMRState(1, repoport.MRMerged)
		ph.waitState(t, runID, run.Completed)
	})
	t.Run("a flow that no longer loads", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, `name: phased
version: 5
operates_on: merge_request
parameters: { harness: fake, model: fake/model }
main:
  - { id: first, type: command, run: "./first.sh" }
outcomes:
  - { id: shipped, on: mr_merged, then: done }
`))
		h.seedMRRun(t, "r1", "phased", "first")
		h.seed(t, "r1", &event.RunPaused{StageID: "first", Reason: event.PauseUser})
		h.e.flows = funcSource(func(string) (*flow.Flow, error) { return nil, errors.New("db down") })
		wantErrText(t, h.e.fireOutcome(ctx, h.view(t, "r1")), "db down")
	})
}

// What an outcome's signal needs is read: an approval or a check the host
// cannot answer fails the pass for that run, it is not a quiet "no".
func TestOutcomeReadsWhatItsSignalNeeds(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ signal, want string }{
		{"mr_approved", "approval"},
		{"mr_mergeable", ""},
	} {
		t.Run(c.signal, func(t *testing.T) {
			h := newHarness(t, mustFlow(t, fmt.Sprintf(`name: phased
version: 5
operates_on: merge_request
parameters: { harness: fake, model: fake/model }
main:
  - { id: first, type: command, run: "./first.sh" }
outcomes:
  - { id: shipped, on: %s, then: done }
`, c.signal)))
			h.seedMRRun(t, "r1", "phased", "first")
			h.seed(t, "r1", &event.RunPaused{StageID: "first", Reason: event.PauseUser})
			h.res.RepoHost = &faultReads{RepoHost: h.repoHost, err: errors.New("host down")}
			err := h.e.fireOutcome(ctx, h.view(t, "r1"))
			if c.want == "" {
				if err != nil {
					t.Errorf("err = %v", err)
				}
				return
			}
			wantErrText(t, err, c.want)
		})
	}
	t.Run("the status itself", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, `name: phased
version: 5
operates_on: merge_request
parameters: { harness: fake, model: fake/model }
main:
  - { id: first, type: command, run: "./first.sh" }
outcomes:
  - { id: shipped, on: mr_merged, then: done }
`))
		h.seedMRRun(t, "r1", "phased", "first")
		h.seed(t, "r1", &event.RunPaused{StageID: "first", Reason: event.PauseUser})
		h.repoHost.SetMRStateErr(errors.New("host down"))
		wantErrText(t, h.e.fireOutcome(ctx, h.view(t, "r1")), "host down")
		// A merge request no integration reaches is passed over: the run has
		// no fate to read there.
		h.repoHost.SetMRStateErr(nil)
		h.res.RepoHost = nil
		if err := h.e.fireOutcome(ctx, h.view(t, "r1")); err != nil {
			t.Errorf("err = %v", err)
		}
	})
}

type faultReads struct {
	*fake.RepoHost
	err error
}

func (f *faultReads) MRApproval(context.Context, repoport.MRRef) (repoport.Approval, error) {
	return repoport.Approval{}, f.err
}

func (f *faultReads) MRChecks(context.Context, repoport.MRRef, string) (repoport.Checks, error) {
	return repoport.Checks{}, f.err
}

// An agent under a take-over can still ask its own question: the turn ends on
// it, the attempt parks on the question with the hold in force, an outcome
// leaves it alone, and the answer resumes the same session under the hold.
func TestAQuestionUnderATakeOverParksWithTheHoldInForce(t *testing.T) {
	ph := newPhasedHarness(t, overrideDoc)
	runID := ph.start(t)
	r := &heldRunner{}
	asked := make(chan struct{})
	ph.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s"})
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "T1"})
		<-asked
		if _, err := ph.e.RaisePing(runID, event.PingClarification, "which one?", "", nil, nil); err != nil {
			return agentport.AgentResult{}, err
		}
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "T1", StopReason: "end_turn"})
		<-ctx.Done()
		return agentport.AgentResult{Session: "s"}, ctx.Err()
	}
	var resumed atomic.Int32
	ph.runner.ResumeFunc = func(ctx context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
		// From its first instruction: a turn that ends before the engine
		// said so would end the stage under the holder.
		if !call.In.Inbox.Held() {
			t.Error("the session resumed without its take-over")
		}
		resumed.Add(1)
		r.turn(call.In, "with the answer", nil)
		return r.drive(ctx, call.In)
	}
	if err := ph.e.StartPhase(runID, "think", ""); err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.AgentSession](t, ph.harness, runID)
	takeOverWhenLive(t, ph.harness, runID, "u1")
	close(asked)
	p := ph.waitPing(t, event.PingClarification)
	waitUntil(t, "the run to park on its question", func() bool { return ph.view(t, runID).Dormant })
	if v := ph.view(t, runID); v.Hold == nil || v.State != run.WaitingOnHuman {
		t.Fatalf("parked = %s, hold %+v", v.State, v.Hold)
	}

	ph.repoHost.SetMRState(1, repoport.MRMerged)
	if err := ph.e.OutcomesOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if logHas(ph.events(t, runID), func(p *event.PhaseStarted) bool { return p.Phase == "merged" }) {
		t.Fatal("an outcome took a run under a take-over")
	}

	if err := ph.e.AnswerPing(p.ID, textAnswer("the second")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the resume", func() bool { return resumed.Load() == 1 })
	takeBack := func() bool { return ph.e.Release(runID, "u1") == nil }
	waitUntil(t, "the release", takeBack)
	ph.e.Wait(runID)
	ph.finishedByOutcome(t, runID)
	if rel, ok := findPayload[*event.HoldReleased](ph.events(t, runID)); !ok || rel.UserID != "u1" {
		t.Errorf("released = %+v", rel)
	}
}
