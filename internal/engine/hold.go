package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
)

// A TAKE-OVER: a human holds the running agent's session, the only way to
// talk to it (a read-only question goes to the Ask pane instead). While a
// hold is in force, a turn ending with nothing queued is not the stage's end.
// The agent waits for the holder's next message (SendMessage), each message
// is a turn in the same session, and the stage can only end once the holder
// releases — the session is then sent the handback prompt, and when THAT turn
// ends the stage's output contract applies as usual.
//
// The hold is on the record: hold_taken, and hold_released with its
// duration, turns and cost (every leg of it, across any park). A holder who
// goes quiet past the live hold parks the run (stage_paused held) the way
// an unanswered question does; their next message, or the release, wakes
// it.

var (
	// ErrAlreadyHeld: someone already holds this stage's session.
	ErrAlreadyHeld = errors.New("someone already holds this agent's session")
	// ErrNotHeld: a release or a message with no take-over in force.
	ErrNotHeld = errors.New("nobody holds this agent's session")
)

// TakeOver puts the run's live agent session in userID's hands. 409-class
// errors: no agent is running to take over (a command stage, a gate, between
// stages, parked), or someone already holds it.
func (e *Engine) TakeOver(runID, userID string) error {
	if h := e.liveLoop(runID); h != nil {
		if !h.agentLive.Load() {
			return ErrNoAgentRunning
		}
		if err := e.holdLive(h, holdReq{take: true, userID: userID}); !errors.Is(err, errNoLoop) {
			return err
		}
	}
	v, err := e.dormantView(context.Background(), runID)
	if err != nil {
		return err
	}
	if v.Hold != nil {
		return ErrAlreadyHeld
	}
	return ErrNoAgentRunning
}

// Release hands the task back: the hold is closed with its account and the
// session is sent the handback prompt — at once when the agent is idle,
// when its turn in flight ends otherwise. A run parked on its holder is
// woken for it.
func (e *Engine) Release(runID, userID string) error {
	if h := e.liveLoop(runID); h != nil {
		if !h.agentLive.Load() {
			return ErrNotHeld
		}
		if err := e.holdLive(h, holdReq{userID: userID}); !errors.Is(err, errNoLoop) {
			return err
		}
	}
	return e.wakeHeld(context.Background(), runID, func(v run.View) ([]event.Payload, error) {
		rel := &event.HoldReleased{StageID: v.Hold.StageID, Attempt: v.Hold.Attempt, UserID: userID,
			Reason: event.HoldReleasedByUser, DurationMS: max(e.clock().Sub(v.Hold.Since).Milliseconds(), 0),
			HoldSpend: event.HoldSpend{Turns: v.Hold.Turns, Messages: v.Hold.Messages}}
		if v.Hold.CostUSD > 0 {
			rel.Usage = &event.Usage{CostUSD: v.Hold.CostUSD}
		}
		return []event.Payload{rel}, nil
	})
}

// holdLive hands a take-over request to the run's loop and waits to hear
// what it did with it. errNoLoop: the loop went without taking it, and the
// run is parked or over on the log.
func (e *Engine) holdLive(h *runHandle, req holdReq) error {
	req.ack = make(chan error, 1)
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	select {
	case h.holds <- req:
	case <-h.done:
		return errNoLoop
	case <-t.C:
		// The invocation ended between the check and the send.
		return ErrNoAgentRunning
	}
	select {
	case err := <-req.ack:
		return err
	case <-h.done:
		// An ack already there wins: the loop can act on it and exit in the
		// same breath.
		select {
		case err := <-req.ack:
			return err
		default:
			return errNoLoop
		}
	}
}

// dormantView is the log's view of a run with no loop.
func (e *Engine) dormantView(ctx context.Context, runID string) (run.View, error) {
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return run.View{}, err
	}
	if len(evs) == 0 {
		return run.View{}, fmt.Errorf("run %s not found", runID)
	}
	return run.Reduce(evs), nil
}

// wakeHeld appends to a run parked on the holder of a take-over and gives
// it a loop again — the message or the release it was waiting for. Like
// answerDormant, it is the one path that writes to a dormant run's log from
// outside its goroutine, serialized by wakeMu; a loop that appeared in the
// meantime owns the log, and refusing beats writing behind it. build runs
// once the run is found waiting on its holder; its error appends nothing.
func (e *Engine) wakeHeld(ctx context.Context, runID string, build func(run.View) ([]event.Payload, error)) error {
	e.wakeMu.Lock()
	defer e.wakeMu.Unlock()

	e.mu.Lock()
	_, active := e.handles[runID]
	e.mu.Unlock()
	if active {
		return fmt.Errorf("run %s: %w", runID, errRunBusy)
	}
	v, err := e.dormantView(ctx, runID)
	if err != nil {
		return err
	}
	switch {
	case v.State.Terminal():
		return fmt.Errorf("run %s: %w", runID, ErrRunNotActive)
	case v.Hold == nil:
		return ErrNoAgentRunning
	case v.State == run.Paused:
		return fmt.Errorf("run %s is paused; play it first: %w", runID, ErrRunNotActive)
	case !v.Dormant || v.State != run.WaitingOnHuman:
		// Mid-admission, or parked on something else (a question of its
		// own): the loop that is owed, or the answer, is what moves it.
		return fmt.Errorf("run %s: %w", runID, errRunBusy)
	}
	payloads, err := build(v)
	if err != nil {
		return err
	}
	payloads = append(payloads, &event.RunUnpaused{Reason: event.UnpauseHold})
	if err := e.AppendEvents(ctx, runID, payloads...); err != nil {
		return err
	}
	lim := e.limits(ctx)
	e.mu.Lock()
	e.queue = append(e.queue, runID)
	e.scheduleLocked(lim)
	e.mu.Unlock()
	return nil
}

// liveHold is the engine's in-flight view of a take-over during one
// invocation: what the holder's messages spent so far (leg), whether the agent sits idle waiting on the holder (the stage clock is
// suspended then, as for a pending question), and a release waiting for the
// turn in flight to end. A start hold still owed its opening turn (on the
// log: hold_taken start, no hold_opened) opens on the agent's own turn —
// not the holder's, so off the hold's account — whether it is sent now or
// followed after a restart; its end is recorded, and the owner is told
// when it leaves the session waiting on them.
type liveHold struct {
	st        *runcore.HeldState
	leg       runcore.HoldLeg
	idle      bool
	releasing bool
	releaseBy string
	opening   bool
}

func (x *exec) liveHold(st *flow.Stage) *liveHold {
	h := x.rs.Holds[st.ID]
	if h == nil {
		return nil
	}
	l := &liveHold{st: h, leg: runcore.NewHoldLeg(), opening: h.Opening}
	l.leg.Spend.Messages, x.heldMessages = x.heldMessages, 0
	return l
}

// notifyHeld is the Type of the notification telling a run's owner that a
// stage which starts held (start_held) is waiting for them.
const notifyHeld = "held"

// holdReq is a take-over request on its way into the run loop: take, or
// release (the handback follows).
type holdReq struct {
	take   bool
	userID string
	ack    chan error
}

// takeHold records a take-over of the stage's live session.
func (x *exec) takeHold(st *flow.Stage, iter int, userID string) error {
	taken := &event.HoldTaken{StageID: st.ID, Attempt: iter, UserID: userID}
	return x.append(taken)
}

// takeStartHold opens an attempt of a stage that starts held (start_held)
// in the run owner's hands, before its stage prompt goes out. A hold in
// force already is this attempt's, taken before a crash: not taken twice.
func (x *exec) takeStartHold(st *flow.Stage, iter int) error {
	if !st.StartHeld || x.rs.AttemptBegun(st.ID) || x.rs.Holds[st.ID] != nil {
		return nil
	}
	return x.append(&event.HoldTaken{StageID: st.ID, Attempt: iter, UserID: x.rs.View.UserID, Start: true})
}

// admitOpening lets the run's owner reach an attempt of a stage that starts
// held before the invocation carrying its opening turn runs: from just
// before hold_taken is written (the run shows in their hands as soon as it
// is) until the stage prompt goes out. A message or a release then waits on
// the loop's channel instead of being refused. A message admitted before
// the stage prompt is on the log rides with it (promptOpening); a release,
// or a message sent once the prompt is being written, waits for the opening
// invocation, which takes it as it would mid-turn — the release follows the
// opening turn, the message is the owner's turn after it.
func (x *exec) admitOpening(st *flow.Stage) {
	if !st.StartHeld {
		return
	}
	if h := x.rs.Holds[st.ID]; h != nil && h.Opening || h == nil && !x.rs.AttemptBegun(st.ID) {
		x.h.agentLive.Store(true)
	}
}

// promptOpening fixes and records the prompt of the invocation about to
// start: text with the human messages nobody has delivered yet. While a
// start hold's opening turn is owed it does so under the run's opening lock
// (runHandle.opening), from taking what the owner sent while the prompt was
// on its way (takeOpeningMessages) until the prompt is on the log, and in
// one write: those messages' message_queued, the delivery of everything the
// prompt carries and its agent_prompted land together or not at all, so no
// message is ever delivered to a prompt the log does not have. Every message
// admitted before then rides with it exactly once; a send that comes
// meanwhile waits for the write, then reaches the opening invocation. A
// stage prompt sent again — a restart before its session was on record —
// carries again what the first one did (HeldState.OpeningMsgs).
func (x *exec) promptOpening(st *flow.Stage, iter int, kind, text string) (string, error) {
	h := x.rs.Holds[st.ID]
	if h == nil || !h.Opening {
		text, err := x.withPendingMessages(st, iter, text)
		if err != nil {
			return "", err
		}
		return text, x.prompted(st.ID, iter, kind, text)
	}
	x.h.opening.Lock()
	defer x.h.opening.Unlock()
	var msgs []agentport.UserMessage
	if kind == event.PromptStage {
		for _, m := range h.OpeningMsgs {
			msgs = append(msgs, agentport.UserMessage{ID: m.MessageID, Text: m.Text})
		}
	}
	sent := x.takeOpeningMessages()
	var batch []event.Payload
	var ids []string
	for _, m := range x.rs.PendingMsgs {
		msgs = append(msgs, agentport.UserMessage{ID: m.MessageID, Text: m.Text})
		ids = append(ids, m.MessageID)
	}
	for _, req := range sent {
		batch = append(batch, &event.MessageQueued{MessageID: req.msg.ID, Text: req.msg.Text, StageID: st.ID})
		msgs = append(msgs, req.msg)
		ids = append(ids, req.msg.ID)
	}
	if len(ids) > 0 {
		batch = append(batch, &event.MessageDelivered{MessageIDs: ids, StageID: st.ID, Attempt: iter})
	}
	if len(msgs) > 0 {
		text += "\n\n---\n\n" + formatMessages(msgs)
	}
	// The holder's words a prompt carries while the hold is opening are
	// banked on its account by the fold, as they land.
	err := x.append(append(batch, promptedEvent(st.ID, iter, kind, text))...)
	for _, req := range sent {
		req.ack <- err
	}
	if err != nil {
		return "", err
	}
	return text, nil
}

// takeOpeningMessages takes what the run's owner sent while a start hold's
// opening prompt was on its way (admitOpening), for that prompt to carry.
func (x *exec) takeOpeningMessages() []msgReq {
	var sent []msgReq
	for {
		select {
		case req := <-x.h.messages:
			sent = append(sent, req)
		default:
			return sent
		}
	}
}

// releaseHold closes a take-over with its account: every leg's spend, the
// time since it was taken, and why it ended. A release by the holder owes
// the session the handback prompt.
func (x *exec) releaseHold(st *flow.Stage, iter int, h *runcore.HeldState, by, reason string, leg event.HoldSpend) error {
	total := h.Banked
	total.Add(leg)
	if by == "" && h.Taken != nil {
		by = h.Taken.UserID
	}
	rel := &event.HoldReleased{StageID: st.ID, Attempt: iter, UserID: by, Reason: reason,
		DurationMS: max(x.e.clock().Sub(h.TakenAt).Milliseconds(), 0), HoldSpend: total}
	return x.append(rel)
}

// endHold closes a take-over the attempt ended underneath: the harness
// returned, died or timed out while someone held it.
func (x *exec) endHold(st *flow.Stage, iter int, leg event.HoldSpend) error {
	h := x.rs.Holds[st.ID]
	if h == nil {
		return nil
	}
	return x.releaseHold(st, iter, h, "", event.HoldEndedWithStage, leg)
}

// holdReentry is what a session under a take-over is sent when the loop
// (re-)enters its stage.
type holdReentry struct {
	reply, kind, reason string
	// park: nothing to send — the attempt parks on its holder.
	park bool
}

// holdReentry decides what a session under a take-over (or owed its
// handback) gets next: the handback its release owes, the holder's pending
// messages, or nothing yet — then the attempt parks on the holder, unless a
// turn from before a restart is still live to be followed (mayReattach).
func (x *exec) holdReentry(st *flow.Stage, iter int, mayReattach bool, runner agentport.AgentRunner, session agentport.SessionID) (holdReentry, error) {
	rs := x.rs
	switch {
	case rs.HandbackOwed[st.ID]:
		return holdReentry{reply: handbackPrompt, kind: event.PromptHandback, reason: event.ResumeReleased}, nil
	case rs.Holds[st.ID] == nil:
		return holdReentry{}, nil
	case rs.Holds[st.ID].Opening:
		// A start hold still owed its opening turn: the stage prompt not
		// sent yet, sent with no session recorded, or its turn in flight
		// at a restart. The attempt's own path sends it (or follows it,
		// or asks the session to carry on), and messages queued meanwhile
		// ride with that prompt.
		return holdReentry{}, nil
	case len(rs.PendingMsgs) > 0:
		n := len(rs.PendingMsgs)
		text, err := x.withPendingMessages(st, iter, "")
		if err != nil {
			return holdReentry{}, err
		}
		x.heldMessages += n // on the hold's account, in the leg they start
		return holdReentry{reply: strings.TrimPrefix(text, "\n\n---\n\n"), kind: event.PromptMessages, reason: event.ResumeMessage}, nil
	case rs.PausedHold[st.ID]:
		return holdReentry{park: true}, nil
	case mayReattach:
		if ra, ok := runner.(agentport.AgentReattacher); ok && ra.Reattachable(x.ctx, rs.EnvID, session, rs.AgentCursor[st.ID]) {
			return holdReentry{}, nil // the crashed path follows the live turn
		}
		return holdReentry{park: true}, nil
	default:
		return holdReentry{park: true}, nil
	}
}

// parkOnHold parks an attempt whose holder has nothing queued for it when
// no invocation is there to do the parking (a restart, a play, a loop
// re-entry). The hold stays in force.
func (x *exec) parkOnHold(st *flow.Stage, iter int, session agentport.SessionID) error {
	x.h.parking.Store(true)
	if !x.rs.PausedHold[st.ID] {
		if err := x.append(&event.StagePaused{StageID: st.ID, Attempt: iter, Reason: event.PauseHeld,
			SessionID: string(session)}); err != nil {
			return err
		}
	}
	return errWaitingOnHuman
}
