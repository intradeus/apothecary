package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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
			if in.Inbox.StopTurn(id) {
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
	if in.StopTurn("T1") || len(r.stoppedTurns()) != 0 {
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

// A release that lands behind a holder's message hands back only after that
// message's own turn, even when the runner takes the message only once the
// engine has seen the stopped turn end: the agent gets the holder's words and
// the handback as two prompts, and the hold accounts for the message and its
// turn.
func TestAReleaseBehindAQueuedMessageWaitsForItsTurn(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	r := &heldRunner{turns: 1} // the stage's own turn is T1; the holder's start at T2
	stopped, endTurn, take := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		in := call.In
		in.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "T1"})
		in.Events(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s"})
		for !in.Inbox.StopTurn("T1") {
			<-in.Inbox.Wake()
		}
		close(stopped)
		<-endTurn
		in.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "T1", StopReason: "cancelled",
			Usage: &agentport.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: turnCost}})
		// Read behind the turn's end: once on the record, the engine has
		// handled that end, and the runner has not taken the queue yet.
		in.Events(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s2"})
		<-take
		return r.drive(ctx, in)
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.AgentSession](t, h, runID) // the turn is open
	takeOverWhenLive(t, h, runID, "u1", true)
	<-stopped
	if _, err := h.e.SendMessage(runID, "use sqlite"); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Release(runID, "u1"); err != nil {
		t.Fatal(err)
	}
	close(endTurn)
	waitUntil(t, "the stopped turn's end to be handled", func() bool {
		return logHas(h.events(t, runID), func(s *event.AgentSession) bool { return s.SessionID == "s2" })
	})
	if _, ok := findPayload[*event.HoldReleased](h.events(t, runID)); ok {
		t.Fatal("handed back ahead of the holder's queued message")
	}
	close(take)
	waitDone(t, h, runID)

	evs := h.events(t, runID)
	want := "agent_prompted:stage hold_taken message_delivered hold_released agent_prompted:handback stage_completed"
	if got := holdOrder(evs); got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
	batches := r.delivered()
	if len(batches) != 2 || len(batches[0]) != 1 || batches[0][0].Text != "use sqlite" ||
		len(batches[1]) != 1 || !batches[1][0].Verbatim || batches[1][0].Text != handbackPrompt {
		t.Errorf("delivered = %+v, want the holder's message, then the handback alone", batches)
	}
	rel, _ := findPayload[*event.HoldReleased](evs)
	if rel == nil || rel.Messages != 1 || rel.Turns != 1 || rel.Usage == nil ||
		rel.Usage.CostUSD < turnCost-1e-9 || rel.Usage.CostUSD > turnCost+1e-9 {
		t.Errorf("hold account = %+v", rel)
	}
}

// A release that lands after the holder's message reached the agent but
// before the turn it prompts has started still waits for that turn: the
// holder's work is still to run, and its turn is on the hold's account.
func TestAReleaseBetweenDeliveryAndTurnStartWaitsForTheTurn(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	r := &heldRunner{turns: 1} // the stage's own turn is T1; the holder's starts at T2
	stopped, startTurn := make(chan struct{}), make(chan struct{})
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		in := call.In
		in.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "T1"})
		in.Events(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s"})
		for !in.Inbox.StopTurn("T1") {
			<-in.Inbox.Wake()
		}
		in.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "T1", StopReason: "cancelled",
			Usage: &agentport.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: turnCost}})
		close(stopped)
		msgs := in.Inbox.Take()
		for len(msgs) == 0 {
			select {
			case <-in.Inbox.Wake():
			case <-ctx.Done():
				return agentport.AgentResult{Session: "s"}, ctx.Err()
			}
			msgs = in.Inbox.Take()
		}
		in.Events(agentport.AgentEvent{Kind: agentport.AgentEvUserMessage, Messages: msgs})
		// Read behind the delivery: once on the record, the engine has
		// handled it, and the turn it prompts has not started.
		in.Events(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s2"})
		<-startTurn
		r.turn(in, "answer: "+msgs[0].Text, nil)
		return r.drive(ctx, in)
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.AgentSession](t, h, runID) // the turn is open
	takeOverWhenLive(t, h, runID, "u1", true)
	<-stopped
	if _, err := h.e.SendMessage(runID, "use sqlite"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the delivery to be handled", func() bool {
		return logHas(h.events(t, runID), func(s *event.AgentSession) bool { return s.SessionID == "s2" })
	})
	if err := h.e.Release(runID, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := findPayload[*event.HoldReleased](h.events(t, runID)); ok {
		t.Fatal("handed back ahead of the turn the holder's delivered message prompts")
	}
	close(startTurn)
	waitDone(t, h, runID)

	evs := h.events(t, runID)
	want := "agent_prompted:stage hold_taken message_delivered hold_released agent_prompted:handback stage_completed"
	if got := holdOrder(evs); got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
	if !agentSaid(evs, "answer: use sqlite") {
		t.Error("the holder's turn did not run")
	}
	batches := r.delivered()
	if len(batches) != 1 || len(batches[0]) != 1 || !batches[0][0].Verbatim || batches[0][0].Text != handbackPrompt {
		t.Errorf("delivered after the holder's turn = %+v, want the handback alone", batches)
	}
	rel, _ := findPayload[*event.HoldReleased](evs)
	if rel == nil || rel.Messages != 1 || rel.Turns != 1 || rel.Usage == nil ||
		rel.Usage.CostUSD < turnCost-1e-9 || rel.Usage.CostUSD > turnCost+1e-9 {
		t.Errorf("hold account = %+v", rel)
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
	if in.StopTurn("Q1") {
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

// A release asked for while the agent's own question holds its turn open is
// not lost when that turn parks on the question: the hold closes as the run
// parks, and the handback follows the answer's turn in the same session —
// no second release. Whether the turn ends on the question or waits on it
// past the live hold.
func TestAReleaseBeforeAQuestionParksHandsBackAfterTheAnswer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ended bool // Q1 ends on its question; otherwise the live hold runs out
	}{{"turn ends on the question", true}, {"question outwaits the live hold", false}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, mustFlow(t, askFlowDoc))
			if !tc.ended {
				h.e.cfg.AskHumanHold = 50 * time.Millisecond
			}
			runIDCh := make(chan string, 1)
			endQ1 := make(chan struct{})
			h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
				emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "Q1"},
					agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s"})
				if _, err := h.e.RaisePing(<-runIDCh, "clarification", "which db?", "", nil, nil); err != nil {
					return agentport.AgentResult{}, err
				}
				if tc.ended {
					<-endQ1
					call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "Q1", StopReason: "end_turn"})
				}
				<-ctx.Done()
				return agentport.AgentResult{Session: "s"}, ctx.Err()
			}
			r := &heldRunner{}
			var resumes atomic.Int32
			h.runner.ResumeFunc = func(ctx context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
				resumes.Add(1)
				// Waiting for the handback from its first instruction: a
				// session let go of before it is queued would end the stage
				// without it.
				if !call.In.Inbox.Held() {
					t.Error("the answer's session was let go of before its handback")
				}
				r.turn(call.In, "with the answer", nil)
				return r.drive(ctx, call.In)
			}
			runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
			if err != nil {
				t.Fatal(err)
			}
			runIDCh <- runID
			p := h.waitPing(t, "clarification")
			takeOverWhenLive(t, h, runID, "u1", true)
			if err := h.e.Release(runID, "u1"); err != nil {
				t.Fatalf("release while the question is open: %v", err)
			}
			close(endQ1)
			waitUntil(t, "the run to park on its question", func() bool { return h.view(t, runID).Dormant })
			if v := h.view(t, runID); v.Hold != nil || v.State != run.WaitingOnHuman {
				t.Fatalf("parked = %s, hold %+v", v.State, v.Hold)
			}

			if err := h.e.AnswerPing(p.ID, textAnswer("postgres")); err != nil {
				t.Fatal(err)
			}
			waitDone(t, h, runID)
			evs := h.events(t, runID)
			if v := h.view(t, runID); v.State != run.Completed || v.Hold != nil {
				t.Errorf("final view = %s hold=%+v", v.State, v.Hold)
			}
			want := "agent_prompted:" + event.PromptStage + " hold_taken hold_released agent_prompted:" +
				event.PromptAnswer + " agent_prompted:" + event.PromptHandback + " stage_completed"
			if got := holdOrder(evs); got != want {
				t.Errorf("order = %s\nwant    %s", got, want)
			}
			rel, _ := findPayload[*event.HoldReleased](evs)
			if rel == nil || rel.Reason != event.HoldReleasedByUser || rel.UserID != "u1" || rel.Turns != 0 || rel.Messages != 0 {
				t.Errorf("hold account = %+v", rel)
			}
			if n := resumes.Load(); n != 1 {
				t.Errorf("resumed %d times, want once", n)
			}
			if b := r.delivered(); len(b) != 1 || len(b[0]) != 1 || b[0][0].Text != handbackPrompt {
				t.Errorf("delivered after the answer = %+v", b)
			}
		})
	}
}

// A release that comes once a question of the agent's has parked a held run
// counts the holder's message still queued behind that question: it rides
// into the answer's prompt, ahead of the handback that follows that turn.
func TestReleasingAQuestionParkedHoldCountsItsQueuedMessage(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	endQ1 := make(chan struct{})
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "Q1"},
			agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s"})
		if _, err := h.e.RaisePing(<-runIDCh, "clarification", "which db?", "", nil, nil); err != nil {
			return agentport.AgentResult{}, err
		}
		<-endQ1
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "Q1", StopReason: "end_turn"})
		<-ctx.Done()
		return agentport.AgentResult{Session: "s"}, ctx.Err()
	}
	r := &heldRunner{}
	answerPrompt := make(chan string, 1)
	h.runner.ResumeFunc = func(ctx context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
		answerPrompt <- call.Reply
		r.turn(call.In, "with the answer", nil)
		return r.drive(ctx, call.In)
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID
	p := h.waitPing(t, "clarification")
	takeOverWhenLive(t, h, runID, "u1", true)
	if _, err := h.e.SendMessage(runID, "use the test database"); err != nil {
		t.Fatal(err)
	}
	close(endQ1)
	waitUntil(t, "the run to park on its question", func() bool { return h.view(t, runID).Dormant })
	if v := h.view(t, runID); v.Hold == nil || v.State != run.WaitingOnHuman {
		t.Fatalf("parked = %s, hold %+v", v.State, v.Hold)
	}
	if err := h.e.Release(runID, "u1"); err != nil {
		t.Fatalf("release of the parked hold: %v", err)
	}
	rel, _ := findPayload[*event.HoldReleased](h.events(t, runID))
	if rel == nil || rel.Reason != event.HoldReleasedByUser || rel.Messages != 1 || rel.Turns != 0 {
		t.Fatalf("hold account = %+v", rel)
	}

	if err := h.e.AnswerPing(p.ID, textAnswer("postgres")); err != nil {
		t.Fatal(err)
	}
	waitDone(t, h, runID)
	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Completed || v.Hold != nil {
		t.Errorf("final view = %s hold=%+v", v.State, v.Hold)
	}
	if got := <-answerPrompt; !strings.Contains(got, "use the test database") {
		t.Errorf("the answer's prompt = %q", got)
	}
	want := "agent_prompted:" + event.PromptStage + " hold_taken hold_released message_delivered agent_prompted:" +
		event.PromptAnswer + " agent_prompted:" + event.PromptHandback + " stage_completed"
	if got := holdOrder(evs); got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
	n := 0
	for _, ev := range evs {
		if _, ok := ev.Payload.(*event.HoldReleased); ok {
			n++
		}
	}
	if n != 1 {
		t.Errorf("released %d times", n)
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
			if in.StopTurn("Q1") {
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
	if in.StopTurn("Q1") {
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

// A forced take-over planned while the agent's question is registered and
// its turn's start has not reached the run's loop finds no turn to stop and
// the holder idle. The question then opens and the turn's start arrives: the
// agent still waits on a person, so the stage clock stays stopped and the
// question's live hold runs out into a park, not a timeout.
func TestALateTurnStartKeepsTheClockStoppedOnAnOpenQuestion(t *testing.T) {
	registered, letGo := make(chan struct{}), make(chan struct{})
	prev := raiseRegistered
	raiseRegistered = func(*Ping) { close(registered); <-letGo }
	t.Cleanup(func() { raiseRegistered = prev })
	doc := strings.Replace(askFlowDoc, "agent: builder,", "agent: builder, timeout: 300ms,", 1)
	h := newHarness(t, mustFlow(t, doc))
	h.e.cfg.AskHumanHold = 900 * time.Millisecond
	runIDCh := make(chan string, 1)
	inbox := make(chan agentport.Inbox, 1)
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		inbox <- call.In.Inbox
		if _, err := h.e.RaisePing(<-runIDCh, "clarification", "which db?", "", nil, nil); err != nil {
			return agentport.AgentResult{}, err
		}
		// The turn's start, behind its question; the session is logged
		// behind it, so the test sees the loop read it.
		emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "Q1"},
			agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s"})
		<-ctx.Done()
		return agentport.AgentResult{Session: "s"}, ctx.Err()
	}
	r := &heldRunner{}
	h.runner.ResumeFunc = func(ctx context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
		r.turn(call.In, "with the answer", nil)
		return r.drive(ctx, call.In)
	}
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
	takeOverWhenLive(t, h, runID, "u1", true) // no turn open yet: the holder is idle
	if in.StopTurn("Q1") {
		t.Fatal("a forced take-over stopped a turn whose question was registered")
	}
	close(letGo)
	p := h.waitPing(t, "clarification")
	waitEvent[*event.AgentSession](t, h, runID) // the loop has read the turn's start
	waitUntil(t, "the run to park or fail", func() bool {
		_, failed := findPayload[*event.StageFailed](h.events(t, runID))
		return failed || h.view(t, runID).Dormant
	})
	if f, ok := findPayload[*event.StageFailed](h.events(t, runID)); ok {
		t.Fatalf("the stage failed while its question was open: %+v", f)
	}
	if v := h.view(t, runID); v.State != run.WaitingOnHuman || v.Hold == nil {
		t.Fatalf("parked = %s, hold %+v", v.State, v.Hold)
	}
	h.e.cfg.AskHumanHold = time.Hour

	if err := h.e.AnswerPing(p.ID, textAnswer("postgres")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the answer's turn", func() bool { return agentSaid(h.events(t, runID), "with the answer") })
	if err := h.e.Release(runID, "u1"); err != nil {
		t.Fatal(err)
	}
	waitDone(t, h, runID)
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
		for !call.In.Inbox.StopTurn("Q1") {
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

// A stop ends with the stopped turn, not with the loop catching up on it: the
// holder's next turn may ask while the loop is still writing the stopped
// turn's transcript, and its question is opened, not refused — nor taken for
// the stopped turn's, which would park the run as the loop reads that turn's
// end. Once let go, the loop may read the question or the turn's end first;
// several runs, as the pick is random.
func TestTheHoldersTurnMayAskWhileTheLoopIsBehindTheStop(t *testing.T) {
	for i := range 16 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, store := stallAt(event.TypeAgentNotice)
			h := newHarnessOn(t, store, mustFlow(t, askFlowDoc))
			letGo := sync.OnceFunc(func() { close(s.letGo) })
			t.Cleanup(letGo)
			runIDCh := make(chan string, 1)
			asked := make(chan error, 1)
			r := &heldRunner{}
			h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
				runID := <-runIDCh
				emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "Q1"},
					agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s"})
				for !call.In.Inbox.StopTurn("Q1") {
					select {
					case <-call.In.Inbox.Wake():
					case <-ctx.Done():
						return agentport.AgentResult{}, ctx.Err()
					}
				}
				var msgs []agentport.UserMessage
				for msgs = call.In.Inbox.Take(); len(msgs) == 0; msgs = call.In.Inbox.Take() {
					select {
					case <-call.In.Inbox.Wake():
					case <-ctx.Done():
						return agentport.AgentResult{}, ctx.Err()
					}
				}
				// The loop stalls on the notice; the stopped turn ends behind
				// it and the holder's turn starts and asks, all before the
				// loop reads them. Only once it is stalled: a loop free to
				// pick would take the question before the notice.
				emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvNotice, Text: "stopping"})
				select {
				case <-s.reached:
				case <-ctx.Done():
					return agentport.AgentResult{}, ctx.Err()
				}
				emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "Q1", StopReason: "cancelled"},
					agentport.AgentEvent{Kind: agentport.AgentEvUserMessage, Messages: msgs},
					agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "Q2"})
				p, err := h.e.RaisePing(runID, "clarification", "which db?", "", nil, nil)
				asked <- err
				if err != nil {
					return agentport.AgentResult{}, err
				}
				if _, ok := p.Await(ctx); !ok {
					return agentport.AgentResult{}, ctx.Err()
				}
				emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvMessage, Turn: "Q2", Text: "with the answer"},
					agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "Q2", StopReason: "end_turn"})
				return r.drive(ctx, call.In)
			}
			runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
			if err != nil {
				t.Fatal(err)
			}
			runIDCh <- runID
			waitEvent[*event.AgentSession](t, h, runID) // the turn is open
			takeOverWhenLive(t, h, runID, "u1", true)
			if _, err := h.e.SendMessage(runID, "use sqlite"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-s.reached:
			case <-time.After(5 * time.Second):
				t.Fatal("the loop never wrote the notice")
			}
			h.e.mu.Lock()
			hd := h.e.handles[runID]
			h.e.mu.Unlock()
			// Refused at once, or registered and queued for the stalled loop.
			waitUntil(t, "the holder's turn to ask", func() bool { return len(asked) > 0 || len(hd.mailbox) == 1 })
			if len(asked) > 0 {
				t.Fatalf("raise from the holder's turn = %v", <-asked)
			}
			letGo()
			if err := <-asked; err != nil {
				t.Fatalf("raise from the holder's turn = %v", err)
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
			if v := h.view(t, runID); v.State != run.Completed || v.Hold != nil {
				t.Errorf("final view = %s hold=%+v", v.State, v.Hold)
			}
		})
	}
}

// The holder's turn may ask before its runner has reported that it started
// (an ACP sidecar records turn_start ahead of the prompt, but the runner
// forwards it after the agent may have run tools): a question raised after
// the stopped turn's end was reported, with no turn reported open, is not
// the stopped turn's, and the loop reading that end late leaves it open.
// Once let go, the loop may read the question or the turn's end first;
// several runs, as the pick is random.
func TestTheHoldersTurnMayAskBeforeItsStartIsReported(t *testing.T) {
	for i := range 16 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			registered := make(chan struct{})
			prev := raiseRegistered
			raiseRegistered = func(*Ping) { close(registered) }
			t.Cleanup(func() { raiseRegistered = prev })
			s, store := stallAt(event.TypeAgentNotice)
			h := newHarnessOn(t, store, mustFlow(t, askFlowDoc))
			letGo := sync.OnceFunc(func() { close(s.letGo) })
			t.Cleanup(letGo)
			runIDCh := make(chan string, 1)
			asked := make(chan error, 1)
			started := make(chan struct{})
			r := &heldRunner{}
			h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
				runID := <-runIDCh
				emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "Q1"},
					agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s"})
				for !call.In.Inbox.StopTurn("Q1") {
					select {
					case <-call.In.Inbox.Wake():
					case <-ctx.Done():
						return agentport.AgentResult{}, ctx.Err()
					}
				}
				var msgs []agentport.UserMessage
				for msgs = call.In.Inbox.Take(); len(msgs) == 0; msgs = call.In.Inbox.Take() {
					select {
					case <-call.In.Inbox.Wake():
					case <-ctx.Done():
						return agentport.AgentResult{}, ctx.Err()
					}
				}
				// The loop stalls on the notice; the stopped turn's end is
				// reported behind it, then the holder's turn asks, and only
				// then is its start reported. Only once it is stalled: a loop
				// free to pick would take the question before the notice.
				emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvNotice, Text: "stopping"})
				select {
				case <-s.reached:
				case <-ctx.Done():
					return agentport.AgentResult{}, ctx.Err()
				}
				emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "Q1", StopReason: "cancelled"},
					agentport.AgentEvent{Kind: agentport.AgentEvUserMessage, Messages: msgs})
				raised := make(chan *Ping, 1)
				go func() {
					p, err := h.e.RaisePing(runID, "clarification", "which db?", "", nil, nil)
					asked <- err
					raised <- p
				}()
				select {
				case <-registered:
				case <-ctx.Done():
					return agentport.AgentResult{}, ctx.Err()
				}
				emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "Q2"})
				close(started)
				p := <-raised
				if p == nil {
					return agentport.AgentResult{}, errors.New("the holder's question was refused")
				}
				if _, ok := p.Await(ctx); !ok {
					return agentport.AgentResult{}, ctx.Err()
				}
				emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvMessage, Turn: "Q2", Text: "with the answer"},
					agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "Q2", StopReason: "end_turn"})
				return r.drive(ctx, call.In)
			}
			runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
			if err != nil {
				t.Fatal(err)
			}
			runIDCh <- runID
			waitEvent[*event.AgentSession](t, h, runID) // the turn is open
			takeOverWhenLive(t, h, runID, "u1", true)
			if _, err := h.e.SendMessage(runID, "use sqlite"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-s.reached:
			case <-time.After(5 * time.Second):
				t.Fatal("the loop never wrote the notice")
			}
			select {
			case <-started:
			case err := <-asked:
				t.Fatalf("raise from the holder's turn = %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("the holder's turn never asked")
			}
			h.e.mu.Lock()
			hd := h.e.handles[runID]
			h.e.mu.Unlock()
			// Queued for the stalled loop, beside the stopped turn's end.
			waitUntil(t, "the question to reach the loop's mailbox", func() bool { return len(asked) > 0 || len(hd.mailbox) == 1 })
			if len(asked) > 0 {
				t.Fatalf("raise from the holder's turn = %v", <-asked)
			}
			letGo()
			if err := <-asked; err != nil {
				t.Fatalf("raise from the holder's turn = %v", err)
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
				t.Error("the stopped turn's end parked the run on the holder's question")
			}
			if v := h.view(t, runID); v.State != run.Completed || v.Hold != nil {
				t.Errorf("final view = %s hold=%+v", v.State, v.Hold)
			}
		})
	}
}

// A runner that cannot stop a turn never takes a forced take-over's stop up:
// its turn runs on as under a passive take-over, and the questions it raises
// meanwhile are still opened, answered and carried on from under the hold.
func TestARunnerThatCannotStopKeepsItsQuestions(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	r := &heldRunner{}
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "Q1"},
			agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s"})
		// Asks once the take-over is in force, never looking at the stop.
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
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.AgentSession](t, h, runID) // the turn is open
	takeOverWhenLive(t, h, runID, "u1", true)
	runIDCh <- runID
	p := h.waitPing(t, "clarification")
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
	if _, ok := findPayload[*event.StagePaused](h.events(t, runID)); ok {
		t.Error("the question parked the run")
	}
	if v := h.view(t, runID); v.State != run.Completed || v.Hold != nil {
		t.Errorf("final view = %s hold=%+v", v.State, v.Hold)
	}
}

// A question the turn raises after a forced take-over asked to stop it, but
// before its runner took the stop up, keeps the turn: the runner is then told
// there is nothing to stop, and the question is answered under the hold.
func TestAQuestionBeforeTheStopIsTakenUpKeepsTheTurn(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	took := make(chan bool, 1)
	r := &heldRunner{}
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "Q1"},
			agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s"})
		select {
		case <-call.In.Inbox.Wake(): // the stop is asked for
		case <-ctx.Done():
			return agentport.AgentResult{}, ctx.Err()
		}
		// The agent asks before its runner takes the stop up.
		p, err := h.e.RaisePing(<-runIDCh, "clarification", "which db?", "", nil, nil)
		if err != nil {
			return agentport.AgentResult{}, err
		}
		took <- call.In.Inbox.StopTurn("Q1")
		if _, ok := p.Await(ctx); !ok {
			return agentport.AgentResult{}, ctx.Err()
		}
		emitAll(call.In, agentport.AgentEvent{Kind: agentport.AgentEvMessage, Turn: "Q1", Text: "with the answer"},
			agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "Q1", StopReason: "end_turn"})
		return r.drive(ctx, call.In)
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.AgentSession](t, h, runID) // the turn is open
	takeOverWhenLive(t, h, runID, "u1", true)
	runIDCh <- runID
	if <-took {
		t.Fatal("the runner took up a stop its turn's question had withdrawn")
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
	if v := h.view(t, runID); v.State != run.Completed || v.Hold != nil {
		t.Errorf("final view = %s hold=%+v", v.State, v.Hold)
	}
}
