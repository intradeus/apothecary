package runcore

import (
	"testing"

	"apothecary/internal/run/event"
)

// A ping's reference is its place among the pings raised in or about its
// stage's latest attempt, whatever their type; a ping with no stage, or whose
// stage has not started, is the run's. Two in one batch are numbered in
// order; a ping the run already holds keeps its reference; a replay of the
// recorded log numbers the next ping the same way.
func TestPingRefs(t *testing.T) {
	rs := NewState(nil)
	var log []event.Event
	record := func(ps ...event.Payload) {
		rs.PingRefs(ps)
		for _, p := range ps {
			ev := event.Event{Seq: int64(len(log) + 1), Payload: p}
			log = append(log, ev)
			rs.Apply(&ev)
		}
	}
	ask := func(id, stage, typ string) *event.PingRaised {
		return &event.PingRaised{PingID: id, StageID: stage, Type: typ}
	}
	confirm := ask("c1", "", event.PingPhaseConfirm)
	record(confirm)
	early := ask("e1", "review", event.PingClarification) // review has not started
	record(early)
	record(&event.StageStarted{StageID: "plan", Attempt: 1})
	planned := ask("p1", "plan", event.PingGate)
	record(planned)
	record(&event.StageStarted{StageID: "build", Attempt: 1}, &event.StageStarted{StageID: "build", Attempt: 2})
	q1, q2 := ask("q1", "build", event.PingClarification), ask("q2", "build", event.PingClarification)
	record(q1, q2)
	debug := ask("d1", "build", event.PingDebug) // after build#2 failed: about it
	record(debug)
	record(&event.StageStarted{StageID: "build", Attempt: 3})
	q3 := ask("q3", "build", event.PingClarification)
	record(q3)
	again := ask("q3", "build", event.PingClarification) // re-raised after a crash
	record(again)

	for p, want := range map[*event.PingRaised]string{
		confirm: "run/decision#1", early: "run/decision#2", planned: "plan#1/decision#1",
		q1: "build#2/decision#1", q2: "build#2/decision#2", debug: "build#2/decision#3",
		q3: "build#3/decision#1", again: "build#3/decision#1",
	} {
		if p.Ref != want {
			t.Errorf("%s (%s): ref = %q, want %q", p.PingID, p.Type, p.Ref, want)
		}
	}

	replayed := Replay(log, nil)
	next := ask("q4", "build", event.PingClarification)
	replayed.PingRefs([]event.Payload{next})
	if next.Ref != "build#3/decision#2" {
		t.Errorf("after a replay: %q", next.Ref)
	}
	set := &event.PingRaised{PingID: "x", StageID: "build", Ref: "build#9/decision#9"}
	if replayed.PingRefs([]event.Payload{set}); set.Ref != "build#9/decision#9" {
		t.Errorf("a ping recorded with its reference got %q", set.Ref)
	}
}
