package engine

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
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

// stallStore stops the first write of one event type until let go: before
// it lands, or (after) once it is on the log.
type stallStore struct {
	event.Store
	typ     event.Type
	after   bool
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

// stallAfter is stallAt with the write on the log while it stalls.
func stallAfter(typ event.Type) (*stallStore, func(*event.MemStore) event.Store) {
	s, store := stallAt(typ)
	s.after = true
	return s, store
}

func (s *stallStore) Append(ctx context.Context, runID string, ps ...event.Payload) ([]event.Event, error) {
	hit := false
	for _, p := range ps {
		hit = hit || p.EventType() == s.typ
	}
	if hit && !s.after {
		s.stall()
	}
	evs, err := s.Store.Append(ctx, runID, ps...)
	if hit && s.after {
		s.stall()
	}
	return evs, err
}

func (s *stallStore) stall() {
	s.once.Do(func() {
		close(s.reached)
		<-s.letGo
	})
}

// inWindow runs act while the loop is stalled writing run_paused, lets the
// write go a moment later, and returns act's error once it has one. The
// tests on it pass whether act lands in the window or just after it (the
// run parked either way); one whose outcome hinges on act being in the
// window uses inWindowParked.
func inWindow(t *testing.T, s *stallStore, act func() error) error {
	t.Helper()
	return stallWindow(t, s, act, func(int, <-chan struct{}) { time.Sleep(20 * time.Millisecond) })
}

// inWindowParked runs act while the loop is stalled and lets the write go
// only once act is parked in fn (a function of this package, such as
// "(*Engine).messageLive"): blocked there on a channel or a lock, waiting
// on the loop. What act hands the loop is then there when the loop goes on,
// however the goroutines are scheduled — a sleep only makes that likely.
func inWindowParked(t *testing.T, s *stallStore, fn string, act func() error) error {
	t.Helper()
	return stallWindow(t, s, act, func(id int, returned <-chan struct{}) { waitParked(t, id, fn, returned) })
}

func stallWindow(t *testing.T, s *stallStore, act func() error, settle func(id int, returned <-chan struct{})) error {
	t.Helper()
	select {
	case <-s.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop never got to parking")
	}
	errs := make(chan error, 1)
	ids := make(chan int, 1)
	returned := make(chan struct{})
	go func() {
		ids <- goroutineID()
		errs <- act()
		close(returned)
	}()
	settle(<-ids, returned)
	close(s.letGo)
	select {
	case err := <-errs:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the verb never came back")
		return nil
	}
}

// goroutineID is the calling goroutine's id, as its stack trace names it.
func goroutineID() int {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	var id int
	if _, err := fmt.Sscanf(string(buf), "goroutine %d ", &id); err != nil {
		panic(err)
	}
	return id
}

// parkedStates are a goroutine's states (its stack trace's header) while it
// is blocked on a channel or a lock.
var parkedStates = []string{"select", "chan send", "chan receive", "sync.Mutex.Lock",
	"sync.RWMutex.Lock", "sync.RWMutex.RLock", "sync.Cond.Wait", "semacquire"}

// waitParked waits until goroutine id is parked in fn — its innermost frame
// of this package's, under the runtime's and the sync package's — or until
// returned is closed. Only a goroutine there for good passes: the trace is
// the runtime's own record of where it is blocked, not a guess from time.
func waitParked(t *testing.T, id int, fn string, returned <-chan struct{}) {
	t.Helper()
	header := fmt.Sprintf("goroutine %d [", id)
	frame := "apothecary/internal/engine." + fn + "("
	deadline := time.Now().Add(10 * time.Second)
	buf := make([]byte, 1<<20)
	for {
		select {
		case <-returned:
			return
		default:
		}
		n := runtime.Stack(buf, true)
		if n == len(buf) {
			buf = make([]byte, 2*len(buf))
			continue
		}
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.HasPrefix(g, header) && parkedIn(g, len(header), frame) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine %d never parked in %s", id, fn)
		}
		runtime.Gosched()
	}
}

// parkedIn reports whether a goroutine's trace g (state from offset at)
// shows it blocked with frame as its innermost frame outside the runtime
// and sync packages.
func parkedIn(g string, at int, frame string) bool {
	state, _, _ := strings.Cut(g[at:], "]")
	state, _, _ = strings.Cut(state, ",") // "select, 2 minutes"
	if !slices.Contains(parkedStates, state) {
		return false
	}
	for _, line := range strings.Split(g, "\n")[1:] {
		switch {
		case strings.HasPrefix(line, "\t"):
		case strings.HasPrefix(line, "runtime."), strings.HasPrefix(line, "sync."), strings.HasPrefix(line, "internal/"):
		default:
			return strings.HasPrefix(line, frame)
		}
	}
	return false
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
	takeOverWhenLive(t, h, runID, "u1", true)
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
		_, err := h.e.SendMessage(runID, "u1", "try the other way", nil)
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
