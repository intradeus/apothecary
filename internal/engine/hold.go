package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"apothecary/internal/event"
	"apothecary/internal/run"
)

// A TAKE-OVER: a human holds the running agent's session. The whisper
// (SendMessage) delivers one message into the live session and lets the
// agent carry on; the engine then reads the end of the agent's next turn
// as the end of the stage — which is right for a note and wrong for a
// conversation. A hold makes the human's intent explicit: while it is in
// force, a turn ending with nothing queued is not the stage's end. The
// agent waits for the holder's next message, each message is a turn in the
// same session, and the stage can only end once the holder releases — the
// session is then sent the handback prompt, and when THAT turn ends the
// stage's output contract applies as usual.
//
// The hold is on the record: hold_taken, and hold_released with its
// duration, turns and cost (every leg of it, across any park). A holder who
// goes quiet past the live hold parks the run (stage_paused held) the way
// an unanswered question does; their next message, or the release, wakes
// it.

var (
	// ErrAlreadyHeld: someone already holds this stage's session.
	ErrAlreadyHeld = errors.New("someone already holds this agent's session")
	// ErrNotHeld: a release with no take-over in force.
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
	return e.wakeHeld(context.Background(), runID, func(v run.View) []event.Payload {
		rel := &event.HoldReleased{StageID: v.Hold.StageID, Iteration: v.Hold.Iteration, UserID: userID,
			Reason: event.HoldReleasedByUser, DurationMS: max(e.clock().Sub(v.Hold.Since).Milliseconds(), 0),
			HoldSpend: event.HoldSpend{Turns: v.Hold.Turns, Messages: v.Hold.Messages}}
		if v.Hold.CostUSD > 0 {
			rel.Usage = &event.Usage{CostUSD: v.Hold.CostUSD}
		}
		return []event.Payload{rel}
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
// meantime owns the log, and refusing beats writing behind it.
func (e *Engine) wakeHeld(ctx context.Context, runID string, build func(run.View) []event.Payload) error {
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
	payloads := append(build(v), &event.RunUnpaused{Reason: event.UnpauseHold})
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
