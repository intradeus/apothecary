package engine

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/run"
)

// overrideDoc has one chain per thing a run can be doing when its merge
// request is merged. Each starts by hand, so a test picks what it interrupts.
const overrideDoc = `name: phased
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: builder
    prompt: build it
  - name: redactor
    prompt: describe it
    outputs:
      - { name: mr, type: file }
main:
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } } }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }
watchers:
  - id: long
    on: manual
    stages:
      - { id: slow, type: command, run: "./slow.sh" }
      - { id: slow_next, type: command, run: "./next.sh" }
  - id: think
    on: manual
    stages:
      - { id: agent_turn, type: agent, harness: $harness, model: $model, agent: builder }
      - { id: think_next, type: command, run: "./next.sh" }
  - id: broken
    on: manual
    stages:
      - { id: bad, type: command, run: "./bad.sh", on_fail: debug }
  - id: list
    on: manual
    stages:
      - { id: load, type: action, action: load_review_comments, output: { threads: { to: artifacts/threads.yaml, type: mr_threads } } }
  - id: ship
    on: manual
    stages:
      - { id: merge, type: action, action: mr_merge }
      - { id: tail, type: command, run: "./tail.sh" }
      - { id: tail_more, type: command, run: "./more.sh" }
outcomes:
  - id: merged
    on: mr_merged
    then: done
    stages:
      - { id: wrap, type: command, run: "./wrap.sh" }
`

func outcomeHarness(t *testing.T) (*phasedHarness, string) {
	t.Helper()
	ph := newPhasedHarness(t, overrideDoc)
	return ph, ph.start(t)
}

// mergeAndEnd merges the merge request and runs the watcher's pass, which must
// carry the run through its outcome to the end.
func (ph *phasedHarness) mergeAndEnd(t *testing.T, runID string) {
	t.Helper()
	ph.repoHost.SetMRState(1, repoport.MRMerged)
	if err := ph.e.OutcomesOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	ph.e.Wait(runID)
	ph.finishedByOutcome(t, runID)
}

func (ph *phasedHarness) finishedByOutcome(t *testing.T, runID string) {
	t.Helper()
	evs := ph.events(t, runID)
	if v := ph.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(evs))
	}
	if n := len(ph.execsOf("./wrap.sh")); n != 1 {
		t.Errorf("the outcome's chain ran %d times: %v", n, eventTypes(evs))
	}
	if !logHas(evs, func(p *event.PhaseStarted) bool { return p.Phase == "merged" }) {
		t.Errorf("the outcome never started: %v", eventTypes(evs))
	}
}

// blockCommand holds script until the test ends or lets go of it.
func (ph *phasedHarness) blockCommand(t *testing.T, script string) (entered *atomic.Bool, release func()) {
	t.Helper()
	entered = &atomic.Bool{}
	held := make(chan struct{})
	release = sync.OnceFunc(func() { close(held) })
	t.Cleanup(release)
	ph.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, script) {
			entered.Store(true)
			<-held
		}
		return envport.ExecResult{}, nil
	}
	return entered, release
}

// An outcome stops a running command: the chain's next stage never runs.
func TestAnOutcomeStopsARunningCommand(t *testing.T) {
	ph, runID := outcomeHarness(t)
	entered, release := ph.blockCommand(t, "./slow.sh")
	if err := ph.e.StartPhase(runID, "long", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, entered.Load)
	ph.repoHost.SetMRState(1, repoport.MRMerged)
	if err := ph.e.OutcomesOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	release()
	ph.e.Wait(runID)
	ph.finishedByOutcome(t, runID)
	if n := len(ph.execsOf("./next.sh")); n != 0 {
		t.Errorf("the stopped chain went on (%d)", n)
	}
}

// An outcome stops a running agent, whose turn is cancelled.
func TestAnOutcomeStopsARunningAgent(t *testing.T) {
	ph, runID := outcomeHarness(t)
	var entered atomic.Bool
	ph.runner.RunFunc = func(ctx context.Context, _ fake.RunCall) (agentport.AgentResult, error) {
		entered.Store(true)
		<-ctx.Done()
		return agentport.AgentResult{}, ctx.Err()
	}
	if err := ph.e.StartPhase(runID, "think", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, entered.Load)
	ph.mergeAndEnd(t, runID)
	if n := len(ph.execsOf("./next.sh")); n != 0 {
		t.Errorf("the stopped chain went on (%d)", n)
	}
}

// An outcome takes a run stopped on an agent's question, and the question is
// withdrawn with the run.
func TestAnOutcomeTakesARunWaitingOnAQuestion(t *testing.T) {
	ph, runID := outcomeHarness(t)
	ph.runner.RunFunc = func(context.Context, fake.RunCall) (agentport.AgentResult, error) {
		if _, err := ph.e.RaisePing(runID, event.PingClarification, "which db?", "", nil, nil); err != nil {
			return agentport.AgentResult{}, err
		}
		return agentport.AgentResult{TurnsUsed: 1, Session: "s"}, nil
	}
	if err := ph.e.StartPhase(runID, "think", ""); err != nil {
		t.Fatal(err)
	}
	asked := ph.waitPing(t, event.PingClarification)
	waitEvent[*event.StagePaused](t, ph.harness, runID)
	ph.e.Wait(runID)
	ph.mergeAndEnd(t, runID)
	for _, p := range run.ReducePings(ph.events(t, runID)) {
		if p.PingID == asked.ID && p.Status != "withdrawn" {
			t.Errorf("the question is %s, want it withdrawn", p.Status)
		}
	}
}

// An outcome takes a run stopped on the thread checklist.
func TestAnOutcomeTakesARunWaitingOnTheChecklist(t *testing.T) {
	ph, runID := outcomeHarness(t)
	ph.repoHost.AddComment(1, "d1", "a.go", "rev", "rename this", ph.clock.Now())
	if err := ph.e.StartPhase(runID, "list", ""); err != nil {
		t.Fatal(err)
	}
	ph.waitPing(t, event.PingSelect)
	waitEvent[*event.RunPaused](t, ph.harness, runID)
	ph.e.Wait(runID)
	ph.mergeAndEnd(t, runID)
}

// An outcome takes a run stopped at the debug leaf.
func TestAnOutcomeTakesARunAtTheDebugLeaf(t *testing.T) {
	ph, runID := outcomeHarness(t)
	ph.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "./bad.sh") {
			return envport.ExecResult{ExitCode: 1, Output: "no"}, nil
		}
		return envport.ExecResult{}, nil
	}
	if err := ph.e.StartPhase(runID, "broken", ""); err != nil {
		t.Fatal(err)
	}
	ph.waitPing(t, PingDebug)
	waitEvent[*event.RunPaused](t, ph.harness, runID)
	ph.e.Wait(runID)
	ph.mergeAndEnd(t, runID)
}

// An outcome overrides a person's pause too.
func TestAnOutcomeTakesAPausedRun(t *testing.T) {
	ph, runID := outcomeHarness(t)
	entered, release := ph.blockCommand(t, "./slow.sh")
	if err := ph.e.StartPhase(runID, "long", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, entered.Load)
	if err := ph.e.Pause(runID); err != nil {
		t.Fatal(err)
	}
	release()
	ph.e.Wait(runID)
	if st := ph.view(t, runID).State; st != run.Paused {
		t.Fatalf("state = %s", st)
	}
	ph.mergeAndEnd(t, runID)
}

// A take-over is the one thing an outcome leaves alone: the person has the
// agent. It fires once they let go.
func TestAnOutcomeLeavesATakeOverAlone(t *testing.T) {
	ph, runID := outcomeHarness(t)
	r := &heldRunner{firstEnd: make(chan struct{})}
	ph.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		r.turn(call.In, "working on it", r.firstEnd)
		return r.drive(ctx, call.In)
	}
	if err := ph.e.StartPhase(runID, "think", ""); err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.AgentSession](t, ph.harness, runID)
	takeOverWhenLive(t, ph.harness, runID, "u1")
	ph.repoHost.SetMRState(1, repoport.MRMerged)
	if err := ph.e.OutcomesOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if logHas(ph.events(t, runID), func(p *event.PhaseStarted) bool { return p.Phase == "merged" }) {
		t.Fatal("the outcome took a run under a take-over")
	}
	close(r.firstEnd)
	if err := ph.e.Release(runID, "u1"); err != nil {
		t.Fatal(err)
	}
	ph.e.Wait(runID)
	ph.finishedByOutcome(t, runID)
}

// A chain that merged the merge request itself goes on to its end: the merge
// is its own doing, and what follows it in the chain is what it is for.
func TestAChainThatMergedGoesOnToItsEnd(t *testing.T) {
	ph, runID := outcomeHarness(t)
	entered, release := ph.blockCommand(t, "./tail.sh")
	ph.repoHost.SetHead(1, "h1")
	if err := ph.e.StartPhase(runID, "ship", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, entered.Load)
	if err := ph.e.OutcomesOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	release()
	ph.e.Wait(runID)
	ph.finishedByOutcome(t, runID)
	if n := len(ph.execsOf("./more.sh")); n != 1 {
		t.Errorf("the outcome cut off the chain that merged: the stage after ran %d times", n)
	}
}

// The daemon's loop does this without anyone asking.
func TestRunOutcomesActsOnItsOwn(t *testing.T) {
	ph, runID := outcomeHarness(t)
	var entered atomic.Bool
	ph.runner.RunFunc = func(ctx context.Context, _ fake.RunCall) (agentport.AgentResult, error) {
		entered.Store(true)
		<-ctx.Done()
		return agentport.AgentResult{}, ctx.Err()
	}
	if err := ph.e.StartPhase(runID, "think", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, entered.Load)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ph.e.RunOutcomes(ctx)
	ph.repoHost.SetMRState(1, repoport.MRMerged)
	ph.waitState(t, runID, run.Completed)
	ph.finishedByOutcome(t, runID)
}

// An idle run is its own loop's business, and a run with nothing merged is
// nobody's: the pass does nothing to either.
func TestThePassLeavesAnIdleRunToItsLoop(t *testing.T) {
	ph, runID := outcomeHarness(t)
	before := len(ph.events(t, runID))
	if err := ph.e.OutcomesOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := len(ph.events(t, runID)); after != before {
		t.Errorf("the pass appended %d events", after-before)
	}
}
