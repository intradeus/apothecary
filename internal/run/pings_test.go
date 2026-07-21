package run

import (
	"testing"
	"time"

	"apothecary/internal/event"
)

// TestReducePingsWithdrawsOnTerminal: a ping still open when its run reaches a
// terminal state is withdrawn (not left "pending" forever), while a ping
// answered before the run ended stays answered.
func TestReducePingsWithdrawsOnTerminal(t *testing.T) {
	base := time.Unix(0, 0).UTC()
	ev := func(runID string, ts int, p event.Payload) event.Event {
		return event.Event{RunID: runID, TS: base.Add(time.Duration(ts) * time.Second), Payload: p}
	}
	evs := []event.Event{
		ev("r1", 1, &event.PingRaised{PingID: "p1", Type: "clarification", Question: "still open?"}),
		ev("r1", 2, &event.PingRaised{PingID: "p2", Type: "clarification", Question: "answered?"}),
		ev("r1", 3, &event.PingAnswered{PingID: "p2", Answer: "yes"}),
		ev("r1", 4, &event.RunAbandoned{StalledAt: "build", Reason: "timeout"}),
	}
	byID := map[string]PingView{}
	for _, v := range ReducePings(evs) {
		byID[v.PingID] = v
	}
	if got := byID["p1"].Status; got != "withdrawn" {
		t.Errorf("p1 status = %q, want withdrawn (run abandoned while it was open)", got)
	}
	if got := byID["p2"].Status; got != "answered" {
		t.Errorf("p2 status = %q, want answered (answered before the run ended)", got)
	}
}
