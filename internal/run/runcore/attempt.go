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
	// HoldRefuseTaken: someone already holds it.
	HoldRefuseTaken
	// HoldRefuseNotHeld: a release with nothing to release.
	HoldRefuseNotHeld
	// HoldAlreadyReleasing: already handing back when the turn in flight ends.
	HoldAlreadyReleasing
	// HoldReleaseAfterTurn: the handback follows the turn in flight, or the
	// one the queued message is about to start.
	HoldReleaseAfterTurn
	// HoldHandBack: the agent is idle, the handback goes now.
	HoldHandBack
)

// PlanHoldRequest reads a take (or else a release) against the hold in force:
// whether one is, whether it is already releasing, and whether the agent is
// busy — a turn in flight, or a message queued to start one.
func PlanHoldRequest(take, held, releasing, busy bool) HoldMove {
	switch {
	case take && held:
		return HoldRefuseTaken
	case take:
		return HoldTake
	case !held:
		return HoldRefuseNotHeld
	case releasing:
		return HoldAlreadyReleasing
	case busy:
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
}

// NewHoldLeg starts a leg with nothing spent.
func NewHoldLeg() HoldLeg { return HoldLeg{turns: map[string]bool{}} }

// MessagesDelivered counts the holder's words reaching the agent. The
// engine's own — a verbatim handback — are not theirs.
func (l *HoldLeg) MessagesDelivered(msgs []agentport.UserMessage) {
	for _, m := range msgs {
		if !m.Verbatim {
			l.Spend.Messages++
		}
	}
}

// TurnStarted counts a turn starting under the hold, once: it is what the
// holder's message prompted.
func (l *HoldLeg) TurnStarted(turn string) {
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
