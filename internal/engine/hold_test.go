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
// A stoppable one also ends its gated turn, cancelled, when a forced
// take-over asks to stop it; otherwise it ignores the request.
type heldRunner struct {
	mu        sync.Mutex
	turns     int
	batches   [][]agentport.UserMessage
	firstEnd  chan struct{} // the stage's own turn ends only once this is closed
	stoppable bool
	stopped   []string // the turns it stopped
}

const turnCost = 0.1

func (r *heldRunner) turn(in agentport.AgentInput, text string, gate <-chan struct{}) {
	r.mu.Lock()
	r.turns++
	id := fmt.Sprintf("T%d", r.turns)
	r.mu.Unlock()
	in.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: id})
	stop := "end_turn"
	if gate != nil {
		// Logged in order behind the turn's start, so a test can see the
		// engine read it: a take-over ahead of that would count the turn as
		// held.
		in.Events(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s"})
		if !r.await(in, id, gate) {
			stop = "cancelled"
		}
	}
	if stop == "end_turn" {
		in.Events(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Turn: id, Text: text})
	}
	in.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: id, StopReason: stop,
		Usage: &agentport.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: turnCost}})
}

// await holds turn id until its gate opens (true) or, stoppable, until a
// forced take-over stops it (false).
func (r *heldRunner) await(in agentport.AgentInput, id string, gate <-chan struct{}) bool {
	if !r.stoppable {
		<-gate
		return true
	}
	for {
		select {
		case <-gate:
			return true
		case <-in.Inbox.Wake():
			if in.Inbox.Interrupted(id) {
				r.mu.Lock()
				r.stopped = append(r.stopped, id)
				r.mu.Unlock()
				return false
			}
		}
	}
}

func (r *heldRunner) stoppedTurns() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.stopped...)
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
func takeOverWhenLive(t *testing.T, h *harness, runID, user string, force bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := h.e.TakeOver(runID, user, force)
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
	takeOverWhenLive(t, h, runID, "u1", true)
	if err := h.e.TakeOver(runID, "u2", true); !errors.Is(err, ErrAlreadyHeld) {
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
	if err := h.e.TakeOver(runID, "u1", true); err == nil {
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
	takeOverWhenLive(t, h, runID, "u1", true)
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
	takeOverWhenLive(t, h, runID, "u1", true)
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
	if err := h.e.TakeOver(runID, "u2", true); !errors.Is(err, ErrAlreadyHeld) {
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
	takeOverWhenLive(t, h, runID, "u1", true)
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

// holdOrder is the take-over's path on the record.
func holdOrder(evs []event.Event) string {
	var kinds []string
	for _, ev := range evs {
		switch p := ev.Payload.(type) {
		case *event.HoldTaken, *event.HoldReleased, *event.MessageDelivered, *event.StageCompleted:
			kinds = append(kinds, string(ev.Type))
		case *event.AgentPrompted:
			kinds = append(kinds, "agent_prompted:"+p.Kind)
		}
	}
	return strings.Join(kinds, " ")
}

// agentSaid reports whether the agent's transcript holds text.
func agentSaid(evs []event.Event, text string) bool {
	return logHas(evs, func(m *event.AgentMessage) bool { return m.Text == text })
}

// waitDone fails the test unless the run ends within a few seconds.
func waitDone(t *testing.T, h *harness, runID string) {
	t.Helper()
	done := make(chan struct{})
	go func() { h.e.Wait(runID); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not end")
	}
}

// A forced take-over stops the agent's own turn at once: the agent waits on
// the holder without finishing its work, and the stopped turn is not on the
// hold's account.
func TestAForcedTakeOverStopsTheAgentsTurn(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	r := &heldRunner{firstEnd: make(chan struct{}), stoppable: true} // the gate never opens
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		r.turn(call.In, "working on it", r.firstEnd)
		return r.drive(ctx, call.In)
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.AgentSession](t, h, runID) // the turn is open
	takeOverWhenLive(t, h, runID, "u1", true)
	waitUntil(t, "the agent's turn to stop", func() bool { return len(r.stoppedTurns()) == 1 })
	if got := r.stoppedTurns(); got[0] != "T1" {
		t.Fatalf("stopped = %v, want the agent's own turn", got)
	}
	time.Sleep(20 * time.Millisecond) // the stopped turn ends; nothing completes
	if v := h.view(t, runID); v.State != run.Running || v.Hold == nil {
		t.Fatalf("after the stop = %s hold=%+v", v.State, v.Hold)
	}
	if _, ok := findPayload[*event.StageCompleted](h.events(t, runID)); ok {
		t.Fatal("a stopped turn completed the stage")
	}

	if _, err := h.e.SendMessage(runID, "use sqlite"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the message to be answered", func() bool { return agentSaid(h.events(t, runID), "answer: use sqlite") })
	if err := h.e.Release(runID, "u1"); err != nil {
		t.Fatal(err)
	}
	waitDone(t, h, runID)
	evs := h.events(t, runID)
	want := "agent_prompted:stage hold_taken message_delivered hold_released agent_prompted:handback stage_completed"
	if got := holdOrder(evs); got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
	if agentSaid(evs, "working on it") {
		t.Error("the stopped turn finished its work")
	}
	rel, _ := findPayload[*event.HoldReleased](evs)
	if rel == nil || rel.Turns != 1 || rel.Messages != 1 || rel.Usage == nil ||
		rel.Usage.CostUSD < turnCost-1e-9 || rel.Usage.CostUSD > turnCost+1e-9 {
		t.Errorf("hold account = %+v", rel)
	}
	if v := h.view(t, runID); v.State != run.Completed || v.Hold != nil {
		t.Errorf("final view = %s hold=%+v", v.State, v.Hold)
	}
}

// A passive take-over leaves the agent's turn alone: it runs to its end, and
// only then does the agent wait on the holder.
func TestAPassiveTakeOverLetsTheTurnFinish(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	r := &heldRunner{firstEnd: make(chan struct{}), stoppable: true}
	inbox := make(chan agentport.Inbox, 1)
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		inbox <- call.In.Inbox
		r.turn(call.In, "working on it", r.firstEnd)
		return r.drive(ctx, call.In)
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	in := <-inbox
	waitEvent[*event.AgentSession](t, h, runID) // the turn is open
	takeOverWhenLive(t, h, runID, "u1", false)
	waitEvent[*event.HoldTaken](t, h, runID)
	time.Sleep(20 * time.Millisecond)
	if in.Interrupted("T1") || len(r.stoppedTurns()) != 0 {
		t.Fatal("a passive take-over stopped the agent's turn")
	}
	if agentSaid(h.events(t, runID), "working on it") {
		t.Fatal("the turn ended before its gate opened")
	}

	close(r.firstEnd)
	waitUntil(t, "the agent's turn to finish", func() bool { return agentSaid(h.events(t, runID), "working on it") })
	time.Sleep(20 * time.Millisecond) // the turn ended under the hold; nothing completes
	if v := h.view(t, runID); v.State != run.Running || v.Hold == nil {
		t.Fatalf("after the turn = %s hold=%+v", v.State, v.Hold)
	}
	if err := h.e.Release(runID, "u1"); err != nil {
		t.Fatal(err)
	}
	waitDone(t, h, runID)
	evs := h.events(t, runID)
	if got, want := holdOrder(evs), "agent_prompted:stage hold_taken hold_released agent_prompted:handback stage_completed"; got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
	if rel, _ := findPayload[*event.HoldReleased](evs); rel == nil || rel.Turns != 0 {
		t.Errorf("hold account = %+v", rel)
	}
}

// A release right after a forced take-over hands back as soon as the stopped
// turn ends: nothing waits on the agent's own work.
func TestReleaseRightAfterAForcedTakeIsPrompt(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	r := &heldRunner{firstEnd: make(chan struct{}), stoppable: true} // the gate never opens
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		r.turn(call.In, "working on it", r.firstEnd)
		return r.drive(ctx, call.In)
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.AgentSession](t, h, runID) // the turn is open
	takeOverWhenLive(t, h, runID, "u1", true)
	if err := h.e.Release(runID, "u1"); err != nil {
		t.Fatal(err)
	}
	waitDone(t, h, runID)
	evs := h.events(t, runID)
	if got, want := holdOrder(evs), "agent_prompted:stage hold_taken hold_released agent_prompted:handback stage_completed"; got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
	if got := r.stoppedTurns(); len(got) != 1 || got[0] != "T1" {
		t.Errorf("stopped = %v", got)
	}
	if batches := r.delivered(); len(batches) != 1 || batches[0][0].Text != handbackPrompt {
		t.Errorf("delivered = %+v", batches)
	}
}

// A turn waiting on the agent's own question is not working: even a forced
// take-over leaves it for the holder to answer, and it runs on under the hold.
func TestAForcedTakeOverLeavesATurnWaitingOnItsQuestion(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	inbox := make(chan agentport.Inbox, 1)
	h.runner.RunFunc = askingRunner(h, runIDCh, inbox)
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID
	in := <-inbox
	p := h.waitPing(t, "clarification")
	takeOverWhenLive(t, h, runID, "u1", true)
	waitEvent[*event.HoldTaken](t, h, runID)
	time.Sleep(20 * time.Millisecond)
	if in.Interrupted("Q1") {
		t.Fatal("a forced take-over stopped a turn waiting on its question")
	}
	if err := h.e.AnswerPing(p.ID, textAnswer("postgres")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the turn to run on", func() bool { return agentSaid(h.events(t, runID), "with the answer") })
	time.Sleep(20 * time.Millisecond) // the turn ended under the hold; nothing completes
	if v := h.view(t, runID); v.State != run.Running || v.Hold == nil {
		t.Fatalf("after the answer = %s hold=%+v", v.State, v.Hold)
	}
	if err := h.e.Release(runID, "u1"); err != nil {
		t.Fatal(err)
	}
	waitDone(t, h, runID)
	evs := h.events(t, runID)
	if _, ok := findPayload[*event.StagePaused](evs); ok {
		t.Error("the question parked the run")
	}
	if v := h.view(t, runID); v.State != run.Completed || v.Hold != nil {
		t.Errorf("final view = %s hold=%+v", v.State, v.Hold)
	}
}

// askingRunner scripts an agent whose turn Q1 asks a question and waits on
// it, as the ask_human call does, then runs on with the answer and serves
// the inbox as the ACP runner does.
func askingRunner(h *harness, runIDCh <-chan string, inbox chan<- agentport.Inbox) func(context.Context, fake.RunCall) (agentport.AgentResult, error) {
	r := &heldRunner{}
	return func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		inbox <- call.In.Inbox
		emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "Q1"},
			agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s"})
		p, err := h.e.RaisePing(<-runIDCh, "clarification", "which db?", "", nil, nil)
		if err != nil {
			return agentport.AgentResult{}, err
		}
		if _, ok := p.Await(ctx); !ok {
			return agentport.AgentResult{}, ctx.Err()
		}
		emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvMessage, Turn: "Q1", Text: "with the answer"},
			agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "Q1", StopReason: "end_turn"})
		return r.drive(ctx, call.In)
	}
}

// A question the agent has raised counts as open while its raise still waits
// for the run's loop: a forced take-over arriving beside it leaves the turn
// alone. The loop is held busy writing the session while the agent raises
// and the take-over comes in, so both are queued when it comes back; it may
// pick either first. Several runs, as the pick is random.
func TestAForcedTakeOverBesideAQueuedQuestionLeavesTheTurn(t *testing.T) {
	for i := range 16 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, store := stallAt(event.TypeAgentSession)
			h := newHarnessOn(t, store, mustFlow(t, askFlowDoc))
			letGo := sync.OnceFunc(func() { close(s.letGo) })
			t.Cleanup(letGo)
			runIDCh := make(chan string, 1)
			inbox := make(chan agentport.Inbox, 1)
			h.runner.RunFunc = askingRunner(h, runIDCh, inbox)
			runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
			if err != nil {
				t.Fatal(err)
			}
			in := <-inbox
			select {
			case <-s.reached:
			case <-time.After(5 * time.Second):
				t.Fatal("the loop never wrote the session")
			}
			runIDCh <- runID // the agent raises now, with the loop busy
			h.e.mu.Lock()
			hd := h.e.handles[runID]
			h.e.mu.Unlock()
			waitUntil(t, "the raise to be queued", func() bool { return len(hd.mailbox) == 1 })
			taken := make(chan error, 1)
			go func() { taken <- h.e.TakeOver(runID, "u1", true) }()
			time.Sleep(20 * time.Millisecond) // the take-over waits on the loop too
			letGo()
			if err := <-taken; err != nil {
				t.Fatalf("take over: %v", err)
			}
			if in.Interrupted("Q1") {
				t.Fatal("a forced take-over stopped a turn whose question was queued")
			}
			p := h.waitPing(t, "clarification")
			if err := h.e.AnswerPing(p.ID, textAnswer("postgres")); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, "the turn to run on", func() bool { return agentSaid(h.events(t, runID), "with the answer") })
			if err := h.e.Release(runID, "u1"); err != nil {
				t.Fatal(err)
			}
			waitDone(t, h, runID)
			if _, ok := findPayload[*event.StagePaused](h.events(t, runID)); ok {
				t.Error("the question parked the run")
			}
		})
	}
}

// A question counts as open from the moment it is registered, before its
// raise reaches the run's loop: a forced take-over planned in between leaves
// the turn alone, and the question opens under the hold.
func TestAForcedTakeOverBesideARegisteredQuestionLeavesTheTurn(t *testing.T) {
	registered, letGo := make(chan struct{}), make(chan struct{})
	prev := raiseRegistered
	raiseRegistered = func(*Ping) { close(registered); <-letGo }
	t.Cleanup(func() { raiseRegistered = prev })
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	inbox := make(chan agentport.Inbox, 1)
	h.runner.RunFunc = askingRunner(h, runIDCh, inbox)
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	in := <-inbox
	runIDCh <- runID
	select {
	case <-registered:
	case <-time.After(5 * time.Second):
		t.Fatal("the question was never registered")
	}
	waitEvent[*event.AgentSession](t, h, runID) // the turn is open
	takeOverWhenLive(t, h, runID, "u1", true)   // planned with the raise short of the loop
	if in.Interrupted("Q1") {
		t.Fatal("a forced take-over stopped a turn whose question was registered")
	}
	close(letGo)
	p := h.waitPing(t, "clarification")
	if err := h.e.AnswerPing(p.ID, textAnswer("postgres")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the turn to run on", func() bool { return agentSaid(h.events(t, runID), "with the answer") })
	if err := h.e.Release(runID, "u1"); err != nil {
		t.Fatal(err)
	}
	waitDone(t, h, runID)
	evs := h.events(t, runID)
	if _, ok := findPayload[*event.StagePaused](evs); ok {
		t.Error("the question parked the run")
	}
	if v := h.view(t, runID); v.State != run.Completed || v.Hold != nil {
		t.Errorf("final view = %s hold=%+v", v.State, v.Hold)
	}
}

// Once a forced take-over has decided to stop the agent's turn, a question
// that turn raises is refused rather than opened on a turn about to end
// (which would park the run); the agent waits on the holder as after any
// stop.
func TestAQuestionFromAStoppingTurnIsRefused(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	asked := make(chan error, 1)
	r := &heldRunner{}
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "Q1"},
			agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s"})
		for !call.In.Inbox.Interrupted("Q1") {
			select {
			case <-call.In.Inbox.Wake():
			case <-ctx.Done():
				return agentport.AgentResult{}, ctx.Err()
			}
		}
		// The agent asks before its harness sees the stop.
		_, err := h.e.RaisePing(<-runIDCh, "clarification", "which db?", "", nil, nil)
		asked <- err
		emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "Q1", StopReason: "cancelled"})
		return r.drive(ctx, call.In)
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID
	waitEvent[*event.AgentSession](t, h, runID) // the turn is open
	takeOverWhenLive(t, h, runID, "u1", true)
	if err := <-asked; !errors.Is(err, ErrTurnStopping) {
		t.Fatalf("raise from the stopping turn = %v", err)
	}
	if _, err := h.e.SendMessage(runID, "use sqlite"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the message to be answered", func() bool { return agentSaid(h.events(t, runID), "answer: use sqlite") })
	if err := h.e.Release(runID, "u1"); err != nil {
		t.Fatal(err)
	}
	waitDone(t, h, runID)
	evs := h.events(t, runID)
	if _, ok := findPayload[*event.PingRaised](evs); ok {
		t.Error("the stopping turn's question was opened")
	}
	if _, ok := findPayload[*event.StagePaused](evs); ok {
		t.Error("the run parked")
	}
	if v := h.view(t, runID); v.State != run.Completed || v.Hold != nil {
		t.Errorf("final view = %s hold=%+v", v.State, v.Hold)
	}
}
