package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

const askFlowDoc = `name: ask
version: 2
defaults: { harness: fake, model: anthropic/claude-sonnet-4-6 }
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
defaults: { harness: fake, model: anthropic/claude-sonnet-4-6 }
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
