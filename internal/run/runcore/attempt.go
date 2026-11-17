package runcore

import (
	"fmt"
	"strings"

	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/run/event"
)

// How one stage attempt ended, and what a take-over spent during it, pure.
// The engine runs the attempt and reads what it left; these say what that
// means.

// AttemptObservation is what an agent or command attempt left behind, read in
// contract order up to the first fault: the engine stops reading there, since
// a later check says nothing useful about an attempt that already failed.
type AttemptObservation struct {
	TimedOut bool
	// Err is why the harness failed.
	Err string
	// ExitCode is a command's.
	ExitCode int
	// Output is the tail of what the attempt printed.
	Output string
	// Missing are the declared outputs that cannot be read.
	Missing []string
	// Invalid says what is wrong with an output that exists: its shape, its
	// verdict, a review output against the threads this round loaded.
	Invalid string
}

// AttemptFailure is an attempt that failed: what its stage_failed says and
// the edge it takes, with the feed that edge carries.
type AttemptFailure struct {
	Reason   string
	Error    string
	ExitCode *int
	EdgeKind string
	Feed     string
}

// ClassifyAttempt reads an attempt's observation: nil when it met its stage's
// contract. An agent's retry is told which files it did not write; a
// command's is told what it printed.
func ClassifyAttempt(st *flow.Stage, o AttemptObservation) *AttemptFailure {
	switch {
	case o.TimedOut:
		// Whatever the cancelled attempt printed rides along for edges that
		// declare feed: output.
		return &AttemptFailure{Reason: event.ReasonTimeout, EdgeKind: FailEdgeKind(st, event.ReasonTimeout), Feed: o.Output}
	case o.Err != "":
		return &AttemptFailure{Reason: event.ReasonHarnessError, Error: o.Err, EdgeKind: flow.EdgeFail, Feed: o.Output}
	case o.ExitCode != 0:
		code := o.ExitCode
		// The output tail rides in the event so recovery can route the
		// failure without re-executing the command.
		return &AttemptFailure{Reason: event.ReasonExitNonzero, Error: o.Output, ExitCode: &code, EdgeKind: flow.EdgeFail, Feed: o.Output}
	case len(o.Missing) > 0:
		msg := fmt.Sprintf("declared output(s) not readable: %s", strings.Join(o.Missing, ", "))
		feed := msg
		if st.Type == flow.StageCommand {
			feed = o.Output
		}
		return &AttemptFailure{Reason: event.ReasonOutputContract, Error: msg, EdgeKind: flow.EdgeFail, Feed: feed}
	case o.Invalid != "":
		return &AttemptFailure{Reason: event.ReasonOutputInvalid, Error: o.Invalid, EdgeKind: flow.EdgeFail, Feed: o.Invalid}
	}
	return nil
}

// FailEdgeKind picks the edge kind for a stage failure: timeouts route
// through on_timeout when the stage declares it, everything else (and
// timeouts without one) through on_fail.
func FailEdgeKind(st *flow.Stage, reason string) string {
	if reason == event.ReasonTimeout && st.Edge(flow.EdgeTimeout) != nil {
		return flow.EdgeTimeout
	}
	return flow.EdgeFail
}

// EndedOnQuestions reports whether a turn that returned with questions still
// open is a pause: the agent ended it on them. An error is not — a harness
// that died mid-question has nothing left to resume into, so it routes as the
// failure it is.
func EndedOnQuestions(open int, harnessErr error) bool {
	return open > 0 && harnessErr == nil
}

// HoldMove is what a take-over request does to the invocation in flight.
type HoldMove int

const (
	// HoldTake: the session goes into the requester's hands.
	HoldTake HoldMove = iota
	// HoldTakeAndStop: the session goes into the requester's hands and the
	// agent's own turn in flight is stopped, so it waits on them at once.
	HoldTakeAndStop
	// HoldRefuseTaken: someone already holds it.
	HoldRefuseTaken
	// HoldRefuseNotHeld: a release with nothing to release.
	HoldRefuseNotHeld
	// HoldAlreadyReleasing: already handing back when the turn in flight ends.
	HoldAlreadyReleasing
	// HoldReleaseAfterTurn: the handback follows the turn in flight, the
	// one the queued message is about to start, or the one the agent's open
	// question is from.
	HoldReleaseAfterTurn
	// HoldHandBack: the agent is idle, the handback goes now.
	HoldHandBack
)

// HoldRequest is a take (or else a release) and what it lands on.
type HoldRequest struct {
	// Take is a take-over; false is a release.
	Take bool
	// Force is a take-over that stops the agent's turn in flight.
	Force bool
	// Held: a hold is in force; Releasing: it is already handing back.
	Held, Releasing bool
	// TurnOpen: a turn is in flight; Queued: a message is still to be
	// delivered, queued or taken by the runner, and will start one.
	TurnOpen, Queued bool
	// QuestionOpen: the agent's own question is waiting on a human.
	QuestionOpen bool
}

// PlanHoldRequest reads a take (or else a release) against the hold in force.
// A forced take stops the turn in flight unless that turn waits on its own
// question: the agent is not working then, and a turn ending on an open
// question parks the run. A release waits while the agent is busy — a turn
// in flight, a message queued to start one, or a question of its own, whose
// turn either runs on with the answer or parks the run, the handback then
// owed to the session the answer resumes.
func PlanHoldRequest(r HoldRequest) HoldMove {
	switch {
	case r.Take && r.Held:
		return HoldRefuseTaken
	case r.Take && r.Force && r.TurnOpen && !r.QuestionOpen:
		return HoldTakeAndStop
	case r.Take:
		return HoldTake
	case !r.Held:
		return HoldRefuseNotHeld
	case r.Releasing:
		return HoldAlreadyReleasing
	case r.TurnOpen || r.Queued || r.QuestionOpen:
		return HoldReleaseAfterTurn
	}
	return HoldHandBack
}

// HoldLeg accounts for one invocation under a take-over: the holder's
// messages, the turns they prompted and what those cost.
type HoldLeg struct {
	Spend event.HoldSpend
	// turns are the ones started under the hold: only those are the holder's.
	turns map[string]bool
	// waiting counts the holder's messages queued but not yet delivered.
	waiting int
	// due: the holder's words have reached the agent but the turn they
	// prompt has not started yet.
	due bool
}

// NewHoldLeg starts a leg with nothing spent.
func NewHoldLeg() HoldLeg { return HoldLeg{turns: map[string]bool{}} }

// MessageQueued counts a holder's message waiting for the agent.
func (l *HoldLeg) MessageQueued() { l.waiting++ }

// Waiting reports whether a holder's message still has its turn to come:
// queued, taken but not yet delivered, or delivered with its turn not yet
// started.
func (l *HoldLeg) Waiting() bool { return l.waiting > 0 || l.due }

// MessagesDelivered counts the holder's words reaching the agent. The
// engine's own — a verbatim handback — are not theirs. The holder's are due
// a turn until the next one starts.
func (l *HoldLeg) MessagesDelivered(msgs []agentport.UserMessage) {
	for _, m := range msgs {
		if !m.Verbatim {
			l.Spend.Messages++
			l.waiting = max(l.waiting-1, 0)
			l.due = true
		}
	}
}

// TurnStarted counts a turn starting under the hold, once: it is what the
// holder's message prompted.
func (l *HoldLeg) TurnStarted(turn string) {
	l.due = false
	if !l.turns[turn] {
		l.turns[turn] = true
		l.Spend.Turns++
	}
}

// TurnEnded adds what a turn of the holder's cost.
func (l *HoldLeg) TurnEnded(turn string, u *agentport.Usage) {
	if l.turns[turn] && u != nil {
		l.Spend.Add(event.HoldSpend{Usage: &event.Usage{InputTokens: u.InputTokens,
			OutputTokens: u.OutputTokens, CostUSD: u.CostUSD}})
	}
}
