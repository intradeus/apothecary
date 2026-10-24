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
	"apothecary/internal/flow"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// phasedFlowDoc is a ticket's whole life: the main chain opens the merge
// request; the watchers conflicts (confirmed by a human) and review_fixes
// (after a quiet hour, at most twice) react to it.
const phasedFlowDoc = `name: phased
version: 5
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
agents:
  - name: builder
    prompt: build it
    inputs:
      - { name: failure, optional: true }
  - name: redactor
    prompt: describe it
    outputs:
      - { name: mr, path: artifacts/mr.md }
main:
  - { id: build, type: agent, agent: builder }
  - { id: redact, type: agent, agent: redactor }
  - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }
watchers:
  - id: conflicts
    on: { signal: mr_conflicts, start: confirm }
    stages:
      - { id: fix_conflicts, type: command, run: "./sync.sh" }
  - id: review_fixes
    on: { signal: mr_review_settled, quiet_for: 1h, max: 2 }
    stages:
      - { id: fix, type: agent, agent: builder, input: { failure: $FAILURE }, on_fail: { to: fix, max: 1 } }
      - { id: post, type: command, run: "./post.sh" }
outcomes:
  - id: shipped
    on: mr_merged
    then: done
`

// offsetClock is wall time plus a manually advanced offset: quiet periods
// can be crossed instantly while every real-time wait still works.
type offsetClock struct{ off atomic.Int64 }

func (c *offsetClock) Now() time.Time          { return time.Now().Add(time.Duration(c.off.Load())) }
func (c *offsetClock) Advance(d time.Duration) { c.off.Add(int64(d)) }

type phasedHarness struct {
	*harness
	clock   *offsetClock
	mu      sync.Mutex
	prompts map[string][]string
	// failFix makes the next N fix invocations fail (per-round budgets).
	failFix atomic.Int32
}

func newPhasedHarness(t *testing.T, doc string) *phasedHarness {
	t.Helper()
	h := newHarness(t, mustFlow(t, doc))
	ph := &phasedHarness{harness: h, clock: &offsetClock{}, prompts: map[string][]string{}}
	h.e.clock = ph.clock.Now
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (ports.AgentResult, error) {
		// The fake carries no stage id: a watcher's prompt is the one
		// with a SUBJECT section.
		stage := call.Spec.Agent
		if strings.Contains(call.In.Prompt, "# SUBJECT") {
			stage = "fix"
		}
		ph.mu.Lock()
		ph.prompts[stage] = append(ph.prompts[stage], call.In.Prompt)
		ph.mu.Unlock()
		if call.Spec.Agent == "redactor" {
			h.writeFile(t, call.Env, "artifacts/mr.md", "# Phased\n")
		}
		if stage == "fix" && ph.failFix.Load() > 0 {
			ph.failFix.Add(-1)
			return ports.AgentResult{}, errors.New("harness hiccup")
		}
		return ports.AgentResult{TurnsUsed: 1, Session: "s"}, nil
	}
	return ph
}

func (ph *phasedHarness) promptsOf(stage string) []string {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	return append([]string(nil), ph.prompts[stage]...)
}

// start files the ticket and waits for the main chain to leave the run in
// review.
func (ph *phasedHarness) start(t *testing.T) string {
	t.Helper()
	runID, err := ph.e.CreateRun(context.Background(), Ticket{Title: "Phased"}, "phased", testProject)
	if err != nil {
		t.Fatal(err)
	}
	ph.waitState(t, runID, run.InReview)
	return runID
}

func (ph *phasedHarness) waitState(t *testing.T, runID string, want run.State) run.View {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var v run.View
	for time.Now().Before(deadline) {
		v = ph.view(t, runID)
		if v.State == want {
			return v
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("run state = %s, want %s; failures: %s", v.State, want, failures(ph.events(t, runID)))
	return v
}

// waitPhaseRuns waits until the run has completed n runs of phase.
func (ph *phasedHarness) waitPhaseRuns(t *testing.T, runID, phase string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := 0
		for _, e := range ph.events(t, runID) {
			if c, ok := e.Payload.(*event.PhaseCompleted); ok && c.Phase == phase {
				got++
			}
		}
		if got >= n && ph.view(t, runID).State == run.InReview {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("phase %s did not complete %d time(s); events: %v", phase, n, eventTypes(ph.events(t, runID)))
}

// waitPhaseCompleted waits until phase has completed n times, whatever the
// run's state reads (a gate parked elsewhere keeps it waiting_on_human).
func (ph *phasedHarness) waitPhaseCompleted(t *testing.T, runID, phase string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := 0
		for _, e := range ph.events(t, runID) {
			if c, ok := e.Payload.(*event.PhaseCompleted); ok && c.Phase == phase {
				got++
			}
		}
		if got >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("phase %s did not complete %d time(s); events: %v", phase, n, eventTypes(ph.events(t, runID)))
}

func phaseStarts(evs []event.Event) []*event.PhaseStarted {
	var out []*event.PhaseStarted
	for _, e := range evs {
		if p, ok := e.Payload.(*event.PhaseStarted); ok {
			out = append(out, p)
		}
	}
	return out
}

func (ph *phasedHarness) execsOf(script string) []ports.Command {
	var out []ports.Command
	for _, c := range ph.envp.Execs() {
		if strings.Contains(c.Cmd.Script, script) {
			out = append(out, c.Cmd)
		}
	}
	return out
}

// The main chain ends with the merge request open: the run is in review,
// not completed, and it completes when the merge request merges (R9).
func TestPhasedRunWaitsInReviewAndCompletesOnMerge(t *testing.T) {
	ph := newPhasedHarness(t, phasedFlowDoc)
	runID := ph.start(t)
	v := ph.view(t, runID)
	if !v.Idle || v.Phase != flow.MainPhase || len(v.MRs) != 1 {
		t.Fatalf("view = idle %v phase %q mrs %d", v.Idle, v.Phase, len(v.MRs))
	}
	starts := phaseStarts(ph.events(t, runID))
	if len(starts) != 1 || starts[0].Phase != flow.MainPhase || starts[0].By != "human" {
		t.Errorf("phase_started = %+v", starts)
	}
	ph.repoHost.SetMRState(1, ports.MRMerged)
	ph.waitState(t, runID, run.Completed)
}

// The run's end is the flow's business: a close abandons the run through
// the wait on the merge, whose on_fail says abort.
func TestParkedWaitFailsWhenTheMRIsClosed(t *testing.T) {
	ph := newPhasedHarness(t, waitFlowDoc)
	runID := ph.start(t)
	ph.repoHost.SetMRState(1, ports.MRClosed)
	v := ph.waitState(t, runID, run.Abandoned)
	ab, _ := findPayload[*event.RunAbandoned](ph.events(t, runID))
	if ab == nil || !strings.Contains(ab.Reason, "closed unmerged") || !strings.Contains(ab.Reason, "aborted by flow") {
		t.Errorf("abandon = %+v (view %s)", ab, v.State)
	}
	if _, parked := findPayload[*event.PhaseParked](ph.events(t, runID)); !parked {
		t.Error("the wait never parked")
	}
}

// The run completes as soon as nothing is armed — here the moment the main
// chain ends, since every other phase (the watchers, the outcome) starts by
// hand.
func TestRunCompletesWhenNothingIsArmed(t *testing.T) {
	doc := strings.Replace(phasedFlowDoc, "on: { signal: mr_conflicts, start: confirm }", "on: manual", 1)
	doc = strings.Replace(doc, "on: { signal: mr_review_settled, quiet_for: 1h, max: 2 }", "on: manual", 1)
	doc = strings.Replace(doc, "on: mr_merged\n    then: done", "on: { signal: mr_merged, start: manual }\n    then: done", 1)
	ph := newPhasedHarness(t, doc)
	runID, err := ph.e.CreateRun(context.Background(), Ticket{Title: "Phased"}, "phased", testProject)
	if err != nil {
		t.Fatal(err)
	}
	v := ph.waitState(t, runID, run.Completed)
	if len(v.MRs) != 1 {
		t.Errorf("merge requests = %d", len(v.MRs))
	}
}

// A document with nothing but a main chain is one phase, "main": the run
// opens it, ends it, and completes.
func TestStagesOnlyDocumentRunsAsOnePhase(t *testing.T) {
	doc := "name: short\nversion: 5\nparameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }\nmain:\n  - { id: s, type: command, run: \"true\" }\n"
	ph := newPhasedHarness(t, doc)
	runID, err := ph.e.CreateRun(context.Background(), Ticket{Title: "Short"}, "short", testProject)
	if err != nil {
		t.Fatal(err)
	}
	ph.waitState(t, runID, run.Completed)
	starts := phaseStarts(ph.events(t, runID))
	done, _ := findPayload[*event.PhaseCompleted](ph.events(t, runID))
	if len(starts) != 1 || starts[0].Phase != flow.MainPhase || starts[0].By != "human" || done == nil || done.Phase != flow.MainPhase {
		t.Errorf("starts %+v completed %+v", starts, done)
	}
}

// waitFlowDoc ends the main chain with the wait on the merge: the chain parks
// there, watchers run meanwhile, and a close routes to abort.
var waitFlowDoc = strings.Replace(phasedFlowDoc,
	"  - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }\n",
	"  - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }\n  - { id: merged, type: wait, for: mr_merged, on_fail: abort }\n  - { id: after, type: command, run: \"./after.sh\" }\n", 1)

// A wait parks its phase: the run is idle and in review, a watcher runs in
// the meantime and gives the workspace back, and the merge resumes the
// phase, which finishes its stages before the run completes.
func TestWaitParksAndWatchersRunMeanwhile(t *testing.T) {
	ph := newPhasedHarness(t, waitFlowDoc)
	runID := ph.start(t)
	infos, err := ph.e.RunPhases(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if d := infos[0]; d.ID != flow.MainPhase || d.Kind != string(flow.PhaseMain) || d.Live == nil || d.Live.ParkedAt != "merged" {
		t.Fatalf("main = %+v", d)
	}
	ph.repoHost.SetTargetSHA(1, "t1")
	ph.repoHost.SetMergeable(1, ports.MergeConflicts, "h1")
	ask := ph.waitPing(t, event.PingPhaseConfirm)
	if err := ph.e.AnswerPing(ask.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	ph.waitPhaseRuns(t, runID, "conflicts", 1)
	if n := len(ph.execsOf("./after.sh")); n != 0 {
		t.Fatalf("the parked phase went on before the merge (%d runs of after)", n)
	}
	ph.repoHost.SetMRState(1, ports.MRMerged)
	ph.waitState(t, runID, run.Completed)
	if n := len(ph.execsOf("./after.sh")); n != 1 {
		t.Errorf("after ran %d times, want once after the merge", n)
	}
	var parked, resumed int
	for _, e := range ph.events(t, runID) {
		switch e.Payload.(type) {
		case *event.PhaseParked:
			parked++
		case *event.PhaseResumed:
			resumed++
		}
	}
	if parked != 1 || resumed != 1 {
		t.Errorf("parked %d resumed %d", parked, resumed)
	}
	if err := ph.e.StartPhase(runID, flow.MainPhase, ""); !errors.Is(err, ErrPhaseNotStartable) {
		t.Errorf("start after the end: %v", err)
	}
}

// A hand start is refused for a phase whose run is parked.
func TestHumanStartRefusesAParkedPhase(t *testing.T) {
	ph := newPhasedHarness(t, waitFlowDoc)
	runID := ph.start(t)
	if err := ph.e.StartPhase(runID, flow.MainPhase, ""); !errors.Is(err, ErrPhaseNotStartable) || !strings.Contains(err.Error(), "parked at merged") {
		t.Errorf("parked phase by hand: %v", err)
	}
}

// A wait's timeout runs from the park and routes on_fail when the stage
// declares no on_timeout.
func TestParkedWaitTimesOut(t *testing.T) {
	doc := strings.Replace(waitFlowDoc, "type: wait, for: mr_merged, on_fail: abort", "type: wait, for: mr_merged, timeout: 1h, on_fail: abort", 1)
	ph := newPhasedHarness(t, doc)
	runID := ph.start(t)
	ph.clock.Advance(2 * time.Hour)
	ph.waitState(t, runID, run.Abandoned)
	failed, _ := findPayload[*event.StageFailed](ph.events(t, runID))
	if failed == nil || failed.StageID != "merged" || failed.Reason != event.ReasonTimeout {
		t.Errorf("failed = %+v", failed)
	}
}

// A gate parks its phase the same way: a watcher runs while the person
// thinks, and a signal may answer the gate for them.
func TestGateParksAndAWatcherRunsMeanwhile(t *testing.T) {
	doc := strings.Replace(phasedFlowDoc,
		"  - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }\n",
		"  - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }\n  - { id: final, type: human_gate, question: \"Merge?\", input: { mr: $MR_URL }, auto_approve: mr_merged, auto_reject: mr_closed, on_reject: abort }\n", 1)
	ph := newPhasedHarness(t, doc)
	runID, err := ph.e.CreateRun(context.Background(), Ticket{Title: "Phased"}, "phased", testProject)
	if err != nil {
		t.Fatal(err)
	}
	gate := ph.waitPing(t, event.PingGate)
	v := ph.waitState(t, runID, run.WaitingOnHuman)
	if !v.Idle {
		t.Errorf("a parked gate leaves the run idle: %+v", v.State)
	}
	ph.repoHost.SetTargetSHA(1, "t1")
	ph.repoHost.SetMergeable(1, ports.MergeConflicts, "h1")
	ask := ph.waitPing(t, event.PingPhaseConfirm, gate.ID)
	if err := ph.e.AnswerPing(ask.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	ph.waitPhaseCompleted(t, runID, "conflicts", 1)
	if n := len(ph.execsOf("./sync.sh")); n != 1 {
		t.Fatalf("sync ran %d times while the gate was open", n)
	}
	if v := ph.view(t, runID); v.State != run.WaitingOnHuman || !v.Idle {
		t.Errorf("after the watcher, the open gate keeps the run waiting: %s idle %v", v.State, v.Idle)
	}
	ph.repoHost.SetMRState(1, ports.MRMerged)
	ph.waitState(t, runID, run.Completed)
	answered := false
	for _, e := range ph.events(t, runID) {
		if a, ok := e.Payload.(*event.PingAnswered); ok && a.PingID == gate.ID && a.Answer == "approve" && strings.Contains(a.Feedback, "auto: mr_merged") {
			answered = true
		}
	}
	if !answered {
		t.Error("the merge did not answer the gate")
	}
}

// A phase with start: manual never opens by itself: its hold is surfaced
// for the run page, and a person starts it.
func TestManualStartPolicySurfacesTheHoldWithoutStarting(t *testing.T) {
	doc := strings.Replace(phasedFlowDoc, "on: { signal: mr_conflicts, start: confirm }", "on: { signal: mr_conflicts, start: manual }", 1)
	ph := newPhasedHarness(t, doc)
	runID := ph.start(t)
	ph.repoHost.SetTargetSHA(1, "t1")
	ph.repoHost.SetMergeable(1, ports.MergeConflicts, "h1")
	deadline := time.Now().Add(2 * time.Second)
	var holds []TriggerHold
	for time.Now().Before(deadline) && len(holds) == 0 {
		infos, err := ph.e.RunPhases(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range infos {
			if p.ID == "conflicts" {
				holds = p.Holds
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(holds) != 1 || holds[0].Subject != "repo" || holds[0].Seen || !strings.Contains(holds[0].Why, "no longer merges cleanly") {
		t.Fatalf("holds = %+v", holds)
	}
	for _, p := range ph.e.PendingPings() {
		if p.Type == event.PingPhaseConfirm {
			t.Fatalf("a manual phase asked: %+v", p)
		}
	}
	if n := len(phaseStarts(ph.events(t, runID))); n != 1 {
		t.Fatalf("a manual phase started by itself (%d starts)", n)
	}
	if err := ph.e.StartPhase(runID, "conflicts", ""); err != nil {
		t.Fatal(err)
	}
	ph.waitPhaseRuns(t, runID, "conflicts", 1)
}

// The start policy may come from a parameter the project binds.
func TestStartPolicyFromAParam(t *testing.T) {
	doc := strings.Replace(phasedFlowDoc, "parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }",
		"parameters: { harness: fake, model: anthropic/claude-sonnet-4-6, rebase: { default: confirm, enum: [auto, confirm, manual] } }", 1)
	doc = strings.Replace(doc, "on: { signal: mr_conflicts, start: confirm }", "on: { signal: mr_conflicts, start: $rebase }", 1)
	ph := newPhasedHarness(t, doc)
	ph.e.SetFlowBindings(staticBindings{"rebase": "auto"})
	runID := ph.start(t)
	ph.repoHost.SetTargetSHA(1, "t1")
	ph.repoHost.SetMergeable(1, ports.MergeConflicts, "h1")
	ph.waitPhaseRuns(t, runID, "conflicts", 1)
	for _, p := range ph.e.PendingPings() {
		if p.Type == event.PingPhaseConfirm {
			t.Fatalf("auto asked first: %+v", p)
		}
	}
	infos, err := ph.e.RunPhases(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if infos[1].Start != "auto" {
		t.Errorf("conflicts start = %q", infos[1].Start)
	}
}

// A restart while parked re-parks from the log: no second ping, no second
// phase start, and the merge still resumes and completes the run.
func TestParkedStateSurvivesARestart(t *testing.T) {
	ph := newPhasedHarness(t, waitFlowDoc)
	runID := ph.start(t)
	ph.e.Shutdown()

	h2 := newHarnessSource(t, ph.flows, ph.flowOf(t, "phased"))
	h2.store, h2.envp, h2.repoHost = ph.store, ph.envp, ph.repoHost
	h2.res.Envp, h2.res.RepoHost = ph.envp, ph.repoHost
	h2.runner.RunFunc = ph.runner.RunFunc
	h2.res.Runner = h2.runner
	h2.e = New(Config{MRPollInterval: 5 * time.Millisecond}, ph.store, StaticResolver{R: h2.res}, ph.flows, h2.settings, nil)
	t.Cleanup(h2.e.Shutdown)
	if err := h2.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	ph.repoHost.SetMRState(1, ports.MRMerged)
	deadline := time.Now().Add(5 * time.Second)
	for h2.view(t, runID).State != run.Completed && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if st := h2.view(t, runID).State; st != run.Completed {
		t.Fatalf("state after restart + merge = %s", st)
	}
	var starts, parked int
	for _, e := range h2.events(t, runID) {
		switch p := e.Payload.(type) {
		case *event.PhaseStarted:
			if p.Phase == flow.MainPhase {
				starts++
			}
		case *event.PhaseParked:
			parked++
		}
	}
	if starts != 1 || parked != 1 {
		t.Errorf("main starts = %d parked = %d, want 1 and 1", starts, parked)
	}
}

// A main chain that opens no merge request has nothing to watch and no
// outcome to wait for: the run completes as a flow without watchers would.
func TestPhasedRunWithoutAnMRCompletes(t *testing.T) {
	doc := `name: phased
version: 5
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
main:
  - { id: s, type: command, run: "true" }
outcomes:
  - { id: shipped, on: mr_merged, then: done }
`
	ph := newPhasedHarness(t, doc)
	runID, err := ph.e.CreateRun(context.Background(), Ticket{Title: "T"}, "phased", testProject)
	if err != nil {
		t.Fatal(err)
	}
	ph.waitState(t, runID, run.Completed)
}

// A confirm phase asks first, runs on approve, and is not offered again for
// the same conflict; a declined conflict is offered again only once the
// target moves (R5, R6).
func TestConflictPhaseIsConfirmedOncePerConflict(t *testing.T) {
	ph := newPhasedHarness(t, phasedFlowDoc)
	runID := ph.start(t)
	ph.repoHost.SetTargetSHA(1, "t1")
	ph.repoHost.SetMergeable(1, ports.MergeConflicts, "h1")
	ask := ph.waitPing(t, event.PingPhaseConfirm)
	if !strings.Contains(ask.Question, "no longer merges cleanly") || ask.Link == "" {
		t.Errorf("confirm ping = %+v", ask)
	}
	if v := ph.view(t, runID); v.State != run.WaitingOnHuman {
		t.Errorf("state while asking = %s", v.State)
	}
	if err := ph.e.AnswerPing(ask.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	ph.waitPhaseRuns(t, runID, "conflicts", 1)
	if n := len(ph.execsOf("./sync.sh")); n != 1 {
		t.Fatalf("sync ran %d times", n)
	}
	// Same conflict still reported (the repository host has not recomputed):
	// no new ask.
	time.Sleep(50 * time.Millisecond)
	for _, p := range ph.e.PendingPings() {
		if p.Type == event.PingPhaseConfirm {
			t.Fatalf("asked again for the same conflict: %+v", p)
		}
	}
	// main moves: a new conflict, asked again — declined this time.
	ph.repoHost.SetTargetSHA(1, "t2")
	ask2 := ph.waitPing(t, event.PingPhaseConfirm, ask.ID)
	if err := ph.e.AnswerPing(ask2.ID, PingAnswer{Answer: "reject", Feedback: "later"}); err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.PhaseDeclined](t, ph.harness, runID)
	ph.waitState(t, runID, run.InReview)
	time.Sleep(50 * time.Millisecond)
	for _, p := range ph.e.PendingPings() {
		if p.Type == event.PingPhaseConfirm {
			t.Fatalf("asked again for a declined conflict: %+v", p)
		}
	}
	// It moves again: asked again.
	ph.repoHost.SetTargetSHA(1, "t3")
	ph.waitPing(t, event.PingPhaseConfirm, ask.ID, ask2.ID)
	if n := len(ph.execsOf("./sync.sh")); n != 1 {
		t.Errorf("a declined conflict ran the phase (%d runs)", n)
	}
}

// Review comments start the phase only after the quiet period, once for a
// burst; each round's edge budget is fresh (R7); max bounds the rounds.
func TestReviewPhaseWaitsForAQuietReview(t *testing.T) {
	ph := newPhasedHarness(t, phasedFlowDoc)
	runID := ph.start(t)
	t0 := ph.clock.Now()
	ph.repoHost.AddComment(1, "d1", "a.go", "rev", "rename", t0)
	ph.repoHost.AddComment(1, "d2", "b.go", "rev", "add a test", t0.Add(10*time.Minute))
	ph.repoHost.AddComment(1, "d1", "a.go", "rev", "and the caller", t0.Add(50*time.Minute))
	ph.clock.Advance(55 * time.Minute) // 5 minutes after the last comment
	time.Sleep(60 * time.Millisecond)
	if n := len(phaseStarts(ph.events(t, runID))); n != 1 {
		t.Fatalf("a review phase started before the review went quiet (%d phase starts)", n)
	}
	ph.failFix.Store(1) // round 1: one failure, absorbed by on_fail max 1
	ph.clock.Advance(56 * time.Minute)
	ph.waitPhaseRuns(t, runID, "review_fixes", 1)
	starts := phaseStarts(ph.events(t, runID))
	if len(starts) != 2 || starts[1].Phase != "review_fixes" || starts[1].Subject != "repo" || starts[1].By != "signal" {
		t.Fatalf("phase starts = %+v", starts)
	}
	fix := ph.promptsOf("fix")
	if len(fix) != 2 || !strings.Contains(fix[1], "# SUBJECT") || !strings.Contains(fix[1], "https://gitlab.local/mr/1") {
		t.Errorf("fix prompts = %d; last:\n%s", len(fix), fix[len(fix)-1])
	}
	// The same burst does not start a second round.
	time.Sleep(60 * time.Millisecond)
	if n := len(phaseStarts(ph.events(t, runID))); n != 2 {
		t.Fatalf("the same comments started %d rounds", n-1)
	}
	// A new comment, quiet again: round 2, whose on_fail budget is fresh.
	ph.failFix.Store(1)
	ph.repoHost.AddComment(1, "d3", "c.go", "rev", "one more", ph.clock.Now())
	ph.clock.Advance(61 * time.Minute)
	ph.waitPhaseRuns(t, runID, "review_fixes", 2)
	// max: 2 — a third burst is not acted on.
	ph.repoHost.AddComment(1, "d4", "d.go", "rev", "last", ph.clock.Now())
	ph.clock.Advance(61 * time.Minute)
	time.Sleep(60 * time.Millisecond)
	if n := len(phaseStarts(ph.events(t, runID))); n != 3 {
		t.Errorf("phase starts = %d, want 3 (main + 2 rounds, max reached)", n)
	}
	if v := ph.view(t, runID); v.State != run.InReview {
		t.Errorf("state = %s", v.State)
	}
}

// Triggers holding together run one after the other in document order: the
// conflict first, then the review round (R3, R4).
func TestSimultaneousTriggersRunInDocumentOrder(t *testing.T) {
	ph := newPhasedHarness(t, strings.Replace(phasedFlowDoc, "start: confirm", "start: auto", 1))
	runID, err := ph.e.CreateRun(context.Background(), Ticket{Title: "Phased"}, "phased", testProject)
	if err != nil {
		t.Fatal(err)
	}
	// Both hold before the run even goes idle.
	waitEvent[*event.MRCreated](t, ph.harness, runID)
	ph.repoHost.SetMergeable(1, ports.MergeConflicts, "h1")
	ph.repoHost.AddComment(1, "d1", "a.go", "rev", "rename", ph.clock.Now())
	ph.clock.Advance(2 * time.Hour)
	ph.waitPhaseRuns(t, runID, "review_fixes", 1)
	starts := phaseStarts(ph.events(t, runID))
	if len(starts) < 3 || starts[1].Phase != "conflicts" || starts[2].Phase != "review_fixes" {
		t.Errorf("phase order = %+v", starts)
	}
}

// Two merge requests are two subjects: each phase run acts on its own
// checkout, and they take turns, the primary first (R3, R12).
func TestPhasesActOnOneSubjectAtATime(t *testing.T) {
	doc := strings.Replace(phasedFlowDoc, "start: confirm", "start: auto", 1)
	doc = strings.Replace(doc, "  - { id: build, type: agent, agent: builder }\n",
		"  - { id: build, type: agent, agent: builder }\n  - { id: commit, type: command, each_repo: true, run: \"git add -A && git commit -m x\" }\n", 1)
	ph := newPhasedHarness(t, doc)
	apiRepoHost := &fake.RepoHost{}
	ph.res.ExtraRepos = []ExtraRepo{{Name: "api", URL: "https://gitlab.local/g/api.git", Locator: "2", DefaultBranch: "develop"}}
	ph.res.RepoHosts = map[string]ports.RepoHost{"api": apiRepoHost}
	ph.envp.ExecFunc = gitPerDir(map[string]*fakeGit{"repo": newFakeGit("main"), "repos/api": newFakeGit("develop")})
	runID := ph.start(t)
	if n := len(apiRepoHost.SnapshotMRs()); n != 1 {
		t.Fatalf("api MRs = %d", n)
	}
	apiRepoHost.SetMergeable(1, ports.MergeConflicts, "a1")
	ph.repoHost.AddComment(1, "d1", "a.go", "rev", "rename", ph.clock.Now())
	ph.clock.Advance(2 * time.Hour)
	ph.waitPhaseRuns(t, runID, "conflicts", 1)
	ph.waitPhaseRuns(t, runID, "review_fixes", 1)
	starts := phaseStarts(ph.events(t, runID))
	if len(starts) != 3 || starts[1].Phase != "review_fixes" || starts[1].Subject != "repo" ||
		starts[2].Phase != "conflicts" || starts[2].Subject != "api" {
		t.Fatalf("starts = %+v (primary first, then api)", starts)
	}
	sync := ph.execsOf("./sync.sh")
	post := ph.execsOf("./post.sh")
	if len(sync) != 1 || sync[0].Dir != "repos/api" || len(post) != 1 || post[0].Dir != "repo" {
		t.Errorf("sync ran in %+v, post in %+v", sync, post)
	}
}

// A human can start any watcher from the idle run — on the single open merge
// request when none is named — not while a phase runs, not for a phase or
// repository the run does not have. The main chain runs once, when the run
// is started: it is never started by hand.
func TestStartPhaseByHand(t *testing.T) {
	ph := newPhasedHarness(t, phasedFlowDoc)
	runID := ph.start(t)
	if err := ph.e.StartPhase(runID, "ghost", ""); !errors.Is(err, ErrPhaseNotStartable) {
		t.Errorf("unknown phase: %v", err)
	}
	if err := ph.e.StartPhase(runID, "conflicts", "ghost"); !errors.Is(err, ErrPhaseNotStartable) {
		t.Errorf("unknown repo: %v", err)
	}
	if err := ph.e.StartPhase(runID, "conflicts", ""); err != nil {
		t.Fatal(err)
	}
	ph.waitPhaseRuns(t, runID, "conflicts", 1)
	starts := phaseStarts(ph.events(t, runID))
	if last := starts[len(starts)-1]; last.By != "human" || last.Phase != "conflicts" || last.Subject != "repo" {
		t.Errorf("start = %+v", last)
	}
	if err := ph.e.StartPhase(runID, flow.MainPhase, ""); !errors.Is(err, ErrPhaseNotStartable) || !strings.Contains(err.Error(), "runs once") {
		t.Errorf("the main chain by hand: %v", err)
	}
	infos, err := ph.e.RunPhases(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]PhaseInfo{}
	for _, p := range infos {
		byID[p.ID] = p
	}
	if d := byID[flow.MainPhase]; d.Kind != string(flow.PhaseMain) || d.Signal != "run_start" || d.Total != 1 || d.Live != nil {
		t.Errorf("main = %+v", d)
	}
	if c := byID["conflicts"]; c.Kind != string(flow.PhaseWatcher) || c.Total != 1 || c.Runs["repo"] != 1 || c.Start != "confirm" || !c.Confirm {
		t.Errorf("conflicts = %+v", c)
	}
}

// An idle run does not hold a concurrency slot: with one slot, a second run
// starts while the first sits in review.
func TestIdleRunsFreeTheirSlot(t *testing.T) {
	ph := newPhasedHarness(t, phasedFlowDoc)
	ph.settings.L.MaxConcurrentRuns = 1
	ph.start(t)
	second, err := ph.e.CreateRun(context.Background(), Ticket{Title: "Second"}, "phased", testProject)
	if err != nil {
		t.Fatal(err)
	}
	ph.waitState(t, second, run.InReview)
}

// An engine restart while idle re-arms the watch: the merge still completes
// the run, and nothing restarts the main chain.
func TestIdleRunSurvivesARestart(t *testing.T) {
	ph := newPhasedHarness(t, phasedFlowDoc)
	runID := ph.start(t)
	ph.e.Shutdown()

	h2 := newHarnessSource(t, ph.flows, ph.flowOf(t, "phased"))
	h2.store, h2.envp, h2.repoHost = ph.store, ph.envp, ph.repoHost
	h2.res.Envp, h2.res.RepoHost = ph.envp, ph.repoHost
	h2.runner.RunFunc = ph.runner.RunFunc
	h2.res.Runner = h2.runner
	h2.e = New(Config{MRPollInterval: 5 * time.Millisecond}, ph.store, StaticResolver{R: h2.res}, ph.flows, h2.settings, nil)
	t.Cleanup(h2.e.Shutdown)
	if err := h2.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	ph.repoHost.SetMRState(1, ports.MRMerged)
	deadline := time.Now().Add(5 * time.Second)
	for h2.view(t, runID).State != run.Completed && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if st := h2.view(t, runID).State; st != run.Completed {
		t.Fatalf("state after restart + merge = %s", st)
	}
	if starts := phaseStarts(h2.events(t, runID)); len(starts) != 2 || starts[0].Phase != flow.MainPhase || starts[1].Phase != "shipped" {
		t.Errorf("phase starts = %+v, want main once, then the merged outcome", starts)
	}
}

// A watcher on manual never begins on its own: the idle loop skips it, a
// person starts it from the run page, and its max counts those starts.
func TestManualPhaseStartsOnlyByHand(t *testing.T) {
	doc := strings.Replace(phasedFlowDoc, "outcomes:\n",
		"  - id: polish\n    on: { signal: manual, max: 1 }\n    stages:\n      - { id: shine, type: command, run: \"./shine.sh\" }\noutcomes:\n", 1)
	ph := newPhasedHarness(t, doc)
	runID := ph.start(t)
	if starts := phaseStarts(ph.events(t, runID)); len(starts) != 1 {
		t.Fatalf("starts = %+v (a manual phase must not begin on its own)", starts)
	}
	if err := ph.e.StartPhase(runID, "polish", ""); err != nil {
		t.Fatal(err)
	}
	ph.waitPhaseRuns(t, runID, "polish", 1)
	ph.waitState(t, runID, run.InReview)
	if execs := ph.execsOf("./shine.sh"); len(execs) != 1 {
		t.Errorf("shine ran %d times", len(execs))
	}
	if err := ph.e.StartPhase(runID, "polish", ""); !errors.Is(err, ErrPhaseNotStartable) || !strings.Contains(err.Error(), "max") {
		t.Errorf("second start past max: %v", err)
	}
}
