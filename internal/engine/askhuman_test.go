package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

const askFlowDoc = `name: ask
version: 2
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: build, type: agent, agent: builder }
`

// waitEvent polls the log for the first payload of type T.
func waitEvent[T event.Payload](t *testing.T, h *harness, runID string) T {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p, ok := findPayload[T](h.events(t, runID)); ok {
			return p
		}
		time.Sleep(2 * time.Millisecond)
	}
	var zero T
	t.Fatalf("timed out waiting for %T; events: %v", zero, eventTypes(h.events(t, runID)))
	return zero
}

// An agent that asks and then ends its turn — which is what the ask_human tool
// now tells it to do when nobody answers inside the grace hold — must NOT be
// treated as a finished stage. The attempt pauses, the engine holds the wait
// (untimed), and the same session is resumed with the answer.
//
// Letting the invocation through instead is the bug this whole path exists to
// kill: the harness cut the tool call at 60 s, the model guessed, and a plan
// nobody had approved sailed into the gate looking finished.
func TestAgentEndingTurnOnOpenQuestionPausesAttempt(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	h.runner.RunFunc = func(_ context.Context, _ fake.RunCall) (ports.AgentResult, error) {
		id := <-runIDCh
		if _, err := h.e.RaisePing(id, "clarification", "one endpoint or two?", "", nil, nil); err != nil {
			return ports.AgentResult{}, err
		}
		// Ends the turn without an answer, exactly as instructed.
		return ports.AgentResult{TurnsUsed: 2, Session: "sess-1",
			Usage: ports.Usage{CostUSD: 0.25}, Output: "asked, waiting"}, nil
	}
	h.runner.ResumeFunc = func(_ context.Context, _ fake.ResumeCall) (ports.AgentResult, error) {
		return ports.AgentResult{TurnsUsed: 3, Session: "sess-1", Usage: ports.Usage{CostUSD: 0.5}}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID

	p := h.waitPing(t, "clarification")
	paused := waitEvent[*event.StagePaused](t, h, runID)
	if paused.StageID != "build" || paused.Reason != event.PauseAskHuman {
		t.Errorf("stage_paused = %+v", paused)
	}
	if len(paused.PingIDs) != 1 || paused.PingIDs[0] != p.ID {
		t.Errorf("stage_paused.ping_ids = %v, want [%s]", paused.PingIDs, p.ID)
	}
	// The invocation's spend is banked at the pause: it was really spent,
	// whether or not the run ever resumes.
	if paused.TurnsUsed != 2 || paused.Usage == nil || paused.Usage.CostUSD != 0.25 {
		t.Errorf("stage_paused spend = %d turns, %+v", paused.TurnsUsed, paused.Usage)
	}
	// The session is on the pause event, so a restart here would still know
	// what to resume into.
	if paused.SessionID != "sess-1" {
		t.Errorf("stage_paused.session_id = %q", paused.SessionID)
	}
	// A paused attempt is not a completed one, and nothing has moved on.
	if evs := h.events(t, runID); len(evs) > 0 {
		if _, ok := findPayload[*event.StageCompleted](evs); ok {
			t.Fatal("stage completed while its question was still open")
		}
	}
	if v := h.view(t, runID); v.State != run.WaitingOnHuman {
		t.Errorf("state while paused = %s", v.State)
	}

	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "two: /health and /healthz"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	resumes := h.runner.ResumeCalls
	if len(resumes) != 1 {
		t.Fatalf("resume calls = %d, want 1", len(resumes))
	}
	if resumes[0].Session != "sess-1" {
		t.Errorf("resumed session = %q", resumes[0].Session)
	}
	for _, want := range []string{"one endpoint or two?", "two: /health and /healthz", "ANSWER FROM THE HUMAN"} {
		if !strings.Contains(resumes[0].Reply, want) {
			t.Errorf("resume reply %q missing %q", resumes[0].Reply, want)
		}
	}

	evs := h.events(t, runID)
	resumed, ok := findPayload[*event.StageResumed](evs)
	if !ok || resumed.Reason != event.ResumeAnswered || resumed.StageID != "build" {
		t.Errorf("stage_resumed = %+v (ok=%v)", resumed, ok)
	}
	// One attempt, not two: the iteration never moved.
	if resumed.Iteration != paused.Iteration || paused.Iteration != 1 {
		t.Errorf("iterations: paused=%d resumed=%d, want both 1", paused.Iteration, resumed.Iteration)
	}
	if n := len(h.runner.SnapshotRunCalls()); n != 1 {
		t.Errorf("run calls = %d, want 1 (the resume is not a new attempt)", n)
	}
	// Every leg's spend counts toward the run's totals.
	rc, _ := findPayload[*event.RunCompleted](evs)
	if rc.TotalTurns != 5 || rc.TotalCostUSD != 0.75 {
		t.Errorf("run totals = %d turns / %.2f USD, want 5 / 0.75", rc.TotalTurns, rc.TotalCostUSD)
	}
	if v := h.view(t, runID); v.State != run.Completed {
		t.Errorf("final state = %s", v.State)
	}
}

// A harness that DIED with a question open has nothing left to resume into:
// that is a failure to route, not an attempt to hold open.
func TestHarnessErrorWithOpenQuestionFailsRatherThanPausing(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	h.runner.RunFunc = func(_ context.Context, _ fake.RunCall) (ports.AgentResult, error) {
		id := <-runIDCh
		if _, err := h.e.RaisePing(id, "clarification", "which db?", "", nil, nil); err != nil {
			return ports.AgentResult{}, err
		}
		return ports.AgentResult{}, context.DeadlineExceeded
	}

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID
	h.e.Wait(runID)

	evs := h.events(t, runID)
	if _, ok := findPayload[*event.StagePaused](evs); ok {
		t.Error("a dead harness must not pause the attempt")
	}
	sf, ok := findPayload[*event.StageFailed](evs)
	if !ok || sf.Reason != event.ReasonHarnessError {
		t.Errorf("stage_failed = %+v (ok=%v)", sf, ok)
	}
}

// The wait outlives this engine: a pause records the session, so a restart
// re-enters the same attempt, waits for the answer, and resumes — instead of
// starting the agent over and throwing away everything it did before asking.
func TestPausedAttemptResumesAfterRestart(t *testing.T) {
	fl := mustFlow(t, askFlowDoc)
	h := newHarness(t, fl)
	ctx := context.Background()
	envID, _ := h.envp.Provision(ctx, ports.EnvSpec{RunID: "r-p"})
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: "ask", ProjectID: testProject, FlowHash: fl.Hash, Branch: "b"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: string(envID)},
		&event.StageStarted{StageID: "build", Iteration: 1},
		&event.PingRaised{PingID: "p-1", Type: "clarification", Question: "one endpoint or two?", StageID: "build"},
		&event.StagePaused{StageID: "build", Iteration: 1, Reason: event.PauseAskHuman,
			PingIDs: []string{"p-1"}, TurnsUsed: 2, SessionID: "sess-x"},
		// kill -9 here, with the human still thinking
	} {
		if _, err := h.store.Append(ctx, "r-p", p); err != nil {
			t.Fatal(err)
		}
	}
	h.runner.ResumeFunc = func(_ context.Context, _ fake.ResumeCall) (ports.AgentResult, error) {
		return ports.AgentResult{TurnsUsed: 1, Session: "sess-x"}, nil
	}
	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}

	// The recovered run is waiting, not working: no harness call yet.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(h.e.PendingPings()) > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if n := len(h.runner.SnapshotRunCalls()); n != 0 {
		t.Fatalf("run calls before the answer = %d, want 0 (the agent must not be restarted)", n)
	}

	if err := h.e.AnswerPing("p-1", PingAnswer{Answer: "two of them"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-p")

	if n := len(h.runner.SnapshotRunCalls()); n != 0 {
		t.Errorf("run calls = %d, want 0", n)
	}
	resumes := h.runner.ResumeCalls
	if len(resumes) != 1 || resumes[0].Session != "sess-x" {
		t.Fatalf("resume calls = %+v", resumes)
	}
	if !strings.Contains(resumes[0].Reply, "two of them") {
		t.Errorf("resume reply = %q", resumes[0].Reply)
	}
	if v := h.view(t, "r-p"); v.State != run.Completed {
		t.Errorf("final state = %s, events: %v", v.State, eventTypes(h.events(t, "r-p")))
	}
	// The pre-crash leg's turns are not lost: replay banks them.
	rc, _ := findPayload[*event.RunCompleted](h.events(t, "r-p"))
	if rc.TotalTurns != 3 {
		t.Errorf("total turns = %d, want 3 (2 before the crash + 1 after)", rc.TotalTurns)
	}
}

// An answer with nowhere to go: the attempt paused on a question but no
// harness session was ever recorded (here: a crash before the harness reported
// one). Re-running the agent from scratch would silently drop the human\'s
// words and let it guess again — so the attempt fails and the flow routes it.
func TestAnswerWithNoSessionToResumeFailsTheAttempt(t *testing.T) {
	fl := mustFlow(t, askFlowDoc)
	h := newHarness(t, fl)
	ctx := context.Background()
	envID, _ := h.envp.Provision(ctx, ports.EnvSpec{RunID: "r-n"})
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: "ask", ProjectID: testProject, FlowHash: fl.Hash, Branch: "b"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: string(envID)},
		&event.StageStarted{StageID: "build", Iteration: 1},
		&event.PingRaised{PingID: "p-2", Type: "clarification", Question: "which db?", StageID: "build"},
		&event.StagePaused{StageID: "build", Iteration: 1, Reason: event.PauseAskHuman, PingIDs: []string{"p-2"}},
	} {
		if _, err := h.store.Append(ctx, "r-n", p); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(h.e.PendingPings()) == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	if err := h.e.AnswerPing("p-2", PingAnswer{Answer: "postgres"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-n")

	sf, ok := findPayload[*event.StageFailed](h.events(t, "r-n"))
	if !ok || sf.Reason != event.ReasonHarnessError || !strings.Contains(sf.Error, "no session to resume") {
		t.Errorf("stage_failed = %+v (ok=%v)", sf, ok)
	}
	if n := len(h.runner.ResumeCalls); n != 0 {
		t.Errorf("resume calls = %d, want 0", n)
	}
	if n := len(h.runner.SnapshotRunCalls()); n != 0 {
		t.Errorf("run calls = %d, want 0 (the agent must not be restarted without the answer)", n)
	}
}

// The stop-and-wait notice is a PROMPT, and a model is free to ignore it. The
// pause must not depend on obedience: an agent that plows on, writes its
// declared output from a guess and ends its turn still pauses on the open
// question — and the resume names the file it wrote blind.
func TestDisobedientAgentStillPausesAndIsToldWhatItGuessed(t *testing.T) {
	doc := `name: askout
version: 2
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
agents:
  - name: planner
    description: writes the plan
    prompt: |
      Plan the work.
    outputs:
      - { name: plan, description: the plan, path: artifacts/plan.md }
stages:
  - { id: plan, type: agent, agent: planner }
`
	h := newHarness(t, mustFlow(t, doc))
	runIDCh := make(chan string, 1)
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (ports.AgentResult, error) {
		id := <-runIDCh
		if _, err := h.e.RaisePing(id, "clarification", "one endpoint or two?", "", nil, nil); err != nil {
			return ports.AgentResult{}, err
		}
		// Ignores the notice: guesses, writes the artifact, ends its turn.
		if err := h.envp.WriteFile(ctx, call.Env, "artifacts/plan.md", []byte("# Goal\nOne endpoint."), 0o644); err != nil {
			return ports.AgentResult{}, err
		}
		return ports.AgentResult{TurnsUsed: 4, Session: "sess-1"}, nil
	}
	h.runner.ResumeFunc = func(_ context.Context, _ fake.ResumeCall) (ports.AgentResult, error) {
		return ports.AgentResult{TurnsUsed: 2, Session: "sess-1"}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "askout", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID

	p := h.waitPing(t, "clarification")
	waitEvent[*event.StagePaused](t, h, runID) // paused anyway — the engine does not trust the prompt
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "two of them"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	resumes := h.runner.ResumeCalls
	if len(resumes) != 1 {
		t.Fatalf("resume calls = %d, want 1", len(resumes))
	}
	for _, want := range []string{"two of them", "artifacts/plan.md", "without it"} {
		if !strings.Contains(resumes[0].Reply, want) {
			t.Errorf("resume reply missing %q:\n%s", want, resumes[0].Reply)
		}
	}
	if v := h.view(t, runID); v.State != run.Completed {
		t.Errorf("final state = %s", v.State)
	}
}

// --- dormancy ---------------------------------------------------------------

// The point of parking: a run waiting on a human holds no goroutine and no
// concurrency slot, so a queued run gets to work while the human thinks —
// and the answer brings the parked one back with no button pressed.
func TestParkedRunFreesItsSlotAndTheAnswerWakesIt(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	h.settings.L = Limits{MaxConcurrentRuns: 1, MaxEnvironments: 10}
	ctx := context.Background()

	runIDCh := make(chan string, 1)
	var mu sync.Mutex
	started := map[string]int{}
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (ports.AgentResult, error) {
		mu.Lock()
		started[string(call.Env)]++
		first := len(started) == 1
		mu.Unlock()
		if !first {
			return ports.AgentResult{TurnsUsed: 1, Session: "sess-b"}, nil
		}
		id := <-runIDCh
		if _, err := h.e.RaisePing(id, "clarification", "one endpoint or two?", "", nil, nil); err != nil {
			return ports.AgentResult{}, err
		}
		return ports.AgentResult{TurnsUsed: 1, Session: "sess-a"}, nil
	}
	h.runner.ResumeFunc = func(_ context.Context, _ fake.ResumeCall) (ports.AgentResult, error) {
		return ports.AgentResult{TurnsUsed: 1, Session: "sess-a"}, nil
	}

	first, err := h.e.CreateRun(ctx, Ticket{Title: "A"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- first
	p := h.waitPing(t, "clarification")

	// Parked: no loop, and the run still reads as waiting on a human rather
	// than paused — nobody pressed anything.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.e.active(first) {
		time.Sleep(2 * time.Millisecond)
	}
	if h.e.active(first) {
		t.Fatal("run still holds a loop while waiting on a human")
	}
	v := h.view(t, first)
	if v.State != run.WaitingOnHuman || !v.Dormant {
		t.Fatalf("parked run = state %s dormant %v", v.State, v.Dormant)
	}
	rp, ok := findPayload[*event.RunPaused](h.events(t, first))
	if !ok || rp.Reason != event.PauseWaiting {
		t.Errorf("run_paused = %+v (ok=%v)", rp, ok)
	}

	// The freed slot admits the second run, which runs to completion while
	// the first is still waiting.
	second, err := h.e.CreateRun(ctx, Ticket{Title: "B"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(second)
	if v := h.view(t, second); v.State != run.Completed {
		t.Fatalf("queued run = %s (the parked one never gave up its slot)", v.State)
	}

	// The answer alone brings the parked run back.
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "two of them"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(first)
	if v := h.view(t, first); v.State != run.Completed {
		t.Fatalf("woken run = %s, events: %v", v.State, eventTypes(h.events(t, first)))
	}
	if n := len(h.runner.ResumeCalls); n != 1 {
		t.Fatalf("resume calls = %d, want 1", n)
	}
	if !strings.Contains(h.runner.ResumeCalls[0].Reply, "two of them") {
		t.Errorf("resume reply = %q", h.runner.ResumeCalls[0].Reply)
	}
}

// A gate with no signal armed is pure waiting too, so it parks the same way —
// and answering it routes the run without anyone pressing play.
func TestGateParksAndAnswerResumes(t *testing.T) {
	doc := `name: gated
version: 2
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: plan_gate, type: human_gate, question: "Approve?", on_success: done, on_reject: abort }
`
	h := newHarness(t, mustFlow(t, doc))
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "gated", testProject)
	if err != nil {
		t.Fatal(err)
	}
	p := h.waitPing(t, "gate")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.e.active(runID) {
		time.Sleep(2 * time.Millisecond)
	}
	if h.e.active(runID) {
		t.Fatal("a gate with nothing to poll must not hold a loop")
	}
	if v := h.view(t, runID); v.State != run.WaitingOnHuman {
		t.Fatalf("parked gate = %s", v.State)
	}

	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state after approving a parked gate = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
}

// Pause means pause. A human who parks a waiting run gets their answer
// recorded but the run stays put until they press play.
func TestPauseOnAParkedRunHoldsItThroughTheAnswer(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	h.runner.RunFunc = func(_ context.Context, _ fake.RunCall) (ports.AgentResult, error) {
		id := <-runIDCh
		if _, err := h.e.RaisePing(id, "clarification", "which db?", "", nil, nil); err != nil {
			return ports.AgentResult{}, err
		}
		return ports.AgentResult{TurnsUsed: 1, Session: "sess-1"}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID
	p := h.waitPing(t, "clarification")
	waitEvent[*event.StagePaused](t, h, runID)

	if err := h.e.Pause(runID); err != nil {
		t.Fatalf("pausing a parked run: %v", err)
	}
	if v := h.view(t, runID); v.State != run.Paused {
		t.Fatalf("state after pause = %s", v.State)
	}
	// Play is the only way out of a user pause — and it is refused for a run
	// that is merely waiting, which this one no longer is.
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "postgres"}); err != nil {
		t.Fatal(err)
	}
	if v := h.view(t, runID); v.State != run.Paused {
		t.Errorf("an answer must not un-pause a run a human parked: %s", v.State)
	}
	if pa, ok := findPayload[*event.PingAnswered](h.events(t, runID)); !ok || pa.Feedback != "postgres" {
		t.Errorf("the answer must still be recorded: %+v (ok=%v)", pa, ok)
	}
	if _, ok := findPayload[*event.RunUnpaused](h.events(t, runID)); ok {
		t.Error("a paused run must not be woken by an answer")
	}
}

// Play tells a waiting run apart from a paused one instead of re-entering it
// into an immediate re-park.
func TestPlayRefusesARunThatIsMerelyWaiting(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	h.runner.RunFunc = func(_ context.Context, _ fake.RunCall) (ports.AgentResult, error) {
		id := <-runIDCh
		if _, err := h.e.RaisePing(id, "clarification", "which db?", "", nil, nil); err != nil {
			return ports.AgentResult{}, err
		}
		return ports.AgentResult{TurnsUsed: 1, Session: "s"}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID
	h.waitPing(t, "clarification")
	waitEvent[*event.StagePaused](t, h, runID)

	err = h.e.Play(context.Background(), runID)
	if err == nil || !strings.Contains(err.Error(), "waiting for an answer") {
		t.Errorf("Play on a waiting run = %v", err)
	}
}

// The crash window the whole design has to survive: the answer was recorded
// and the engine died before re-queuing the run. Boot must pick it up, not
// leave it parked on a question that has already been answered.
func TestRecoverAdmitsAParkedRunWhoseAnswerLanded(t *testing.T) {
	fl := mustFlow(t, askFlowDoc)
	h := newHarness(t, fl)
	ctx := context.Background()
	envID, _ := h.envp.Provision(ctx, ports.EnvSpec{RunID: "r-w"})
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: "ask", ProjectID: testProject, FlowHash: fl.Hash, Branch: "b"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: string(envID)},
		&event.StageStarted{StageID: "build", Iteration: 1},
		&event.PingRaised{PingID: "p-w", Type: "clarification", Question: "which db?", StageID: "build"},
		&event.StagePaused{StageID: "build", Iteration: 1, Reason: event.PauseAskHuman,
			PingIDs: []string{"p-w"}, TurnsUsed: 1, SessionID: "sess-w"},
		&event.RunPaused{StageID: "build", Reason: event.PauseWaiting},
		&event.PingAnswered{PingID: "p-w", Answer: "text", Feedback: "postgres"},
		// kill -9 here, between recording the answer and re-queuing the run
	} {
		if _, err := h.store.Append(ctx, "r-w", p); err != nil {
			t.Fatal(err)
		}
	}
	h.runner.ResumeFunc = func(_ context.Context, _ fake.ResumeCall) (ports.AgentResult, error) {
		return ports.AgentResult{TurnsUsed: 1, Session: "sess-w"}, nil
	}
	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-w")

	if v := h.view(t, "r-w"); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, "r-w")))
	}
	resumes := h.runner.ResumeCalls
	if len(resumes) != 1 || !strings.Contains(resumes[0].Reply, "postgres") {
		t.Fatalf("resume calls = %+v", resumes)
	}
	if n := len(h.runner.SnapshotRunCalls()); n != 0 {
		t.Errorf("run calls = %d, want 0", n)
	}
}

// A parked run whose question is still open stays parked across a boot: it has
// nothing to do, so it is owed no loop.
func TestRecoverLeavesAnUnansweredParkedRunAlone(t *testing.T) {
	fl := mustFlow(t, askFlowDoc)
	h := newHarness(t, fl)
	ctx := context.Background()
	envID, _ := h.envp.Provision(ctx, ports.EnvSpec{RunID: "r-u"})
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: "ask", ProjectID: testProject, FlowHash: fl.Hash, Branch: "b"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: string(envID)},
		&event.StageStarted{StageID: "build", Iteration: 1},
		&event.PingRaised{PingID: "p-u", Type: "clarification", Question: "which db?", StageID: "build"},
		&event.StagePaused{StageID: "build", Iteration: 1, Reason: event.PauseAskHuman,
			PingIDs: []string{"p-u"}, TurnsUsed: 1, SessionID: "sess-u"},
		&event.RunPaused{StageID: "build", Reason: event.PauseWaiting},
	} {
		if _, err := h.store.Append(ctx, "r-u", p); err != nil {
			t.Fatal(err)
		}
	}
	h.runner.ResumeFunc = func(_ context.Context, _ fake.ResumeCall) (ports.AgentResult, error) {
		return ports.AgentResult{TurnsUsed: 1, Session: "sess-u"}, nil
	}
	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-u")
	if n := len(h.runner.ResumeCalls) + len(h.runner.SnapshotRunCalls()); n != 0 {
		t.Errorf("harness calls = %d, want 0 (nothing to do until it is answered)", n)
	}
	// ...but the question is still listed, which is the whole point of the
	// projection-backed inbox: a parked run's ping is the one waiting longest.
	var found bool
	for _, p := range h.e.PendingPings() {
		if p.ID == "p-u" {
			found = true
		}
	}
	if !found {
		t.Error("a parked run's question vanished from the inbox")
	}
	// Answering it now wakes it, restart or no restart.
	if err := h.e.AnswerPing("p-u", PingAnswer{Answer: "postgres"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-u")
	if v := h.view(t, "r-u"); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, "r-u")))
	}
}

// The answer that lands a second too late. Once the tool call has handed the
// turn back, the agent cannot receive an answer any more — so an answer that
// arrives while it is still winding down must NOT close the question, or the
// stage completes on a guess with the human's words sitting unread in the log.
func TestAnswerAfterTheTurnWasHandedBackStillPausesTheAttempt(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	h.runner.RunFunc = func(_ context.Context, _ fake.RunCall) (ports.AgentResult, error) {
		id := <-runIDCh
		p, err := h.e.RaisePing(id, "clarification", "which db?", "", nil, nil)
		if err != nil {
			return ports.AgentResult{}, err
		}
		// The grace hold expired: the agent was told to stop and wait.
		p.HandOff(false)
		// ...and the human answers while it is still ending its turn.
		if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "postgres"}); err != nil {
			return ports.AgentResult{}, err
		}
		return ports.AgentResult{TurnsUsed: 1, Session: "sess-1"}, nil
	}
	h.runner.ResumeFunc = func(_ context.Context, _ fake.ResumeCall) (ports.AgentResult, error) {
		return ports.AgentResult{TurnsUsed: 1, Session: "sess-1"}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID
	h.e.Wait(runID)

	evs := h.events(t, runID)
	if _, ok := findPayload[*event.StagePaused](evs); !ok {
		t.Fatal("the attempt must pause on a question the agent never got an answer to")
	}
	resumes := h.runner.ResumeCalls
	if len(resumes) != 1 || !strings.Contains(resumes[0].Reply, "postgres") {
		t.Fatalf("resume calls = %+v (the answer must reach the agent on resume)", resumes)
	}
	if v := h.view(t, runID); v.State != run.Completed {
		t.Errorf("final state = %s", v.State)
	}
}
