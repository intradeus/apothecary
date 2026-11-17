package runcore

import (
	"errors"
	"testing"

	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/run/event"
)

func TestClassifyAttempt(t *testing.T) {
	agent := &flow.Stage{ID: "a", Type: flow.StageAgent}
	command := &flow.Stage{ID: "c", Type: flow.StageCommand}
	const missing = "declared output(s) not readable: x, y"
	tests := []struct {
		name                     string
		st                       *flow.Stage
		o                        AttemptObservation
		reason, errText, feed, k string
	}{
		{name: "passed", st: agent, o: AttemptObservation{Output: "fine"}},
		{name: "timeout feeds the output", st: agent, o: AttemptObservation{TimedOut: true, Output: "tail", Err: "ignored"},
			reason: event.ReasonTimeout, feed: "tail", k: flow.EdgeFail},
		{name: "harness error", st: agent, o: AttemptObservation{Err: "boom", Output: "tail"},
			reason: event.ReasonHarnessError, errText: "boom", feed: "tail", k: flow.EdgeFail},
		{name: "non-zero exit", st: command, o: AttemptObservation{ExitCode: 3, Output: "tail"},
			reason: event.ReasonExitNonzero, errText: "tail", feed: "tail", k: flow.EdgeFail},
		{name: "an agent is told what it did not write", st: agent, o: AttemptObservation{Missing: []string{"x", "y"}, Output: "tail"},
			reason: event.ReasonOutputContract, errText: missing, feed: missing, k: flow.EdgeFail},
		{name: "a command is told what it printed", st: command, o: AttemptObservation{Missing: []string{"x", "y"}, Output: "tail"},
			reason: event.ReasonOutputContract, errText: missing, feed: "tail", k: flow.EdgeFail},
		{name: "invalid output", st: agent, o: AttemptObservation{Invalid: "bad shape"},
			reason: event.ReasonOutputInvalid, errText: "bad shape", feed: "bad shape", k: flow.EdgeFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := ClassifyAttempt(tt.st, tt.o)
			if tt.reason == "" {
				if f != nil {
					t.Fatalf("got %+v, want it to pass", f)
				}
				return
			}
			if f == nil || f.Reason != tt.reason || f.Error != tt.errText || f.Feed != tt.feed || f.EdgeKind != tt.k {
				t.Fatalf("got %+v", f)
			}
		})
	}
	if f := ClassifyAttempt(command, AttemptObservation{ExitCode: 3}); f.ExitCode == nil || *f.ExitCode != 3 {
		t.Fatalf("exit code = %v", f.ExitCode)
	}
}

func TestEndedOnQuestions(t *testing.T) {
	if !EndedOnQuestions(1, nil) || EndedOnQuestions(0, nil) || EndedOnQuestions(1, errors.New("died")) {
		t.Fatal("a turn over with questions open pauses; a harness that died does not")
	}
}

func TestPlanHoldRequest(t *testing.T) {
	tests := []struct {
		req  HoldRequest
		want HoldMove
	}{
		{HoldRequest{Take: true}, HoldTake},
		{HoldRequest{Take: true, TurnOpen: true}, HoldTake}, // passive: the turn runs on
		{HoldRequest{Take: true, Queued: true}, HoldTake},
		{HoldRequest{Take: true, Force: true}, HoldTake},                                     // nothing in flight
		{HoldRequest{Take: true, Force: true, Queued: true}, HoldTake},                       // no turn yet to stop
		{HoldRequest{Take: true, Force: true, TurnOpen: true, QuestionOpen: true}, HoldTake}, // waiting on a human
		{HoldRequest{Take: true, Force: true, TurnOpen: true}, HoldTakeAndStop},
		{HoldRequest{Take: true, Force: true, TurnOpen: true, Queued: true}, HoldTakeAndStop},
		{HoldRequest{Take: true, Held: true}, HoldRefuseTaken},
		{HoldRequest{Take: true, Force: true, Held: true, TurnOpen: true}, HoldRefuseTaken},
		{HoldRequest{}, HoldRefuseNotHeld},
		{HoldRequest{TurnOpen: true}, HoldRefuseNotHeld},
		{HoldRequest{Held: true, Releasing: true, TurnOpen: true}, HoldAlreadyReleasing},
		{HoldRequest{Held: true, TurnOpen: true}, HoldReleaseAfterTurn},
		{HoldRequest{Held: true, Queued: true}, HoldReleaseAfterTurn},
		{HoldRequest{Held: true}, HoldHandBack},
	}
	for _, tt := range tests {
		if got := PlanHoldRequest(tt.req); got != tt.want {
			t.Errorf("%+v: got %v, want %v", tt.req, got, tt.want)
		}
	}
}

func TestHoldLegCountsOnlyTheHoldersSpend(t *testing.T) {
	l := NewHoldLeg()
	l.MessagesDelivered([]agentport.UserMessage{{Text: "mine"}, {Text: "handback", Verbatim: true}})
	l.TurnEnded("before", &agentport.Usage{CostUSD: 9}) // started before the hold
	l.TurnStarted("t1")
	l.TurnStarted("t1")
	l.TurnEnded("t1", &agentport.Usage{InputTokens: 2, OutputTokens: 3, CostUSD: 1.5})
	l.TurnEnded("t1", nil)
	s := l.Spend
	if s.Messages != 1 || s.Turns != 1 || s.Usage == nil || s.Usage.CostUSD != 1.5 || s.Usage.InputTokens != 2 || s.Usage.OutputTokens != 3 {
		t.Fatalf("spend = %+v usage %+v", s, s.Usage)
	}
}

func TestHoldLegWaitsUntilTheHoldersMessagesAreDelivered(t *testing.T) {
	l := NewHoldLeg()
	if l.Waiting() {
		t.Fatal("a new leg waits on nothing")
	}
	l.MessageQueued()
	l.MessageQueued()
	l.MessagesDelivered([]agentport.UserMessage{{Text: "one"}, {Text: "handback", Verbatim: true}})
	if !l.Waiting() {
		t.Fatal("one of two messages delivered, and a handback, must still wait")
	}
	l.MessagesDelivered([]agentport.UserMessage{{Text: "two"}, {Text: "carried from a re-entry"}})
	if l.Waiting() {
		t.Fatal("every message delivered, still waiting")
	}
	l.MessageQueued()
	if !l.Waiting() {
		t.Fatal("a delivery past the queue must not cover the next message")
	}
}
