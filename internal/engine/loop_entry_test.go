package engine

// What a run's loop does as it comes up, before any stage: the project and
// the flow may have changed or gone while it was down, and each of those has
// one honest answer — fail for good, or stay put for the next boot.

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

// live seeds a run with a workspace that exists, its first stage underway.
func (h *harness) seedLive(t *testing.T, runID, flowName string, ps ...event.Payload) envport.EnvID {
	t.Helper()
	env, err := h.envp.Provision(context.Background(), envport.EnvSpec{})
	if err != nil {
		t.Fatal(err)
	}
	head := []event.Payload{&event.RunCreated{TicketTitle: "T", FlowName: flowName, FlowHash: h.flows[flowName].Hash,
		ProjectID: testProject, Branch: "feat/x"},
		&event.PhaseStarted{Phase: flow.MainPhase}, &event.EnvReady{EnvID: string(env)}}
	h.seed(t, runID, append(head, ps...)...)
	return env
}

// funcSource is a flow source scripted by a function.
type funcSource func(name string) (*flow.Flow, error)

func (f funcSource) Get(_ context.Context, _, name string) (*flow.Flow, error) { return f(name) }

func recoverAndSettle(t *testing.T, h *harness, runID string) run.View {
	t.Helper()
	if err := h.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	return h.view(t, runID)
}

func TestAProjectThatWillNeverResolveFailsTheRun(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.seedLive(t, "r1", "loop")
	h.e.resolver = resolution.StaticResolver{}
	v := recoverAndSettle(t, h, "r1")
	if v.State != run.Failed {
		t.Fatalf("state = %s", v.State)
	}
	rf, _ := findPayload[*event.RunFailed](h.events(t, "r1"))
	if rf == nil || !strings.Contains(rf.Reason, "unresolvable") {
		t.Errorf("run_failed = %+v", rf)
	}
}

func TestAProjectThatFailedToResolveStaysForTheNextBoot(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.seedLive(t, "r1", "loop")
	h.e.resolver = failingResolver{errors.New("vault sealed")}
	v := recoverAndSettle(t, h, "r1")
	if v.State.Terminal() {
		t.Errorf("a transient failure ended the run: %s", v.State)
	}
	if n := len(h.events(t, "r1")); n != 3 {
		t.Errorf("a run left for recovery wrote: %v", eventTypes(h.events(t, "r1")))
	}
}

func TestAFlowThatIsGone(t *testing.T) {
	parseErr := &flow.Error{File: "f.yaml", Line: 3, Msg: "unknown key"}
	cases := []struct {
		name     string
		err      error
		terminal bool
		reason   string
	}{
		{"deleted", flow.ErrNotFound, true, `flow "loop" not found`},
		{"no longer parses", parseErr, true, `flow "loop" no longer parses, or no longer resolves with this run's parameters`},
		{"a database blip", errors.New("db down"), false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, mustFlow(t, loopFlowDoc))
			h.seedLive(t, "r1", "loop")
			h.e.flows = funcSource(func(string) (*flow.Flow, error) { return nil, c.err })
			v := recoverAndSettle(t, h, "r1")
			if v.State.Terminal() != c.terminal {
				t.Fatalf("state = %s", v.State)
			}
			if c.terminal {
				rf, _ := findPayload[*event.RunFailed](h.events(t, "r1"))
				if rf == nil || !strings.Contains(rf.Reason, c.reason) {
					t.Errorf("run_failed = %+v", rf)
				}
			}
		})
	}
}

// A flow edited under a run must not report a half-finished run as finished.
func TestAFlowEditedUnderTheRun(t *testing.T) {
	t.Run("its stage is gone", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.seedLive(t, "r1", "loop", &event.StageStarted{StageID: "vanished", Iteration: 1})
		v := recoverAndSettle(t, h, "r1")
		rf, _ := findPayload[*event.RunFailed](h.events(t, "r1"))
		if v.State != run.Failed || rf == nil || !strings.Contains(rf.Reason, "current stage no longer exists") {
			t.Errorf("state %s, run_failed %+v", v.State, rf)
		}
	})
	t.Run("its phase is gone", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.seedLive(t, "r1", "loop", &event.PhaseCompleted{Phase: flow.MainPhase}, &event.PhaseStarted{Phase: "vanished"})
		v := recoverAndSettle(t, h, "r1")
		rf, _ := findPayload[*event.RunFailed](h.events(t, "r1"))
		if v.State != run.Failed || rf == nil || !strings.Contains(rf.Reason, "no longer exists") {
			t.Errorf("state %s, run_failed %+v: %v", v.State, rf, eventTypes(h.events(t, "r1")))
		}
	})
}

// pinnedOnly serves the current document by name and history by hash.
type pinnedOnly struct {
	cur *flow.Flow
	err error
}

func (p pinnedOnly) Get(context.Context, string, string) (*flow.Flow, error) { return p.cur, nil }
func (p pinnedOnly) GetVersion(context.Context, string, string, string) (*flow.Flow, error) {
	return nil, p.err
}

// A run keeps the version it was created with; when that version is gone it
// runs on the current one rather than dying, and a store failure is not
// "gone".
func TestARunWhosePinIsGone(t *testing.T) {
	fl := mustFlow(t, loopFlowDoc)
	t.Run("falls back to the current document", func(t *testing.T) {
		h := newHarness(t, fl)
		h.seedLive(t, "r1", "loop", &event.RunPaused{Reason: event.PauseWaiting})
		h.store.Append(context.Background(), "r1", &event.RunUnpaused{}) //nolint:errcheck
		cur := *fl
		cur.Hash = "another"
		h.e.flows = pinnedOnly{cur: &cur, err: flow.ErrNotFound}
		v := recoverAndSettle(t, h, "r1")
		if v.State != run.Completed {
			t.Errorf("state = %s: %v", v.State, eventTypes(h.events(t, "r1")))
		}
	})
	t.Run("a store failure stays for the next boot", func(t *testing.T) {
		h := newHarness(t, fl)
		h.seedLive(t, "r1", "loop")
		h.e.flows = pinnedOnly{cur: fl, err: errors.New("db down")}
		v := recoverAndSettle(t, h, "r1")
		if v.State.Terminal() {
			t.Errorf("state = %s", v.State)
		}
	})
}

// eventsDown refuses to read a run's log.
type eventsDown struct {
	event.Store
	down atomic.Bool
}

func (s *eventsDown) Events(ctx context.Context, runID string, after int64) ([]event.Event, error) {
	if s.down.Load() {
		return nil, errors.New("db down")
	}
	return s.Store.Events(ctx, runID, after)
}

// A loop that cannot read its log leaves it alone, and frees its slot.
func TestALoopThatCannotReadItsLog(t *testing.T) {
	store := &eventsDown{}
	h := newHarnessOn(t, func(m *event.MemStore) event.Store { store.Store = m; return store }, mustFlow(t, loopFlowDoc))
	h.seedLive(t, "r1", "loop")
	store.down.Store(true)
	h.e.mu.Lock()
	h.e.startLocked("r1")
	h.e.mu.Unlock()
	h.e.Wait("r1")
	store.down.Store(false)
	if h.e.active("r1") {
		t.Error("the loop kept its slot")
	}
	if got := len(h.events(t, "r1")); got != 3 {
		t.Errorf("events = %d", got)
	}
}

// A loop started on a run that is over only gives up its credentials.
func TestALoopOnAFinishedRunOnlyScrubs(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	env := h.seedLive(t, "r1", "loop", &event.RunCompleted{})
	h.writeFile(t, env, "/apothecary/provider.env", "KEY=secret")
	before := len(h.events(t, "r1"))
	h.e.mu.Lock()
	h.e.startLocked("r1")
	h.e.startLocked("r1") // a second start of a live loop is a no-op
	h.e.mu.Unlock()
	h.e.Wait("r1")
	if got := len(h.events(t, "r1")); got != before {
		t.Errorf("a finished run was written to: %v", eventTypes(h.events(t, "r1")))
	}
	if !hasScript(h.execScripts(), "rm -f") {
		t.Errorf("no scrub among %v", h.execScripts())
	}
}

// A verdict rejected with no route for it abandons the run, saying what the
// reviewer said rather than calling it a failure.
func TestARejectionWithNoRouteAbandonsWithItsWords(t *testing.T) {
	doc := `name: loop
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: review, type: agent, harness: $harness, model: $model, agent: reviewer, output: { verdict: { to: artifacts/verdict.json, type: verdict } } }
  - { id: never, type: command, run: "./never.sh" }
`
	h := newHarness(t, mustFlow(t, doc))
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		h.writeFile(t, call.Env, "artifacts/verdict.json", `{"verdict":"REJECT","comments":["the tests are empty"]}`)
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	evs := h.events(t, id)
	ab, _ := findPayload[*event.RunAbandoned](evs)
	if ab == nil || !strings.Contains(ab.Reason, "was rejected and declares no on_reject") || !strings.Contains(ab.Reason, "the tests are empty") {
		t.Fatalf("abandoned = %+v: %v", ab, eventTypes(evs))
	}
	if strings.Contains(ab.Reason, "unrecoverable") {
		t.Errorf("a person's no read as a breakage: %q", ab.Reason)
	}
}

func TestChainMax(t *testing.T) {
	two := &flow.Edge{Max: 2, Then: &flow.Edge{Max: flow.UnlimitedMax}}
	if got := chainMax(two); got != "2+∞" {
		t.Errorf("chainMax = %q", got)
	}
	if got := chainMax(&flow.Edge{Max: 5}); got != "5" {
		t.Errorf("chainMax = %q", got)
	}
}

// A crash between a verdict stage completing and its routing: the routing
// re-reads the verdict, and one that is no longer there or cannot be read is
// the stage's failure, not a silent pass.
func TestRecoveredVerdictThatCannotBeRead(t *testing.T) {
	doc := `name: loop
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: review, type: agent, harness: $harness, model: $model, agent: reviewer, output: { verdict: { to: artifacts/verdict.json, type: verdict } }, on_fail: abort }
  - { id: never, type: command, run: "./never.sh" }
`
	seed := func(t *testing.T) *harness {
		h := newHarness(t, mustFlow(t, doc))
		h.seedLive(t, "r1", "loop", &event.StageStarted{StageID: "review", Iteration: 1, Harness: "fake", Model: "fake/model"},
			&event.StageCompleted{StageID: "review", TurnsUsed: 1})
		return h
	}
	t.Run("missing", func(t *testing.T) {
		h := seed(t)
		v := recoverAndSettle(t, h, "r1")
		sf, _ := findPayload[*event.StageFailed](h.events(t, "r1"))
		if sf == nil || sf.Reason != event.ReasonOutputContract || !strings.Contains(sf.Error, "artifacts/verdict.json missing") {
			t.Fatalf("stage_failed = %+v", sf)
		}
		if v.State != run.Abandoned {
			t.Errorf("state = %s", v.State)
		}
		if hasScript(h.execScripts(), "./never.sh") {
			t.Error("the chain went on past an unread verdict")
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		h := seed(t)
		h.res.Envp = readRefusing{h.envp}
		recoverAndSettle(t, h, "r1")
		sf, _ := findPayload[*event.StageFailed](h.events(t, "r1"))
		if sf == nil || !strings.Contains(sf.Error, "permission denied") {
			t.Fatalf("stage_failed = %+v", sf)
		}
	})
}

// readRefusing is a workspace whose files cannot be read, for another reason
// than their not being there.
type readRefusing struct{ *fake.EnvProvisioner }

func (readRefusing) ReadFile(context.Context, envport.EnvID, string) ([]byte, error) {
	return nil, errors.New("permission denied")
}

// flakyStore refuses one write of an event type, then lets everything by.
type flakyStore struct {
	event.Store
	typ  event.Type
	left atomic.Int32
}

func (s *flakyStore) Append(ctx context.Context, runID string, ps ...event.Payload) ([]event.Event, error) {
	for _, p := range ps {
		if p.EventType() == s.typ && s.left.Add(-1) >= 0 {
			return nil, errors.New("db down")
		}
	}
	return s.Store.Append(ctx, runID, ps...)
}

// An answer whose write failed tells the human so, and its claim rolls back:
// the recovered run takes the same answer.
func TestAnAnswerThatCouldNotBeRecordedCanBeGivenAgain(t *testing.T) {
	store := &flakyStore{typ: event.TypePingAnswered}
	store.left.Store(1)
	doc := `name: loop
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: gate, type: human_gate, question: "ok?" }
  - { id: after, type: command, run: "./after.sh", on_success: done }
`
	h := newHarnessOn(t, func(m *event.MemStore) event.Store { store.Store = m; return store }, mustFlow(t, doc))
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	p := h.waitPing(t, event.PingGate)
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve"}); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("answer = %v", err)
	}
	if _, ok := findPayload[*event.PingAnswered](h.events(t, id)); ok {
		t.Fatal("a refused write reached the log")
	}
	// The loop that could not write is gone; recovery re-arms the gate.
	h.e.Wait(id)
	if err := h.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the gate re-armed", func() bool { return h.e.GetPing(p.ID) != nil })
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatalf("the second answer: %v", err)
	}
	h.e.Wait(id)
	if s := h.view(t, id).State; s != run.Completed {
		t.Errorf("state = %s: %v", s, eventTypes(h.events(t, id)))
	}
}
