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
