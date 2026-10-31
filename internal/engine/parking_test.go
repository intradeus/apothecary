package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

// A human's verb landing while the loop is on its way to parked: the log is
// about to say so, the loop is not gone yet. A stalled write holds that
// window open. What arrives in it must act on the parked run — never reach a
// loop that is leaving, which would drop it or refuse it for no reason.

// stallStore stops the first write of one event type until let go.
type stallStore struct {
	event.Store
	typ     event.Type
	once    sync.Once
	reached chan struct{}
	letGo   chan struct{}
}

func stallAt(typ event.Type) (*stallStore, func(*event.MemStore) event.Store) {
	s := &stallStore{typ: typ, reached: make(chan struct{}), letGo: make(chan struct{})}
	return s, func(m *event.MemStore) event.Store {
		s.Store = m
		return s
	}
}

func (s *stallStore) Append(ctx context.Context, runID string, ps ...event.Payload) ([]event.Event, error) {
	for _, p := range ps {
		if p.EventType() == s.typ {
			s.once.Do(func() {
				close(s.reached)
				<-s.letGo
			})
		}
	}
	return s.Store.Append(ctx, runID, ps...)
}

// inWindow runs act while the loop is stalled writing run_paused, lets the
// write go a moment later, and returns act's error once it has one.
func inWindow(t *testing.T, s *stallStore, act func() error) error {
	t.Helper()
	select {
	case <-s.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop never got to parking")
	}
	errs := make(chan error, 1)
	go func() { errs <- act() }()
	time.Sleep(20 * time.Millisecond)
	close(s.letGo)
	select {
	case err := <-errs:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the verb never came back")
		return nil
	}
}

// parkOnQuestion starts a run whose agent ends its turn on a question.
func parkOnQuestion(t *testing.T, store func(*event.MemStore) event.Store) (*harness, string) {
	t.Helper()
	h := newHarnessOn(t, store, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	h.runner.RunFunc = func(_ context.Context, _ fake.RunCall) (agentport.AgentResult, error) {
		id := <-runIDCh
		if _, err := h.e.RaisePing(id, "clarification", "which db?", "", nil, nil); err != nil {
			return agentport.AgentResult{}, err
		}
		return agentport.AgentResult{TurnsUsed: 1, Session: "sess-1"}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID
	return h, runID
}

func TestPauseWhileParkingPausesTheRun(t *testing.T) {
	s, store := stallAt(event.TypeRunPaused)
	h, runID := parkOnQuestion(t, store)
	if err := inWindow(t, s, func() error { return h.e.Pause(runID) }); err != nil {
		t.Fatalf("pause: %v", err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Paused {
		t.Errorf("state = %s, want paused: %v", v.State, eventTypes(h.events(t, runID)))
	}
}

func TestAbortWhileParkingAbandonsTheRun(t *testing.T) {
	s, store := stallAt(event.TypeRunPaused)
	h, runID := parkOnQuestion(t, store)
	if err := inWindow(t, s, func() error { return h.e.Abort(runID) }); err != nil {
		t.Fatalf("abort: %v", err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Abandoned {
		t.Errorf("state = %s, want abandoned: %v", v.State, eventTypes(h.events(t, runID)))
	}
}

// parkOnHolder starts a run whose holder goes quiet: the attempt parks on them.
func parkOnHolder(t *testing.T, store func(*event.MemStore) event.Store) (*harness, string) {
	t.Helper()
	h := newHarnessOn(t, store, mustFlow(t, askFlowDoc))
	h.e.cfg.AskHumanHold = 30 * time.Millisecond
	r := &heldRunner{firstEnd: make(chan struct{})}
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		r.turn(call.In, "working on it", r.firstEnd)
		return r.drive(ctx, call.In)
	}
	h.runner.ResumeFunc = func(ctx context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
		r.turn(call.In, "finishing", nil)
		return r.drive(ctx, call.In)
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.AgentSession](t, h, runID) // the turn is open
	takeOverWhenLive(t, h, runID, "u1")
	close(r.firstEnd)
	return h, runID
}

func TestReleaseWhileParkingWakesTheRun(t *testing.T) {
	s, store := stallAt(event.TypeRunPaused)
	h, runID := parkOnHolder(t, store)
	if err := inWindow(t, s, func() error { return h.e.Release(runID, "u1") }); err != nil {
		t.Fatalf("release: %v", err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Errorf("state = %s, want it handed back and finished: %v", v.State, eventTypes(h.events(t, runID)))
	}
}

func TestAMessageWhileParkingWakesTheRun(t *testing.T) {
	s, store := stallAt(event.TypeRunPaused)
	h, runID := parkOnHolder(t, store)
	err := inWindow(t, s, func() error {
		_, err := h.e.SendMessage(runID, "try the other way")
		return err
	})
	if err != nil {
		t.Fatalf("message: %v", err)
	}
	waitUntil(t, "the agent to be resumed", func() bool {
		_, ok := findPayload[*event.StageResumed](h.events(t, runID))
		return ok
	})
}
