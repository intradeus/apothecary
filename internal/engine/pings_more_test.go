package engine

// A human's answer has two ways in (the live loop, the dormant log) and any
// number of ways to be wrong. Refusals below are what stand between a
// mistaken click and a log that no longer explains the run.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

// parked seeds a run parked on one question of the given kind.
func (h *harness) parkedOn(t *testing.T, runID, pingID, kind string) {
	t.Helper()
	h.seedLive(t, runID, "loop",
		&event.StageStarted{StageID: "build", Iteration: 1},
		&event.PingRaised{PingID: pingID, Type: event.PingClarification, Question: "which?", AnswerKind: kind, StageID: "build",
			Files: []string{"artifacts/plan.md"},
			Form:  &event.Form{Fields: []event.Field{{ID: AnswerField, Type: event.FieldString, Required: true}}}},
		&event.RunPaused{StageID: "build", Reason: event.PauseWaiting})
}

func TestRaiseQuestionRefusals(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	cases := []struct {
		name string
		q    Question
		want string
	}{
		{"a type nobody reads", Question{Type: "gossip", Question: "?"}, `invalid ask_human ping type "gossip"`},
		{"a path outside the repo", Question{Type: "blocker", Question: "?", Artifacts: []string{"/etc/passwd"}}, `artifact "/etc/passwd" must be a repo-relative path`},
		{"too many artifacts", Question{Type: "blocker", Question: "?", Artifacts: strings.Split("a b c d e f g h i", " ")}, "at most 8 artifacts (got 9)"},
		{"a form it cannot show", Question{Type: "blocker", Question: "?", Fields: []event.Field{{ID: "", Type: event.FieldString}}}, "ask_human:"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := h.e.RaiseQuestion("r1", c.q)
			wantErrText(t, err, c.want)
		})
	}
	// Valid, but with no loop to take it. Blank artifacts are not artifacts.
	_, err := h.e.RaiseQuestion("r1", Question{Type: "blocker", Question: "?", Artifacts: []string{"  "}})
	if !errors.Is(err, ErrRunNotActive) {
		t.Errorf("no loop: %v", err)
	}
}

// A question raised while the run is in no agent stage waits to be read; a
// run ending under it withdraws it rather than leave the caller hanging, and
// that holds for one queued past the mailbox.
func TestRaisingIntoARunThatEnds(t *testing.T) {
	doc := `name: ask
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: slow, type: command, run: "./slow.sh", on_success: done }
`
	h := newHarness(t, mustFlow(t, doc))
	release := make(chan struct{})
	running := make(chan struct{}, 1)
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "./slow.sh") {
			running <- struct{}{}
			<-release
		}
		return envport.ExecResult{}, nil
	}
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	<-running
	const raises = 10 // the mailbox holds 8
	var wg sync.WaitGroup
	errs := make(chan error, raises)
	for i := range raises {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.e.RaisePing(id, "blocker", fmt.Sprintf("q%d?", i), "", nil, nil)
			errs <- err
		}()
	}
	waitUntil(t, "the questions to queue", func() bool {
		h.e.mu.Lock()
		defer h.e.mu.Unlock()
		return len(h.e.pings) == raises
	})
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrRunNotActive) {
			t.Errorf("raise = %v", err)
		}
	}
	h.e.Wait(id)
	if got := h.e.PendingPings(); len(got) != 0 {
		t.Errorf("withdrawn questions still pending: %+v", got)
	}
	if countType(h.events(t, id), event.TypePingRaised) != 0 {
		t.Error("a question no agent stage read reached the log")
	}
}

func TestAnsweringAParkedRun(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	if err := h.e.AnswerPing("ghost", textAnswer("x")); !errors.Is(err, ErrPingNotFound) {
		t.Errorf("unknown ping: %v", err)
	}

	h.parkedOn(t, "yn", "p-yn", event.AnswerYesNo)
	wantErrText(t, h.e.AnswerPing("p-yn", PingAnswer{Answer: "maybe"}), "this question is answered approve|reject")
	if countType(h.events(t, "yn"), event.TypePingAnswered) != 0 {
		t.Error("a refused answer reached the log")
	}

	h.parkedOn(t, "rv", "p-rv", event.AnswerYesNo)
	err := h.e.AnswerPing("p-rv", PingAnswer{Answer: "approve", Review: []event.ReviewComment{{Path: "not/shown.md", Body: "x"}}})
	wantErrText(t, err, "not/shown.md")
}

// An answer to a run the human already moved on from: terminal, or its
// question answered, is refused, and an answer that cannot wake it (a pause)
// is recorded and waits for play.
func TestAnsweringPastTheQuestion(t *testing.T) {
	ctx := context.Background()
	t.Run("the run ended without it", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.parkedOn(t, "r1", "p1", event.AnswerForm)
		h.seed(t, "r1", &event.RunAbandoned{Reason: "x"})
		if err := h.e.AnswerPing("p1", textAnswer("x")); err == nil {
			t.Error("an ended run's question took an answer")
		}
	})
	t.Run("answered already", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		idx := h.e
		h.parkedOn(t, "r1", "p1", event.AnswerForm)
		h.e.pingIndex = fixedPings{v: run.PingView{PingID: "p1", RunID: "r1", Status: "answered"}}
		if err := idx.AnswerPing("p1", textAnswer("x")); !errors.Is(err, ErrPingAnswered) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("withdrawn", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.e.pingIndex = fixedPings{v: run.PingView{PingID: "p1", RunID: "r1", Status: "withdrawn"}}
		if err := h.e.AnswerPing("p1", textAnswer("x")); !errors.Is(err, ErrRunNotActive) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a pause holds it back", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.parkedOn(t, "r1", "p1", event.AnswerForm)
		h.seed(t, "r1", &event.RunPaused{StageID: "build", Reason: event.PauseUser})
		if err := h.e.AnswerPing("p1", textAnswer("x")); err != nil {
			t.Fatal(err)
		}
		evs := h.events(t, "r1")
		if countType(evs, event.TypePingAnswered) != 1 || countType(evs, event.TypeRunUnpaused) != 0 {
			t.Errorf("events = %v", eventTypes(evs))
		}
		if h.e.active("r1") {
			t.Error("an answer woke a paused run")
		}
		// Abort it to end the test: the paused run has no loop.
		if err := h.e.Play(ctx, "r1"); err != nil {
			t.Fatal(err)
		}
		h.e.Wait("r1")
	})
	t.Run("the first of two questions wakes nothing", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.parkedOn(t, "r1", "p1", event.AnswerForm)
		h.seed(t, "r1", &event.PingRaised{PingID: "p2", Type: event.PingClarification, Question: "and?", AnswerKind: event.AnswerForm, StageID: "build",
			Form: &event.Form{Fields: []event.Field{{ID: AnswerField, Type: event.FieldString, Required: true}}}})
		if err := h.e.AnswerPing("p1", textAnswer("x")); err != nil {
			t.Fatal(err)
		}
		if countType(h.events(t, "r1"), event.TypeRunUnpaused) != 0 || h.e.active("r1") {
			t.Errorf("woke with a question open: %v", eventTypes(h.events(t, "r1")))
		}
	})
}

// A run mid-admission (no loop, not parked) is owed a loop: an answer does
// not write behind it, and the human hears "busy" once the settle runs out.
func TestAnAnswerToARunMidAdmissionIsRefusedAsBusy(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.seedLive(t, "r1", "loop",
		&event.PingRaised{PingID: "p1", Type: event.PingClarification, Question: "?", AnswerKind: event.AnswerForm,
			Form: &event.Form{Fields: []event.Field{{ID: AnswerField, Type: event.FieldString, Required: true}}}})
	var ticks int
	h.e.clock = func() time.Time {
		ticks++
		return time.Unix(0, 0).Add(time.Duration(ticks) * answerSettle)
	}
	wantErrText(t, h.e.AnswerPing("p1", textAnswer("x")), "run is busy; the answer was not accepted")
	if countType(h.events(t, "r1"), event.TypePingAnswered) != 0 {
		t.Error("an answer was written behind a loop that is owed")
	}
}

// fixedPings is a ping projection holding one view.
type fixedPings struct {
	v   run.PingView
	err error
}

func (f fixedPings) GetPing(context.Context, string) (run.PingView, error) { return f.v, f.err }
func (f fixedPings) ListPings(_ context.Context, _, status string, _ int) ([]run.PingView, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.v.Status == status {
		return []run.PingView{f.v}, nil
	}
	return nil, nil
}

// The inbox reads the projection when there is one, and never dies with it.
func TestPendingPingsFromTheProjection(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.e.pingIndex = fixedPings{v: run.PingView{PingID: "p1", RunID: "r1", Status: "pending", Question: "still there?"}}
	got := h.e.PendingPings()
	if len(got) != 1 || got[0].ID != "p1" || got[0].Question != "still there?" {
		t.Errorf("pending = %+v", got)
	}
	h.e.pingIndex = fixedPings{err: errors.New("db down")}
	if got := h.e.PendingPings(); len(got) != 0 {
		t.Errorf("pending = %+v", got)
	}
	if err := h.e.AnswerPing("p1", textAnswer("x")); !errors.Is(err, ErrPingNotFound) {
		t.Errorf("a projection that failed reads as not found: %v", err)
	}
}

func TestPingFoldSurvivesAStoreFailure(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.parkedOn(t, "r1", "p1", event.AnswerForm)
	store := &eventsDown{Store: h.store}
	store.down.Store(true)
	h.e.store = store
	if got := h.e.PendingPings(); len(got) != 0 {
		t.Errorf("pending = %+v", got)
	}
	h.e.store = failingViews{Store: h.store, err: errors.New("db down")}
	if got := h.e.PendingPings(); len(got) != 0 {
		t.Errorf("pending = %+v", got)
	}
	h.e.store = h.store
}

func TestPingAccessors(t *testing.T) {
	p := &Ping{Files: []string{"a.md", "b.md"}, done: make(chan struct{})}
	if got := p.Primary(); got != "a.md" {
		t.Errorf("primary = %q", got)
	}
	if got := (&Ping{Link: "https://x", Files: []string{"a.md"}}).Primary(); got != "https://x" {
		t.Errorf("primary = %q", got)
	}
	if got := (&Ping{}).Primary(); got != "" {
		t.Errorf("primary = %q", got)
	}
	if _, ok := p.Answered(); ok {
		t.Error("an open ping answered")
	}
	if got := ComposeAnswer(p); got != "" {
		t.Errorf("compose of an open ping = %q", got)
	}
	if got := p.DeliveredArtifacts(); len(got) != 0 {
		t.Errorf("delivered = %v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := p.Await(ctx); ok {
		t.Error("await of an open ping returned an answer")
	}
	p.answer = &PingAnswer{Answer: "approve", Feedback: "looks right"}
	close(p.done)
	if got := ComposeAnswer(p); got != "approve\n(note: looks right)" {
		t.Errorf("compose = %q", got)
	}
	p2 := &Ping{answer: &PingAnswer{Answer: "reject"}, done: make(chan struct{})}
	close(p2.done)
	if got := ComposeAnswer(p2); got != "reject" {
		t.Errorf("compose = %q", got)
	}
}

// Limits come from the store, default when never set, and a broken source
// never bricks admission.
type limitsStore struct {
	runs, envs int
	ok         bool
	err        error
	saved      []int
}

func (s *limitsStore) LoadLimits(context.Context) (int, int, bool, error) {
	return s.runs, s.envs, s.ok, s.err
}
func (s *limitsStore) SaveLimits(_ context.Context, runs, envs int) error {
	s.saved = []int{runs, envs}
	return nil
}

func TestStoredLimits(t *testing.T) {
	ctx := context.Background()
	s := &limitsStore{}
	l, err := StoredLimits{s}.Limits(ctx)
	if err != nil || l != (Limits{}).withDefaults() {
		t.Errorf("unset limits = %+v, %v", l, err)
	}
	s.runs, s.envs, s.ok = 7, Unlimited, true
	if l, _ = (StoredLimits{s}).Limits(ctx); l.MaxConcurrentRuns != 7 || l.MaxEnvironments != Unlimited || l.EnvsCapped() {
		t.Errorf("stored limits = %+v", l)
	}
	s.err = errors.New("db down")
	if _, err := (StoredLimits{s}).Limits(ctx); err == nil {
		t.Error("a broken store went unnoticed")
	}
	for _, bad := range []Limits{{MaxConcurrentRuns: 0, MaxEnvironments: 1}, {MaxConcurrentRuns: 1, MaxEnvironments: -2}} {
		if err := (StoredLimits{s}).Set(ctx, bad); err == nil || !strings.Contains(err.Error(), "must be a positive count or -1") {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if s.saved != nil {
		t.Errorf("a refused limit was saved: %v", s.saved)
	}
	if err := (StoredLimits{s}).Set(ctx, Limits{MaxConcurrentRuns: Unlimited, MaxEnvironments: 3}); err != nil {
		t.Fatal(err)
	}
	if len(s.saved) != 2 || s.saved[0] != Unlimited || s.saved[1] != 3 {
		t.Errorf("saved = %v", s.saved)
	}
}

// A settings source that fails admits on the defaults.
type brokenSettings struct{}

func (brokenSettings) Limits(context.Context) (Limits, error) {
	return Limits{}, errors.New("db down")
}

func TestABrokenSettingsSourceAdmitsOnDefaults(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.e.settings = brokenSettings{}
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	if s := h.view(t, id).State; s != run.Completed {
		t.Errorf("state = %s", s)
	}
}

// A person who took over an agent is the only one who talks to it, and each
// way of not being that person is refused.
func TestMessagesAndHoldsWithNoAgentToTalkTo(t *testing.T) {
	long := strings.Repeat("x", maxMessageLen+1)
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	_, err := h.e.SendMessage("r1", "   ")
	wantErrText(t, err, "message is empty")
	_, err = h.e.SendMessage("r1", long)
	wantErrText(t, err, "longer than")

	if _, err := h.e.SendMessage("ghost", "hello"); err == nil || !strings.Contains(err.Error(), "run ghost not found") {
		t.Errorf("unknown run: %v", err)
	}
	if err := h.e.TakeOver("ghost", "u1"); err == nil || !strings.Contains(err.Error(), "run ghost not found") {
		t.Errorf("take over of an unknown run: %v", err)
	}

	h.seed(t, "over", created(testProject), &event.RunCompleted{})
	if _, err := h.e.SendMessage("over", "hello"); !errors.Is(err, ErrRunNotActive) {
		t.Errorf("a finished run: %v", err)
	}
	if err := h.e.TakeOver("over", "u1"); !errors.Is(err, ErrNoAgentRunning) {
		t.Errorf("take over of a finished run: %v", err)
	}
	h.seed(t, "nohold", created(testProject), &event.RunPaused{Reason: event.PauseWaiting})
	if _, err := h.e.SendMessage("nohold", "hello"); !errors.Is(err, ErrNoAgentRunning) {
		t.Errorf("no hold: %v", err)
	}
	if err := h.e.Release("nohold", "u1"); !errors.Is(err, ErrNoAgentRunning) {
		t.Errorf("release with no hold: %v", err)
	}

	// A live loop with no agent in it: a command stage.
	h2 := newHarness(t, mustFlow(t, `name: loop
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: slow, type: command, run: "./slow.sh", on_success: done }
`))
	release := make(chan struct{})
	started := make(chan struct{})
	h2.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "./slow.sh") {
			close(started)
			<-release
		}
		return envport.ExecResult{}, nil
	}
	id, err := h2.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := h2.e.SendMessage(id, "hello"); !errors.Is(err, ErrNoAgentRunning) {
		t.Errorf("message during a command: %v", err)
	}
	if err := h2.e.TakeOver(id, "u1"); !errors.Is(err, ErrNoAgentRunning) {
		t.Errorf("take over during a command: %v", err)
	}
	if err := h2.e.Release(id, "u1"); !errors.Is(err, ErrNotHeld) {
		t.Errorf("release during a command: %v", err)
	}
	close(release)
	h2.e.Wait(id)
}

// A run parked on its holder: only the holder's next word moves it. Someone
// else taking over is refused, a user's pause holds it, and a run parked on
// its own question is the answer's to wake.
func TestAParkedHold(t *testing.T) {
	spent := &event.HoldSpend{Turns: 2, Messages: 1, Usage: &event.Usage{CostUSD: 0.5}}
	seed := func(t *testing.T, tail ...event.Payload) *harness {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		ps := append([]event.Payload{
			&event.StageStarted{StageID: "build", Iteration: 1},
			&event.HoldTaken{StageID: "build", Iteration: 1, UserID: "u1"},
			&event.StagePaused{StageID: "build", Iteration: 1, Reason: event.PauseHeld, Hold: spent},
			&event.RunPaused{StageID: "build", Reason: event.PauseWaiting},
		}, tail...)
		h.seedLive(t, "r1", "loop", ps...)
		return h
	}
	t.Run("someone else cannot take it", func(t *testing.T) {
		h := seed(t)
		if err := h.e.TakeOver("r1", "u2"); !errors.Is(err, ErrAlreadyHeld) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a release carries the spend of every leg", func(t *testing.T) {
		h := seed(t)
		h.runner.ResumeFunc = func(_ context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
			return agentport.AgentResult{TurnsUsed: 1, Session: call.Session}, nil
		}
		if err := h.e.Release("r1", "u1"); err != nil {
			t.Fatal(err)
		}
		h.e.Wait("r1")
		rel, ok := findPayload[*event.HoldReleased](h.events(t, "r1"))
		if !ok || rel.Turns != 2 || rel.Messages != 1 || rel.Usage == nil || rel.Usage.CostUSD != 0.5 || rel.Reason != event.HoldReleasedByUser {
			t.Fatalf("released = %+v", rel)
		}
	})
	t.Run("a pause holds it", func(t *testing.T) {
		h := seed(t, &event.RunPaused{StageID: "build", Reason: event.PauseUser})
		_, err := h.e.SendMessage("r1", "hello")
		if !errors.Is(err, ErrRunNotActive) || !strings.Contains(err.Error(), "play it first") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("mid-admission it waits", func(t *testing.T) {
		h := seed(t, &event.RunUnpaused{Reason: event.UnpauseHold})
		if _, err := h.e.SendMessage("r1", "hello"); !errors.Is(err, errRunBusy) {
			t.Errorf("err = %v", err)
		}
		if err := h.e.Release("r1", "u1"); !errors.Is(err, errRunBusy) {
			t.Errorf("release = %v", err)
		}
	})
}
