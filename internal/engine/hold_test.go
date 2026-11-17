package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

// A take-over: the human holds the agent's live session. While held, the end
// of a turn is not the end of the stage — the agent waits for the holder's
// next message — and the stage ends only after the release hands the task
// back with the fixed handback prompt. The hold is on the record with its
// duration, turns and cost.

// heldRunner scripts a runner that honours the inbox the way the ACP runner
// does: it takes messages at every turn end and, while held, waits for the
// next one instead of returning. Each turn it runs reports a fixed spend.
type heldRunner struct {
	mu       sync.Mutex
	turns    int
	batches  [][]agentport.UserMessage
	firstEnd chan struct{} // the stage's own turn ends only once this is closed
}

const turnCost = 0.1

func (r *heldRunner) turn(in agentport.AgentInput, text string, gate <-chan struct{}) {
	r.mu.Lock()
	r.turns++
	id := fmt.Sprintf("T%d", r.turns)
	r.mu.Unlock()
	in.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: id})
	if gate != nil {
		// Logged in order behind the turn's start, so a test can see the
		// engine read it: a take-over ahead of that would count the turn as
		// held.
		in.Events(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s"})
		<-gate
	}
	in.Events(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Turn: id, Text: text})
	in.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: id, StopReason: "end_turn",
		Usage: &agentport.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: turnCost}})
}

// drive is the ACP runner's inbox loop: deliver what is queued, wait while
// held, return once the hold is gone and nothing is queued.
func (r *heldRunner) drive(ctx context.Context, in agentport.AgentInput) (agentport.AgentResult, error) {
	for {
		msgs := in.Inbox.Take()
		for len(msgs) == 0 {
			if !in.Inbox.Held() {
				if msgs = in.Inbox.Take(); len(msgs) == 0 {
					r.mu.Lock()
					defer r.mu.Unlock()
					return agentport.AgentResult{Session: "s", TurnsUsed: r.turns, Usage: agentport.Usage{CostUSD: turnCost * float64(r.turns)}}, nil
				}
				break
			}
			select {
			case <-in.Inbox.Wake():
			case <-ctx.Done():
				return agentport.AgentResult{Session: "s"}, ctx.Err()
			}
			msgs = in.Inbox.Take()
		}
		r.mu.Lock()
		r.batches = append(r.batches, msgs)
		r.mu.Unlock()
		in.Events(agentport.AgentEvent{Kind: agentport.AgentEvUserMessage, Messages: msgs})
		r.turn(in, "answer: "+msgs[0].Text, nil)
	}
}

func (r *heldRunner) delivered() [][]agentport.UserMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]agentport.UserMessage(nil), r.batches...)
}

// takeOverWhenLive retries until the agent invocation is up to be taken.
func takeOverWhenLive(t *testing.T, h *harness, runID, user string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := h.e.TakeOver(runID, user)
		if err == nil {
			return
		}
		if !errors.Is(err, ErrNoAgentRunning) && !errors.Is(err, ErrRunNotActive) || time.Now().After(deadline) {
			t.Fatalf("take over: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestTakeOverKeepsTheSessionOpenUntilReleased(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	r := &heldRunner{firstEnd: make(chan struct{})}
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		// The stage's own turn: in flight when the human takes over.
		r.turn(call.In, "working on it", r.firstEnd)
		return r.drive(ctx, call.In)
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing to release yet; a second take is refused.
	waitEvent[*event.AgentSession](t, h, runID) // the turn is open
	takeOverWhenLive(t, h, runID, "u1")
	if err := h.e.TakeOver(runID, "u2"); !errors.Is(err, ErrAlreadyHeld) {
		t.Errorf("second take over = %v, want ErrAlreadyHeld", err)
	}
	waitEvent[*event.HoldTaken](t, h, runID)
	if v := h.view(t, runID); v.Hold == nil || v.Hold.UserID != "u1" || v.Hold.StageID != "build" {
		t.Fatalf("view.hold = %+v", v.Hold)
	}
	close(r.firstEnd)

	// The turn ends under the hold: the stage does NOT complete. The holder's
	// message is the next turn.
	id, err := h.e.SendMessage(runID, "use sqlite")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the message to be delivered", func() bool {
		d, ok := findPayload[*event.MessageDelivered](h.events(t, runID))
		return ok && len(d.MessageIDs) == 1 && d.MessageIDs[0] == id
	})
	time.Sleep(20 * time.Millisecond) // the answer's turn ends; still nothing completes
	if v := h.view(t, runID); v.State != run.Running {
		t.Fatalf("state under hold = %s, want running", v.State)
	}
	if _, ok := findPayload[*event.StageCompleted](h.events(t, runID)); ok {
		t.Fatal("the stage completed under a hold")
	}

	// Release: the hold closes with its account and the session gets the
	// handback as its last turn; then the stage ends as usual.
	if err := h.e.Release(runID, "u1"); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	rel, ok := findPayload[*event.HoldReleased](evs)
	if !ok || rel.Reason != event.HoldReleasedByUser || rel.UserID != "u1" || rel.StageID != "build" {
		t.Fatalf("hold_released = %+v", rel)
	}
	if rel.Turns != 1 || rel.Messages != 1 || rel.Usage == nil || rel.Usage.CostUSD < turnCost-1e-9 || rel.Usage.CostUSD > turnCost+1e-9 ||
		rel.Usage.InputTokens != 10 || rel.DurationMS < 0 {
		t.Errorf("hold account = %+v (usage %+v)", rel.HoldSpend, rel.Usage)
	}
	var kinds []string
	for _, ev := range evs {
		switch p := ev.Payload.(type) {
		case *event.HoldTaken, *event.HoldReleased, *event.MessageDelivered, *event.StageCompleted:
			kinds = append(kinds, string(ev.Type))
		case *event.AgentPrompted:
			kinds = append(kinds, "agent_prompted:"+p.Kind)
			if p.Kind == event.PromptHandback && p.Text != handbackPrompt {
				t.Errorf("handback text = %q", p.Text)
			}
		}
	}
	want := "agent_prompted:stage hold_taken message_delivered hold_released agent_prompted:handback stage_completed"
	if got := strings.Join(kinds, " "); got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
	batches := r.delivered()
	if len(batches) != 2 || batches[0][0].Text != "use sqlite" || !batches[1][0].Verbatim || batches[1][0].Text != handbackPrompt {
		t.Errorf("delivered = %+v", batches)
	}
	if v := h.view(t, runID); v.State != run.Completed || v.Hold != nil {
		t.Errorf("final view = %s hold=%+v", v.State, v.Hold)
	}
	// Nothing to hold or release once the run is over.
	if err := h.e.TakeOver(runID, "u1"); err == nil {
		t.Error("a finished run accepted a take over")
	}
	if err := h.e.Release(runID, "u1"); err == nil {
		t.Error("a finished run accepted a release")
	}
}

func TestReleaseWithoutAHoldIsRefused(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	gate := make(chan struct{})
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		<-gate
		return agentport.AgentResult{Session: "s", TurnsUsed: 1}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the agent to be live", func() bool {
		err := h.e.Release(runID, "u1")
		return errors.Is(err, ErrNotHeld)
	})
	close(gate)
	h.e.Wait(runID)
}

// A runner that returns underneath a hold (a harness that died, or one that
// cannot wait on an inbox) ends the hold with the attempt, on the record.
func TestHoldEndsWithTheAttempt(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	taken := make(chan struct{})
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		<-taken
		return agentport.AgentResult{Session: "s", TurnsUsed: 1}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	takeOverWhenLive(t, h, runID, "u1")
	close(taken)
	h.e.Wait(runID)
	evs := h.events(t, runID)
	rel, ok := findPayload[*event.HoldReleased](evs)
	if !ok || rel.Reason != event.HoldEndedWithStage || rel.UserID != "u1" {
		t.Fatalf("hold_released = %+v", rel)
	}
	if _, ok := findPayload[*event.StageCompleted](evs); !ok {
		t.Error("the stage did not complete")
	}
	if v := h.view(t, runID); v.Hold != nil {
		t.Errorf("view.hold = %+v after the run ended", v.Hold)
	}
}

// The holder goes quiet past the live hold: the run parks on them (the hold
// stays in force, the slot is freed) and their next message wakes it — the
// session resumes with that message as its prompt.
func TestHoldParksOnAQuietHolderAndWakesOnTheirMessage(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	h.e.cfg.AskHumanHold = 30 * time.Millisecond
	r := &heldRunner{firstEnd: make(chan struct{})}
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		r.turn(call.In, "working on it", r.firstEnd)
		return r.drive(ctx, call.In)
	}
	var resumed []string
	var rmu sync.Mutex
	h.runner.ResumeFunc = func(ctx context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
		rmu.Lock()
		resumed = append(resumed, call.Reply)
		rmu.Unlock()
		// The holder's message is this invocation's first turn.
		r.turn(call.In, "resumed: "+call.Reply, nil)
		return r.drive(ctx, call.In)
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.AgentSession](t, h, runID) // the turn is open
	takeOverWhenLive(t, h, runID, "u1")
	close(r.firstEnd)

	paused := waitEvent[*event.StagePaused](t, h, runID)
	if paused.Reason != event.PauseHeld || len(paused.PingIDs) != 0 || paused.Hold == nil {
		t.Fatalf("stage_paused = %+v", paused)
	}
	waitUntil(t, "the run to park", func() bool { return h.view(t, runID).Dormant })
	if v := h.view(t, runID); v.State != run.WaitingOnHuman || v.Hold == nil {
		t.Fatalf("parked view = %s hold=%+v", v.State, v.Hold)
	}
	// Still held while parked.
	if err := h.e.TakeOver(runID, "u2"); !errors.Is(err, ErrAlreadyHeld) {
		t.Errorf("take over while parked = %v, want ErrAlreadyHeld", err)
	}
	// One park is the case: the holder stays quiet after the resumed turn
	// too, and a slow machine must not reach the release a hold later. The
	// loop is gone, so nothing reads the hold while it changes.
	h.e.Wait(runID)
	h.e.cfg.AskHumanHold = time.Hour

	id, err := h.e.SendMessage(runID, "switch to sqlite")
	if err != nil {
		t.Fatalf("message to a parked hold: %v", err)
	}
	res := waitEvent[*event.StageResumed](t, h, runID)
	if res.Reason != event.ResumeMessage || res.StageID != "build" {
		t.Fatalf("stage_resumed = %+v", res)
	}
	waitUntil(t, "the resume to be prompted", func() bool {
		rmu.Lock()
		defer rmu.Unlock()
		return len(resumed) == 1
	})
	rmu.Lock()
	if !strings.Contains(resumed[0], "Message from the human operator:\n\nswitch to sqlite") || strings.Contains(resumed[0], "restarted") {
		t.Errorf("resume prompt = %q", resumed[0])
	}
	rmu.Unlock()
	d, ok := findPayload[*event.MessageDelivered](h.events(t, runID))
	if !ok || d.MessageIDs[0] != id {
		t.Errorf("message_delivered = %+v", d)
	}
	waitUntil(t, "the resumed turn to end", func() bool {
		evs := h.events(t, runID)
		n := 0
		for _, ev := range evs {
			if _, ok := ev.Payload.(*event.AgentMessage); ok {
				n++
			}
		}
		return n >= 2
	})
	if err := h.e.Release(runID, "u1"); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	rel, ok := findPayload[*event.HoldReleased](evs)
	if !ok || rel.Reason != event.HoldReleasedByUser {
		t.Fatalf("hold_released = %+v", rel)
	}
	// One held turn (the resumed one), one message, across the park.
	if rel.Turns != 1 || rel.Messages != 1 || rel.Usage == nil || rel.Usage.CostUSD < turnCost-1e-9 {
		t.Errorf("hold account across the park = %+v (usage %+v)", rel.HoldSpend, rel.Usage)
	}
	if v := h.view(t, runID); v.State != run.Completed || v.Hold != nil {
		t.Errorf("final view = %s hold=%+v", v.State, v.Hold)
	}
	if n := countType(evs, event.TypeRunUnpaused); n != 1 {
		t.Errorf("run_unpaused = %d, want 1", n)
	}
}

// Releasing a parked hold wakes the run for the handback alone.
func TestReleasingAParkedHoldResumesWithTheHandback(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	h.e.cfg.AskHumanHold = 30 * time.Millisecond
	r := &heldRunner{firstEnd: make(chan struct{})}
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		r.turn(call.In, "working on it", r.firstEnd)
		return r.drive(ctx, call.In)
	}
	var resumed []string
	var rmu sync.Mutex
	h.runner.ResumeFunc = func(ctx context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
		rmu.Lock()
		resumed = append(resumed, call.Reply)
		rmu.Unlock()
		r.turn(call.In, "finishing", nil)
		return r.drive(ctx, call.In)
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.AgentSession](t, h, runID) // the turn is open
	takeOverWhenLive(t, h, runID, "u1")
	close(r.firstEnd)
	waitEvent[*event.StagePaused](t, h, runID)

	if err := h.e.Release(runID, "u1"); err != nil {
		t.Fatalf("release of a parked hold: %v", err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	rel, ok := findPayload[*event.HoldReleased](evs)
	if !ok || rel.Reason != event.HoldReleasedByUser || rel.UserID != "u1" || rel.Turns != 0 {
		t.Fatalf("hold_released = %+v", rel)
	}
	res, ok := findPayload[*event.StageResumed](evs)
	if !ok || res.Reason != event.ResumeReleased {
		t.Fatalf("stage_resumed = %+v", res)
	}
	rmu.Lock()
	if len(resumed) != 1 || resumed[0] != handbackPrompt {
		t.Errorf("resume prompt = %q", resumed)
	}
	rmu.Unlock()
	var kinds []string
	for _, ev := range evs {
		switch p := ev.Payload.(type) {
		case *event.HoldReleased, *event.StageResumed, *event.StageCompleted, *event.RunUnpaused:
			kinds = append(kinds, string(ev.Type))
		case *event.AgentPrompted:
			if p.Kind == event.PromptHandback {
				kinds = append(kinds, "agent_prompted:handback")
			}
		}
	}
	want := "hold_released run_unpaused stage_resumed agent_prompted:handback stage_completed"
	if got := strings.Join(kinds, " "); got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
	if v := h.view(t, runID); v.State != run.Completed || v.Hold != nil {
		t.Errorf("final view = %s hold=%+v", v.State, v.Hold)
	}
}

// START HELD: a stage with start_held opens every attempt as a take-over by
// the run's owner. The agent answers the stage prompt, then waits for them;
// from there it is the take-over above.

// startHeldDoc's plan stage starts held; it retries itself once on a
// failure, and the gate sends it back once.
const startHeldDoc = `name: held
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: planner
    prompt: plan it with me
    outputs:
      - { name: plan, type: file }
main:
  - { id: plan, type: agent, harness: $harness, model: $model, agent: planner, start_held: true, output: { plan: { to: artifacts/plan.md, type: file } }, on_fail: { to: plan, max: 1 } }
  - { id: gate, type: human_gate, question: "Good?", on_reject: { to: plan, max: 1 } }
`

// startHeldHarness serves startHeldDoc, its runs owned by "owner". The
// runner writes the plan unless skip says not to for the attempt it is in
// (1-based, one invocation per attempt).
func startHeldHarness(t *testing.T, skip func(attempt int) bool) (*harness, *heldRunner) {
	t.Helper()
	return startHeldHarnessOn(t, nil, skip)
}

// startHeldHarnessOn is startHeldHarness on the store store makes (newHarnessOn).
func startHeldHarnessOn(t *testing.T, store func(*event.MemStore) event.Store, skip func(attempt int) bool) (*harness, *heldRunner) {
	t.Helper()
	h := newHarnessOn(t, store, mustFlow(t, startHeldDoc))
	h.res.UserID = "owner"
	r := &heldRunner{}
	var calls sync.Mutex
	n := 0
	work := func(ctx context.Context, env envport.EnvID, in agentport.AgentInput) (agentport.AgentResult, error) {
		calls.Lock()
		n++
		attempt := n
		calls.Unlock()
		if skip == nil || !skip(attempt) {
			_ = h.envp.WriteFile(ctx, env, "artifacts/plan.md", []byte("# plan"), 0o644)
		}
		r.turn(in, "a first draft", nil)
		return r.drive(ctx, in)
	}
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		return work(ctx, call.Env, call.In)
	}
	h.runner.ResumeFunc = func(ctx context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
		return work(ctx, call.Env, call.In)
	}
	return h, r
}

// waitHeldNotices waits for the owner to have been told n times that a
// stage waits for them: each time, the opening turn of an attempt is over.
func waitHeldNotices(t *testing.T, h *harness, n int) {
	t.Helper()
	waitUntil(t, fmt.Sprintf("held notification %d", n), func() bool { return len(sentOfType(h, notifyHeld)) >= n })
}

func TestStartHeldOpensInTheOwnersHands(t *testing.T) {
	h, r := startHeldHarness(t, nil)
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "held", testProject)
	if err != nil {
		t.Fatal(err)
	}
	// One turn on the stage prompt, then the session waits on the owner.
	waitHeldNotices(t, h, 1)
	if v := h.view(t, runID); v.State != run.Running || v.Hold == nil || v.Hold.UserID != "owner" || v.Hold.StageID != "plan" {
		t.Fatalf("view = %s hold=%+v", v.State, v.Hold)
	}
	if n := sentOfType(h, notifyHeld)[0]; n.RunID != runID || n.UserID != "owner" || !strings.Contains(n.Title, "plan") {
		t.Errorf("notification = %+v", n)
	}
	if _, ok := findPayload[*event.StageCompleted](h.events(t, runID)); ok {
		t.Fatal("the stage ended after its opening turn")
	}

	// The owner's message is the next turn, in the same session.
	id, err := h.e.SendMessage(runID, "smaller, please")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the owner's turn to end", func() bool {
		return countType(h.events(t, runID), event.TypeAgentMessage) >= 2
	})
	if err := h.e.Release(runID, "owner"); err != nil {
		t.Fatal(err)
	}
	gate := h.waitPing(t, PingGate)
	if err := h.e.AnswerPing(gate.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	evs := h.events(t, runID)
	var kinds []string
	for _, ev := range evs {
		switch p := ev.Payload.(type) {
		case *event.HoldTaken:
			kinds = append(kinds, string(ev.Type))
			if p.UserID != "owner" || p.Attempt != 1 || !p.Start {
				t.Errorf("hold_taken = %+v", p)
			}
		case *event.HoldOpened, *event.HoldReleased, *event.MessageDelivered:
			kinds = append(kinds, string(ev.Type))
		case *event.StageCompleted:
			if p.StageID == "plan" {
				kinds = append(kinds, string(ev.Type))
			}
		case *event.AgentPrompted:
			kinds = append(kinds, "agent_prompted:"+p.Kind)
		}
	}
	want := "hold_taken agent_prompted:stage hold_opened message_delivered hold_released agent_prompted:handback stage_completed"
	if got := strings.Join(kinds, " "); got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
	// The hold's account is the owner's turn alone: the opening one answered
	// the stage prompt.
	rel, _ := findPayload[*event.HoldReleased](evs)
	if rel == nil || rel.Reason != event.HoldReleasedByUser || rel.Turns != 1 || rel.Messages != 1 ||
		rel.Usage == nil || rel.Usage.CostUSD < turnCost-1e-9 || rel.Usage.CostUSD > turnCost+1e-9 {
		t.Errorf("hold_released = %+v", rel)
	}
	if d, ok := findPayload[*event.MessageDelivered](evs); !ok || d.MessageIDs[0] != id {
		t.Errorf("message_delivered = %+v", d)
	}
	if b := r.delivered(); len(b) != 2 || b[0][0].Text != "smaller, please" || b[1][0].Text != handbackPrompt {
		t.Errorf("delivered = %+v", b)
	}
	if n := len(sentOfType(h, notifyHeld)); n != 1 {
		t.Errorf("held notifications = %d, want one: later turns tell nobody", n)
	}
	if v := h.view(t, runID); v.State != run.Completed || v.Hold != nil {
		t.Errorf("final view = %s hold=%+v", v.State, v.Hold)
	}
}

// heldOrder is the hold's story on a start-held run's log, plan stage only.
func heldOrder(evs []event.Event) string {
	var kinds []string
	for _, ev := range evs {
		switch p := ev.Payload.(type) {
		case *event.HoldTaken, *event.HoldOpened, *event.HoldReleased, *event.MessageDelivered:
			kinds = append(kinds, string(ev.Type))
		case *event.StageCompleted:
			if p.StageID == "plan" {
				kinds = append(kinds, string(ev.Type))
			}
		case *event.AgentPrompted:
			kinds = append(kinds, "agent_prompted:"+p.Kind)
		}
	}
	return strings.Join(kinds, " ")
}

// The run shows in the owner's hands from hold_taken on, while the opening
// prompt is still on its way: what they do then reaches the attempt. A
// message is delivered once, with the stage prompt; a release follows the
// opening turn.
func TestStartHeldAdmitsTheOwnerBeforeItsPrompt(t *testing.T) {
	finish := func(t *testing.T, h *harness, runID string) []event.Event {
		t.Helper()
		gate := h.waitPing(t, PingGate)
		if err := h.e.AnswerPing(gate.ID, PingAnswer{Answer: "approve"}); err != nil {
			t.Fatal(err)
		}
		h.e.Wait(runID)
		if v := h.view(t, runID); v.State != run.Completed || v.Hold != nil {
			t.Errorf("final view = %s hold=%+v", v.State, v.Hold)
		}
		return h.events(t, runID)
	}
	// inOpeningWindow acts while the loop is stalled on its way to the
	// opening prompt, once the run shows the hold.
	inOpeningWindow := func(t *testing.T, h *harness, s *stallStore, runID string, act func() error) {
		t.Helper()
		err := inWindow(t, s, func() error {
			if v, err := h.e.store.Events(context.Background(), runID, 0); err != nil {
				return err
			} else if hold := run.Reduce(v).Hold; hold == nil || hold.UserID != "owner" {
				return fmt.Errorf("the run does not show the hold yet: %+v", hold)
			}
			return act()
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	t.Run("message", func(t *testing.T) {
		s, store := stallAfter(event.TypeHoldTaken)
		h, r := startHeldHarnessOn(t, store, nil)
		runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "held", testProject)
		if err != nil {
			t.Fatal(err)
		}
		var id string
		inOpeningWindow(t, h, s, runID, func() (err error) {
			id, err = h.e.SendMessage(runID, "smaller, please")
			return err
		})
		waitHeldNotices(t, h, 1)
		if err := h.e.Release(runID, "owner"); err != nil {
			t.Fatal(err)
		}
		evs := finish(t, h, runID)
		want := "hold_taken message_delivered agent_prompted:stage hold_opened hold_released agent_prompted:handback stage_completed"
		if got := heldOrder(evs); got != want {
			t.Errorf("order = %s\nwant    %s", got, want)
		}
		if n := countType(evs, event.TypeMessageQueued); n != 1 {
			t.Errorf("message_queued = %d, want one", n)
		}
		if d, ok := findPayload[*event.MessageDelivered](evs); !ok || len(d.MessageIDs) != 1 || d.MessageIDs[0] != id {
			t.Errorf("message_delivered = %+v", d)
		}
		if p, _ := findPayload[*event.AgentPrompted](evs); p == nil || p.Kind != event.PromptStage ||
			!strings.HasPrefix(p.Text, "# OUTPUT CONTRACT") || !strings.HasSuffix(p.Text, "smaller, please") {
			t.Errorf("stage prompt = %+v, want the message riding with it", p)
		}
		if b := r.delivered(); len(b) != 1 || b[0][0].Text != handbackPrompt {
			t.Errorf("delivered = %+v, want the handback alone after the opening turn", b)
		}
		if rel, _ := findPayload[*event.HoldReleased](evs); rel == nil || rel.Turns != 0 || rel.Messages != 1 {
			t.Errorf("hold_released = %+v, want the owner's message and no turn of theirs", rel)
		}
	})

	// A message sent while the stage prompt is being written missed it: the
	// send waits for the write, and the message is the owner's turn after
	// the opening one — admitted once, never half in the prompt.
	t.Run("message while the prompt is written", func(t *testing.T) {
		s, store := stallAt(event.TypeAgentPrompted)
		h, r := startHeldHarnessOn(t, store, nil)
		runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "held", testProject)
		if err != nil {
			t.Fatal(err)
		}
		var id string
		inOpeningWindow(t, h, s, runID, func() (err error) {
			if id, err = h.e.SendMessage(runID, "smaller, please"); err != nil {
				return err
			}
			evs, err := h.e.store.Events(context.Background(), runID, 0)
			if err != nil {
				return err
			}
			if _, ok := findPayload[*event.AgentPrompted](evs); !ok {
				return errors.New("the send came back before the stage prompt was written")
			}
			return nil
		})
		waitUntil(t, "the owner's turn to end", func() bool {
			return countType(h.events(t, runID), event.TypeAgentMessage) >= 2
		})
		if err := h.e.Release(runID, "owner"); err != nil {
			t.Fatal(err)
		}
		evs := finish(t, h, runID)
		want := "hold_taken agent_prompted:stage hold_opened message_delivered hold_released agent_prompted:handback stage_completed"
		if got := heldOrder(evs); got != want {
			t.Errorf("order = %s\nwant    %s", got, want)
		}
		if n := countType(evs, event.TypeMessageQueued); n != 1 {
			t.Errorf("message_queued = %d, want one", n)
		}
		if p, _ := findPayload[*event.AgentPrompted](evs); p == nil || p.Kind != event.PromptStage || strings.Contains(p.Text, "smaller, please") {
			t.Errorf("stage prompt = %+v, want it without the message it missed", p)
		}
		if d, ok := findPayload[*event.MessageDelivered](evs); !ok || len(d.MessageIDs) != 1 || d.MessageIDs[0] != id {
			t.Errorf("message_delivered = %+v", d)
		}
		if b := r.delivered(); len(b) != 2 || b[0][0].Text != "smaller, please" || b[1][0].Text != handbackPrompt {
			t.Errorf("delivered = %+v, want the message after the opening turn, then the handback", b)
		}
		if rel, _ := findPayload[*event.HoldReleased](evs); rel == nil || rel.Turns != 1 || rel.Messages != 1 {
			t.Errorf("hold_released = %+v, want the owner's message and their one turn", rel)
		}
	})

	t.Run("release", func(t *testing.T) {
		s, store := stallAt(event.TypeAgentPrompted)
		h, r := startHeldHarnessOn(t, store, nil)
		runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "held", testProject)
		if err != nil {
			t.Fatal(err)
		}
		inOpeningWindow(t, h, s, runID, func() error { return h.e.Release(runID, "owner") })
		evs := finish(t, h, runID)
		want := "hold_taken agent_prompted:stage hold_opened hold_released agent_prompted:handback stage_completed"
		if got := heldOrder(evs); got != want {
			t.Errorf("order = %s\nwant    %s", got, want)
		}
		if b := r.delivered(); len(b) != 1 || b[0][0].Text != handbackPrompt {
			t.Errorf("delivered = %+v, want the handback alone after the opening turn", b)
		}
		if rel, _ := findPayload[*event.HoldReleased](evs); rel == nil || rel.Reason != event.HoldReleasedByUser || rel.Turns != 0 {
			t.Errorf("hold_released = %+v", rel)
		}
	})
}

// Every attempt starts held: a retry after the output check failed on the
// release, and a redo the gate sent back.
func TestStartHeldHoldsEveryAttempt(t *testing.T) {
	h, _ := startHeldHarness(t, func(attempt int) bool { return attempt == 1 })
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "held", testProject)
	if err != nil {
		t.Fatal(err)
	}
	var rejected string
	for n := 1; n <= 3; n++ {
		waitHeldNotices(t, h, n)
		if err := h.e.Release(runID, "owner"); err != nil {
			t.Fatalf("release %d: %v", n, err)
		}
		if n == 2 {
			gate := h.waitPing(t, PingGate)
			if err := h.e.AnswerPing(gate.ID, PingAnswer{Answer: "reject", Feedback: "again"}); err != nil {
				t.Fatal(err)
			}
			rejected = gate.ID
		}
	}
	gate := h.waitPing(t, PingGate, rejected)
	if err := h.e.AnswerPing(gate.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s", v.State)
	}
	evs := h.events(t, runID)
	if fs := failuresOf(evs, "plan"); len(fs) != 1 || fs[0].Reason != event.ReasonOutputContract {
		t.Fatalf("plan failures = %+v", fs)
	}
	// Each attempt's hold is taken before its stage prompt.
	var seq []string
	for _, ev := range evs {
		switch p := ev.Payload.(type) {
		case *event.StageStarted:
			if p.StageID == "plan" {
				seq = append(seq, fmt.Sprintf("start:%d", p.Attempt))
			}
		case *event.HoldTaken:
			seq = append(seq, fmt.Sprintf("held:%d:%s", p.Attempt, p.UserID))
		case *event.AgentPrompted:
			if p.Kind == event.PromptStage {
				seq = append(seq, fmt.Sprintf("prompt:%d", p.Attempt))
			}
		}
	}
	want := "start:1 held:1:owner prompt:1 start:2 held:2:owner prompt:2 start:3 held:3:owner prompt:3"
	if got := strings.Join(seq, " "); got != want {
		t.Errorf("attempts = %s\nwant       %s", got, want)
	}
}

// A start hold whose owner stays quiet parks after the opening turn (the
// slot freed, the hold in force) and their next message wakes it.
func TestStartHeldParksOnAQuietOwner(t *testing.T) {
	h, _ := startHeldHarness(t, nil)
	h.e.cfg.AskHumanHold = 30 * time.Millisecond
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "held", testProject)
	if err != nil {
		t.Fatal(err)
	}
	paused := waitEvent[*event.StagePaused](t, h, runID)
	if paused.Reason != event.PauseHeld || paused.StageID != "plan" {
		t.Fatalf("stage_paused = %+v", paused)
	}
	waitUntil(t, "the run to park", func() bool { return h.view(t, runID).Dormant })
	h.e.Wait(runID)
	h.e.cfg.AskHumanHold = time.Hour
	if v := h.view(t, runID); v.State != run.WaitingOnHuman || v.Hold == nil || v.Hold.UserID != "owner" {
		t.Fatalf("parked view = %s hold=%+v", v.State, v.Hold)
	}
	if n := len(sentOfType(h, notifyHeld)); n != 1 {
		t.Errorf("held notifications = %d, want one", n)
	}

	if _, err := h.e.SendMessage(runID, "go on"); err != nil {
		t.Fatalf("message to a parked start hold: %v", err)
	}
	if res := waitEvent[*event.StageResumed](t, h, runID); res.Reason != event.ResumeMessage {
		t.Fatalf("stage_resumed = %+v", res)
	}
	waitUntil(t, "the owner's turn to end", func() bool {
		return countType(h.events(t, runID), event.TypeAgentMessage) >= 2
	})
	if err := h.e.Release(runID, "owner"); err != nil {
		t.Fatal(err)
	}
	gate := h.waitPing(t, PingGate)
	if err := h.e.AnswerPing(gate.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if n := countType(evs, event.TypeHoldTaken); n != 1 {
		t.Errorf("hold_taken = %d, want the one the attempt opened with", n)
	}
	if rel, _ := findPayload[*event.HoldReleased](evs); rel == nil || rel.Turns != 1 || rel.Messages != 1 {
		t.Errorf("hold_released = %+v", rel)
	}
	if n := len(sentOfType(h, notifyHeld)); n != 1 {
		t.Errorf("held notifications = %d, want one: a woken turn tells nobody", n)
	}
	if v := h.view(t, runID); v.State != run.Completed {
		t.Errorf("state = %s", v.State)
	}
}

// A crash before the opening turn ran — between the start hold and the
// stage prompt, or after the prompt but before the harness recorded a
// session: recovery finds the hold in force, still owed its opening turn,
// and sends the stage prompt. It neither parks on the owner before the agent
// has said anything nor takes the hold again, and the turn it runs is the
// agent's own: the owner hears of its end, and the hold's account is empty.
func TestRecoveredStartHoldSendsTheStagePrompt(t *testing.T) {
	cases := []struct {
		name    string
		seed    []event.Payload
		prompts int
	}{
		{"before the prompt", nil, 1},
		{"prompted with no session", []event.Payload{&event.AgentPrompted{StageID: "plan", Attempt: 1, Kind: event.PromptStage, Text: "plan it with me"}}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fl := mustFlow(t, startHeldDoc)
			h, _ := startHeldHarness(t, nil)
			seed := append([]event.Payload{
				&event.PhaseStarted{Phase: "main", By: "human"},
				&event.StageStarted{StageID: "plan", Attempt: 1},
				&event.HoldTaken{StageID: "plan", Attempt: 1, UserID: "owner", Start: true},
			}, tc.seed...)
			seedRun(t, h, "r-sh", "held", fl.Hash, seed...)
			if err := h.e.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitHeldNotices(t, h, 1)
			evs := h.events(t, "r-sh")
			if n := countType(evs, event.TypeHoldTaken); n != 1 {
				t.Errorf("hold_taken = %d, want the one before the crash", n)
			}
			if n := countType(evs, event.TypeStagePaused); n != 0 {
				t.Errorf("parked before the opening turn: %v", eventTypes(evs))
			}
			var prompts []string
			for _, ev := range evs {
				if p, ok := ev.Payload.(*event.AgentPrompted); ok {
					prompts = append(prompts, p.Kind)
				}
			}
			if len(prompts) != tc.prompts || prompts[len(prompts)-1] != event.PromptStage {
				t.Errorf("agent_prompted = %v, want the stage prompt sent", prompts)
			}
			if err := h.e.Release("r-sh", "owner"); err != nil {
				t.Fatal(err)
			}
			gate := h.waitPing(t, PingGate)
			if err := h.e.AnswerPing(gate.ID, PingAnswer{Answer: "approve"}); err != nil {
				t.Fatal(err)
			}
			h.e.Wait("r-sh")
			evs = h.events(t, "r-sh")
			if v := h.view(t, "r-sh"); v.State != run.Completed {
				t.Errorf("state = %s: %v", v.State, eventTypes(evs))
			}
			if n := countType(evs, event.TypeHoldOpened); n != 1 {
				t.Errorf("hold_opened = %d, want one", n)
			}
			if rel, _ := findPayload[*event.HoldReleased](evs); rel == nil || rel.Turns != 0 || rel.Usage != nil {
				t.Errorf("hold_released = %+v, want the opening turn off the account", rel)
			}
			if n := len(sentOfType(h, notifyHeld)); n != 1 {
				t.Errorf("held notifications = %d, want one", n)
			}
		})
	}
}

// A crash after the opening prompt went out with an owner's message, before
// the harness recorded a session: the stage prompt sent again carries that
// message again, and it stays on the hold's account — once.
func TestRecoveredStartHoldKeepsTheOpeningMessage(t *testing.T) {
	fl := mustFlow(t, startHeldDoc)
	h, r := startHeldHarness(t, nil)
	seedRun(t, h, "r-sm", "held", fl.Hash,
		&event.PhaseStarted{Phase: "main", By: "human"},
		&event.StageStarted{StageID: "plan", Attempt: 1},
		&event.HoldTaken{StageID: "plan", Attempt: 1, UserID: "owner", Start: true},
		&event.MessageQueued{MessageID: "m1", Text: "smaller, please", StageID: "plan"},
		&event.MessageDelivered{MessageIDs: []string{"m1"}, StageID: "plan", Attempt: 1},
		&event.AgentPrompted{StageID: "plan", Attempt: 1, Kind: event.PromptStage, Text: "plan it with me\n\n---\n\nsmaller, please"},
	)
	if err := h.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitHeldNotices(t, h, 1)
	if calls := h.runner.SnapshotRunCalls(); len(calls) != 1 || !strings.Contains(calls[0].In.Prompt, "smaller, please") {
		t.Errorf("run calls = %+v, want the stage prompt with the owner's message", calls)
	}
	if err := h.e.Release("r-sm", "owner"); err != nil {
		t.Fatal(err)
	}
	gate := h.waitPing(t, PingGate)
	if err := h.e.AnswerPing(gate.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-sm")
	evs := h.events(t, "r-sm")
	if v := h.view(t, "r-sm"); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(evs))
	}
	if n := countType(evs, event.TypeMessageDelivered); n != 1 {
		t.Errorf("message_delivered = %d, want the one before the crash", n)
	}
	if b := r.delivered(); len(b) != 1 || b[0][0].Text != handbackPrompt {
		t.Errorf("delivered = %+v, want the handback alone after the opening turn", b)
	}
	if rel, _ := findPayload[*event.HoldReleased](evs); rel == nil || rel.Messages != 1 || rel.Turns != 0 {
		t.Errorf("hold_released = %+v, want the owner's message and no turn of theirs", rel)
	}
}

// The opening prompt lands with the messages it carries or not at all: a
// write that fails leaves no message delivered to a prompt the log lacks,
// and the owner hears their send was not taken.
func TestStartHeldOpeningPromptLandsWhole(t *testing.T) {
	s, stall := stallAfter(event.TypeHoldTaken)
	flaky := &flakyStore{typ: event.TypeAgentPrompted}
	flaky.left.Store(1)
	h, _ := startHeldHarnessOn(t, func(m *event.MemStore) event.Store {
		flaky.Store = m
		stall(m)
		s.Store = flaky
		return s
	}, nil)
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "held", testProject)
	if err != nil {
		t.Fatal(err)
	}
	err = inWindow(t, s, func() error {
		_, err := h.e.SendMessage(runID, "smaller, please")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("send = %v, want the failed write", err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	for _, typ := range []event.Type{event.TypeMessageQueued, event.TypeMessageDelivered, event.TypeAgentPrompted} {
		if n := countType(evs, typ); n != 0 {
			t.Errorf("%s = %d after the failed write: %v", typ, n, eventTypes(evs))
		}
	}
	if err := h.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitHeldNotices(t, h, 1)
	if err := h.e.Release(runID, "owner"); err != nil {
		t.Fatal(err)
	}
	gate := h.waitPing(t, PingGate)
	if err := h.e.AnswerPing(gate.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs = h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(evs))
	}
	if p, _ := findPayload[*event.AgentPrompted](evs); p == nil || strings.Contains(p.Text, "smaller, please") {
		t.Errorf("stage prompt = %+v, want it without the send that was refused", p)
	}
	if rel, _ := findPayload[*event.HoldReleased](evs); rel == nil || rel.Messages != 0 {
		t.Errorf("hold_released = %+v, want no message of theirs", rel)
	}
}

// heldReattacher is a harness whose agent outlived an engine restart in the
// middle of a start hold's opening turn. Reattached, it ends that turn —
// seen from its start, or only from its end when the start lay before the
// recorded cursor — then serves the session as heldRunner does.
type heldReattacher struct {
	*fake.AgentRunner
	held      *heldRunner
	fromStart bool
	mu        sync.Mutex
	attached  int
}

func (r *heldReattacher) Reattachable(context.Context, envport.EnvID, agentport.SessionID, string) bool {
	return true
}

func (r *heldReattacher) Reattach(ctx context.Context, _ envport.EnvID, _ agentport.SessionID, _ string, in agentport.AgentInput) (agentport.AgentResult, bool, error) {
	r.mu.Lock()
	r.attached++
	r.mu.Unlock()
	if r.fromStart {
		r.held.turn(in, "a first draft", nil)
	} else {
		in.Events(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Turn: "T0", Text: "a first draft"})
		in.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "T0", StopReason: "end_turn",
			Usage: &agentport.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: turnCost}})
	}
	res, err := r.held.drive(ctx, in)
	return res, true, err
}

// A restart during a start hold's opening turn follows that turn, and it is
// still the opening one: its end tells the owner (once) and stays off the
// hold's account, which holds the owner's turn alone.
func TestRecoveredStartHoldFollowsTheOpeningTurn(t *testing.T) {
	for _, fromStart := range []bool{true, false} {
		t.Run(fmt.Sprintf("turn start seen %v", fromStart), func(t *testing.T) {
			fl := mustFlow(t, startHeldDoc)
			h, r := startHeldHarness(t, nil)
			ra := &heldReattacher{AgentRunner: h.runner, held: r, fromStart: fromStart}
			h.res.Runner = ra
			envID := seedRun(t, h, "r-sr", "held", fl.Hash,
				&event.PhaseStarted{Phase: "main", By: "human"},
				&event.StageStarted{StageID: "plan", Attempt: 1},
				&event.HoldTaken{StageID: "plan", Attempt: 1, UserID: "owner", Start: true},
				&event.AgentPrompted{StageID: "plan", Attempt: 1, Kind: event.PromptStage, Text: "plan it with me"},
				&event.AgentSession{StageID: "plan", Attempt: 1, SessionID: "s"},
			)
			if err := h.envp.WriteFile(context.Background(), envID, "artifacts/plan.md", []byte("# plan"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := h.e.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitHeldNotices(t, h, 1)
			if _, err := h.e.SendMessage("r-sr", "smaller, please"); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, "the owner's turn to end", func() bool {
				return countType(h.events(t, "r-sr"), event.TypeAgentMessage) >= 2
			})
			if err := h.e.Release("r-sr", "owner"); err != nil {
				t.Fatal(err)
			}
			gate := h.waitPing(t, PingGate)
			if err := h.e.AnswerPing(gate.ID, PingAnswer{Answer: "approve"}); err != nil {
				t.Fatal(err)
			}
			h.e.Wait("r-sr")
			evs := h.events(t, "r-sr")
			if v := h.view(t, "r-sr"); v.State != run.Completed {
				t.Fatalf("state = %s: %v", v.State, eventTypes(evs))
			}
			if ra.attached != 1 || len(h.runner.SnapshotRunCalls())+len(h.runner.ResumeCalls) != 0 {
				t.Errorf("reattached %d, run/resume %d/%d: want the live turn followed", ra.attached,
					len(h.runner.SnapshotRunCalls()), len(h.runner.ResumeCalls))
			}
			if n := countType(evs, event.TypeHoldOpened); n != 1 {
				t.Errorf("hold_opened = %d, want one", n)
			}
			if n := len(sentOfType(h, notifyHeld)); n != 1 {
				t.Errorf("held notifications = %d, want one", n)
			}
			rel, _ := findPayload[*event.HoldReleased](evs)
			if rel == nil || rel.Turns != 1 || rel.Messages != 1 ||
				rel.Usage == nil || rel.Usage.CostUSD < turnCost-1e-9 || rel.Usage.CostUSD > turnCost+1e-9 {
				t.Errorf("hold_released = %+v, want the owner's turn alone", rel)
			}
		})
	}
}
