package engine

import (
	"context"
	"sync"
	"testing"

	"apothecary/internal/event"
	"apothecary/internal/runinput"
)

type recordingSink struct {
	mu  sync.Mutex
	evs []event.Event
}

func (r *recordingSink) OnEvent(e event.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, e)
}

func (r *recordingSink) count(t event.Type) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.evs {
		if e.Type == t {
			n++
		}
	}
	return n
}

// Every appended event reaches the sink exactly once; recovery replay does
// NOT re-emit history (§7: metrics would double-count otherwise).
func TestSinkReceivesFreshEventsOnly(t *testing.T) {
	sink := &recordingSink{}
	h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Sink = sink }, mustFlow(t, loopFlowDoc))

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	evs := h.events(t, runID)
	sink.mu.Lock()
	got := len(sink.evs)
	sink.mu.Unlock()
	if got != len(evs) {
		t.Errorf("sink saw %d events, store has %d", got, len(evs))
	}
	if sink.count(event.TypeRunCreated) != 1 || sink.count(event.TypeRunCompleted) != 1 {
		t.Errorf("terminal/created counts wrong: %+v", sink.evs)
	}

	// Simulate a restart: new engine over the same store, recovery of the
	// terminal run must not re-emit anything.
	sink2 := &recordingSink{}
	h2 := newHarnessWith(t, func(_ *harness, d *Deps) { d.Sink = sink2 }, mustFlow(t, loopFlowDoc))
	if err := h2.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	sink2.mu.Lock()
	replayed := len(sink2.evs)
	sink2.mu.Unlock()
	if replayed != 0 {
		t.Errorf("recovery re-emitted %d events to the sink", replayed)
	}

	// Archive emits through the sink too (engine-side writer).
	if err := h.e.Archive(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	if sink.count(event.TypeRunArchived) != 1 || sink.count(event.TypeEnvDestroyed) != 1 {
		t.Error("archive events did not reach the sink")
	}
}
