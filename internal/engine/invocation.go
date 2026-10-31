package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
)

// invIO is what flows between a running invocation and the loop: the
// agent's transcript events, the human's messages, and the transcript they
// are recorded into.
type invIO struct {
	events chan agentport.AgentEvent
	done   chan struct{} // closed when the loop stops listening
	inbox  *inbox
	tr     *transcript
}

func newInvIO(tr *transcript) *invIO {
	return &invIO{events: make(chan agentport.AgentEvent, 256), done: make(chan struct{}), inbox: newInbox(), tr: tr}
}

func (io *invIO) input(prompt string) agentport.AgentInput {
	return agentport.AgentInput{
		Prompt: prompt,
		Inbox:  io.inbox,
		// Blocks while the loop is listening (ordering: everything the
		// runner emitted is queued before its result is), never after.
		Events: func(ev agentport.AgentEvent) {
			select {
			case io.events <- ev:
			case <-io.done:
			}
		},
	}
}

// drain records whatever events are already queued, through record.
func (io *invIO) drain(record func(agentport.AgentEvent) (bool, error)) error {
	for {
		select {
		case ev := <-io.events:
			if _, err := record(ev); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

// invoke runs one harness invocation to its end, servicing pings, messages
// and the transcript while it runs.
func (x *exec) invoke(st *flow.Stage, budget time.Duration, tr *transcript, initialOpen []*Ping, prompt string,
	call func(ctx context.Context, in agentport.AgentInput) (agentport.AgentResult, error)) (invocation, error) {
	runCtx, cancelRun := context.WithCancelCause(x.ctx)
	io := newInvIO(tr)
	resCh := make(chan agentRes, 1)
	in := io.input(prompt)
	go func() {
		r, err := call(runCtx, in)
		resCh <- agentRes{r, err}
	}()
	x.h.agentLive.Store(true)
	x.h.parking.Store(false)
	inv, err := x.awaitAgent(st, resCh, cancelRun, budget, io, initialOpen)
	x.h.agentLive.Store(false)
	cancelRun(nil)
	close(io.done)
	// Messages the agent never took stay pending (they are queued in the log
	// and in rs.pendingMsgs) and ride into the next prompt.
	if ferr := tr.flushAll(); ferr != nil && err == nil {
		err = ferr
	}
	return inv, err
}

// awaitAgent waits for one harness invocation while servicing pings, human
// messages and the transcript. The stage timeout is suspended while any ping
// is pending (§3 defaults.timeout) and what is left of it carries to the next
// invocation of the same attempt.
//
// A question blocks the agent's turn (ask_human waits in the tool call) for
// up to Config.AskHumanHold; past that the run is PARKED — the turn is
// cancelled and the invocation ends as a pause, to be resumed with the answer.
// An invocation that returns with questions still open is a pause too: the
// agent stopped rather than guessing, and the caller holds the attempt until
// the human answers. Letting it through instead is what silently turned an
// unanswered clarification into an approved-looking plan.
func (x *exec) awaitAgent(st *flow.Stage, resCh <-chan agentRes, cancelRun context.CancelCauseFunc, budget time.Duration,
	io *invIO, initialOpen []*Ping) (invocation, error) {
	remaining := budget
	open := append([]*Ping(nil), initialOpen...)
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	lastArm := time.Now()
	// The take-over in force, if any: carried in from the log (taken in an
	// earlier leg of this attempt) and tracked live from here.
	hold := x.liveHold(st)
	if hold != nil {
		io.inbox.setHeld(true)
	}
	holdLeg := func() event.HoldSpend {
		if hold != nil {
			return hold.leg.Spend
		}
		return event.HoldSpend{}
	}
	turnOpen := false
	// record files one agent event: a new harness session is recorded the
	// moment it exists, so a crash mid-invocation resumes (or reattaches
	// to) it, not a new one — whichever path meets the event, the loop's
	// select, a drain after the result, or a wind-down; everything else
	// goes to the transcript. session says it was a new session.
	record := func(ev agentport.AgentEvent) (session bool, err error) {
		if ev.Kind == agentport.AgentEvSession && ev.Text != "" && x.rs.Sessions[st.ID] != agentport.SessionID(ev.Text) {
			if err := x.append(&event.AgentSession{StageID: st.ID, Iteration: io.tr.iter, SessionID: ev.Text}); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, io.tr.handle(ev)
	}

	suspend := func() {
		if !timer.Stop() {
			// The timer fired concurrently with the ping; drain the stale
			// tick so the later Reset re-arms cleanly instead of firing a
			// spurious instant timeout after the human answers.
			select {
			case <-timer.C:
			default:
			}
		}
		remaining -= time.Since(lastArm)
		if remaining <= 0 {
			remaining = time.Millisecond
		}
	}
	resume := func() {
		lastArm = time.Now()
		timer.Reset(remaining)
	}
	// left is what the caller carries to the next invocation: the timer is
	// already stopped-and-debited whenever a ping is open.
	left := func() time.Duration {
		if len(open) > 0 || (hold != nil && hold.idle) {
			return remaining
		}
		return remaining - time.Since(lastArm)
	}
	var park *time.Timer
	var parkC <-chan time.Time
	armPark := func() {
		park = time.NewTimer(x.e.cfg.AskHumanHold)
		parkC = park.C
	}
	disarmPark := func() {
		if park != nil {
			park.Stop()
			park, parkC = nil, nil
		}
	}
	defer disarmPark()
	if len(open) > 0 {
		suspend()
		armPark()
	}
	// idleOnHolder: the agent's turn ended with nothing queued while someone
	// holds the session. The clock stops for the holder's thinking time,
	// and past the live hold the run parks on them.
	idleOnHolder := func() {
		if hold == nil || hold.idle || len(open) > 0 {
			return
		}
		hold.idle = true
		suspend()
		armPark()
	}
	busyAgain := func() {
		if hold == nil || !hold.idle {
			return
		}
		hold.idle = false
		resume()
		disarmPark()
	}
	// handBack ends the hold: its account is recorded, the handback prompt
	// is queued as the session's next turn (before the runner is let go of,
	// so a runner that sees the hold gone always finds it), and the stage
	// clock runs again.
	handBack := func(by string) error {
		if err := x.releaseHold(st, io.tr.iter, hold.st, by, event.HoldReleasedByUser, hold.leg.Spend); err != nil {
			return err
		}
		if err := x.prompted(st.ID, io.tr.iter, event.PromptHandback, handbackPrompt); err != nil {
			return err
		}
		io.inbox.push(agentport.UserMessage{Text: handbackPrompt, Verbatim: true})
		busyAgain()
		hold = nil
		io.inbox.setHeld(false)
		return nil
	}

	// collect waits for a cancelled invocation to wind down, still recording
	// its transcript — but doesn't trust the harness to honor the
	// cancellation: after a grace period it abandons the goroutine rather
	// than wedge the run.
	collect := func(out agentOutcome) (invocation, error) {
		grace := time.NewTimer(60 * time.Second)
		defer grace.Stop()
		for {
			select {
			case res := <-resCh:
				if err := io.drain(record); err != nil {
					return invocation{}, err
				}
				return invocation{res: res, outcome: out, open: open, left: left(), holdSpend: holdLeg()}, nil
			case ev := <-io.events:
				if _, err := record(ev); err != nil {
					return invocation{}, err
				}
			case <-grace.C:
				x.e.log.Warn("harness ignored cancellation; abandoning attempt", "run", x.rs.RunID, "stage", st.ID)
				return invocation{outcome: out, open: open, left: left(), holdSpend: holdLeg()}, nil
			}
		}
	}

	for {
		select {
		case res := <-resCh:
			if err := io.drain(record); err != nil {
				return invocation{}, err
			}
			out := outcomeReturned
			if runcore.EndedOnQuestions(len(open), res.err) {
				out = outcomePaused
			}
			return invocation{res: res, outcome: out, open: open, left: left(), holdSpend: holdLeg()}, nil
		case ev := <-io.events:
			if session, err := record(ev); err != nil {
				return invocation{}, err
			} else if session {
				continue
			}
			switch ev.Kind {
			case agentport.AgentEvUserMessage:
				// The holder's words reaching the agent, on the hold's account.
				if hold != nil {
					hold.leg.MessagesDelivered(ev.Messages)
				}
			case agentport.AgentEvTurnStart:
				turnOpen = true
				if hold != nil {
					// A turn starting under the hold is the holder's: it is
					// what their message prompted.
					busyAgain()
					hold.leg.TurnStarted(ev.Turn)
				}
			case agentport.AgentEvTurnEnd:
				turnOpen = false
				if hold == nil {
					break
				}
				hold.leg.TurnEnded(ev.Turn, ev.Usage)
				if len(open) > 0 {
					// The turn ended on the agent's own open question. The
					// holder is the one to answer it, but the tool call it came
					// in on is gone: park on the question as any attempt does,
					// and resume with the answer under the same hold.
					for _, p := range open {
						p.handedBack.Store(true)
					}
					x.h.parking.Store(true)
					cancelRun(errParked)
					return collect(outcomePaused)
				}
				if hold.releasing {
					if err := handBack(hold.releaseBy); err != nil {
						return invocation{}, err
					}
				} else if !io.inbox.pending() {
					idleOnHolder()
				}
			}
		case req := <-x.h.messages:
			if hold == nil || hold.releasing {
				req.ack <- ErrNotHeld
				continue
			}
			q := &event.MessageQueued{MessageID: req.msg.ID, Text: req.msg.Text, StageID: st.ID}
			if err := x.append(q); err != nil {
				req.ack <- err
				return invocation{}, err
			}
			io.inbox.push(req.msg)
			req.ack <- nil
		case req := <-x.h.holds:
			var err error
			held, releasing := hold != nil, hold != nil && hold.releasing
			switch runcore.PlanHoldRequest(req.take, held, releasing, turnOpen || io.inbox.pending()) {
			case runcore.HoldRefuseTaken:
				err = ErrAlreadyHeld
			case runcore.HoldTake:
				if err = x.takeHold(st, io.tr.iter, req.userID); err == nil {
					hold = x.liveHold(st)
					io.inbox.setHeld(true)
					if !turnOpen && !io.inbox.pending() {
						idleOnHolder()
					}
				}
			case runcore.HoldRefuseNotHeld:
				err = ErrNotHeld
			case runcore.HoldReleaseAfterTurn:
				hold.releasing, hold.releaseBy = true, req.userID
			case runcore.HoldHandBack:
				err = handBack(req.userID)
			}
			req.ack <- err
			if err != nil && !errors.Is(err, ErrAlreadyHeld) && !errors.Is(err, ErrNotHeld) {
				return invocation{}, err
			}
		case req := <-x.h.mailbox:
			x.e.mu.Lock()
			req.p.StageID = st.ID // the stage whose agent asked (PLAN4 M15)
			x.e.mu.Unlock()
			raised := &event.PingRaised{PingID: req.p.ID, Type: req.p.Type,
				Question: req.p.Question, Context: req.p.Context, Key: req.p.key,
				Files: req.p.Files, Link: req.p.Link, StageID: st.ID, Form: req.p.Form, AnswerKind: req.p.AnswerKind}
			if err := x.append(raised); err != nil {
				close(req.ack)
				return invocation{}, err
			}
			// Keep the working state a mirror of what replay would build, so
			// a resume reads the same whether the answer arrived in this
			// process or was found in the log after a restart.
			// Snapshot the attached files as they are RIGHT NOW: the agent
			// resumes the moment this ping is answered, and the record must
			// keep what the human was actually shown.
			x.publishPingFiles(req.p, st.ID)
			x.e.notify(x.bg, notifyport.Notification{
				RunID: x.rs.RunID, PingID: req.p.ID, Type: req.p.Type,
				Title: fmt.Sprintf("%s: %s", req.p.Type, x.rs.View.TicketTitle),
				Body:  req.p.Question,
			})
			if len(open) == 0 {
				suspend()
				armPark()
			}
			open = append(open, req.p)
			close(req.ack)
		case ar := <-x.h.answers:
			handled, err := x.processAnswer(ar)
			if err != nil {
				return invocation{}, err
			}
			// An answer the agent never saw does not close the question: once
			// the turn was handed back (parked), the only way in is a resume,
			// so the ping stays open and the attempt pauses on it below.
			if handled && runcore.AgentPingType(ar.p.Type) && !ar.p.handedBack.Load() {
				open = dropPing(open, ar.p)
				if len(open) == 0 {
					resume()
					disarmPark()
				}
			}
		case <-parkC:
			parkC = nil
			if hold != nil && hold.idle {
				// The holder went quiet: park on them, hold in force.
				x.h.parking.Store(true)
				cancelRun(errParked)
				inv, err := collect(outcomePaused)
				inv.holdPark = true
				return inv, err
			}
			// Nobody answered while the turn stayed live: park. The open
			// questions are handed back first, so an answer racing the
			// cancel is delivered by resume rather than lost in the tool call.
			for _, p := range open {
				p.handedBack.Store(true)
			}
			x.h.parking.Store(true)
			cancelRun(errParked)
			return collect(outcomePaused)
		case <-timer.C:
			cancelRun(errStageTimeout)
			return collect(outcomeTimedOut)
		case <-x.ctx.Done():
			if hold != nil {
				// The leg for whoever closes the hold next (an abort's account;
				// a pause re-reads the log and starts the leg over).
				x.cutLeg = hold.leg.Spend
			}
			cancelRun(context.Cause(x.ctx))
			return invocation{}, context.Cause(x.ctx)
		}
	}
}
