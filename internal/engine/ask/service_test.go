package ask

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

const (
	project = "prj-1"
	runID   = "run-1"
	secret  = "sk-provider-secret-value"
	token   = "run-bearer-token-value"
)

// engine is the two things the pane reads from the real one.
type engine struct {
	fl  *flow.Flow
	err error
}

func (e *engine) RunFlow(context.Context, run.View) (*flow.Flow, error) { return e.fl, e.err }
func (e *engine) RunToken(string) string                                { return token }

// failingStore fails the named Store call; everything else reaches the MemStore.
type failingStore struct {
	*MemStore
	mu      sync.Mutex
	thread  error
	save    error
	append  error
	appends int // AppendAskMessage calls to let through before failing
	list    error
}

func (s *failingStore) set(f func(*failingStore)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func (s *failingStore) AskThread(ctx context.Context, id string) (Thread, bool, error) {
	s.mu.Lock()
	err := s.thread
	s.mu.Unlock()
	if err != nil {
		return Thread{}, false, err
	}
	return s.MemStore.AskThread(ctx, id)
}

func (s *failingStore) SaveAskThread(ctx context.Context, t Thread) error {
	s.mu.Lock()
	err := s.save
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.MemStore.SaveAskThread(ctx, t)
}

func (s *failingStore) AppendAskMessage(ctx context.Context, m Message) (Message, error) {
	s.mu.Lock()
	err := s.append
	if s.appends > 0 {
		s.appends--
		err = nil
	}
	s.mu.Unlock()
	if err != nil {
		return Message{}, err
	}
	return s.MemStore.AppendAskMessage(ctx, m)
}

func (s *failingStore) AskMessages(ctx context.Context, id string, after int64) ([]Message, error) {
	s.mu.Lock()
	err := s.list
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.MemStore.AskMessages(ctx, id, after)
}

// resolver counts what it opened and closed: a question is detached from its
// request, so the resolution must be released when the ANSWER ends.
type resolver struct {
	res    *resolution.Resolution
	err    error
	opened atomic.Int32
	closed atomic.Int32
}

func (r *resolver) Resolve(context.Context, string) (*resolution.Resolution, error) {
	if r.err != nil {
		return nil, r.err
	}
	r.opened.Add(1)
	res := *r.res
	res.Release = func() { r.closed.Add(1) }
	return &res, nil
}

type rig struct {
	store  *failingStore
	events *event.MemStore
	eng    *engine
	res    *resolver
	runner *fake.AgentRunner
	svc    *Service
}

func newRig(t *testing.T) *rig {
	t.Helper()
	events := event.NewMemStore()
	if _, err := events.Append(context.Background(), runID,
		&event.RunCreated{ProjectID: project}, &event.EnvReady{EnvID: "env-1"}); err != nil {
		t.Fatal(err)
	}
	runner := &fake.AgentRunner{}
	res := &resolver{res: &resolution.Resolution{
		ProjectID: project, HarnessKind: "fake", Runner: runner,
		ProviderEnv: map[string]string{"ANTHROPIC_API_KEY": secret},
	}}
	eng := &engine{fl: &flow.Flow{Name: "f", Ask: &flow.Ask{Harness: "fake"}, Defaults: flow.Defaults{Model: "fake/model"}}}
	store := &failingStore{MemStore: NewMemStore()}
	r := &rig{store: store, events: events, eng: eng, res: res, runner: runner}
	r.svc = New(&run.Reader{Store: events}, res, eng, store)
	r.svc.NewID = counter()
	return r
}

func counter() func() string {
	var n atomic.Int32
	return func() string { return fmt.Sprintf("id-%d", n.Add(1)) }
}

// answered waits for the pane to go quiet and returns its messages, then
// checks the detached answer released what it resolved.
func (r *rig) answered(t *testing.T, want int) []Message {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, err := r.svc.State(context.Background(), runID)
		if err == nil && !st.Busy && len(st.Messages) >= want && r.res.opened.Load() == r.res.closed.Load() {
			return st.Messages
		}
		time.Sleep(time.Millisecond)
	}
	st, err := r.svc.State(context.Background(), runID)
	t.Fatalf("the pane never settled on %d messages: %+v, %v (opened %d, closed %d)", want, st, err, r.res.opened.Load(), r.res.closed.Load())
	return nil
}

func TestPaneIsOffAndSaysWhy(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		setup  func(r *rig)
		reason string
		harn   string
	}{
		{"no section", func(r *rig) { r.eng.fl.Ask = nil }, "no Ask section", ""},
		{"archived", func(r *rig) {
			if _, err := r.events.Append(ctx, runID, &event.RunArchived{}); err != nil {
				t.Fatal(err)
			}
		}, "no workspace", "fake"},
		{"harness outside the set", func(r *rig) { r.eng.fl.Ask.Harness = "other" }, `harness "other"`, "other"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			c.setup(r)
			st, err := r.svc.State(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			if st.Enabled || !strings.Contains(st.Reason, c.reason) || st.Harness != c.harn {
				t.Errorf("state = %+v, want off because %q", st, c.reason)
			}
			if st.Messages == nil {
				t.Error("an off pane's messages are null, the API's [] is the contract")
			}
			if _, err := r.svc.Ask(ctx, runID, "u", "why?"); !errors.Is(err, ErrOff) || !strings.Contains(err.Error(), c.reason) {
				t.Errorf("ask = %v", err)
			}
			if err := r.svc.SetModel(ctx, runID, "a/b"); !errors.Is(err, ErrOff) {
				t.Errorf("set model = %v", err)
			}
			if o, cl := r.res.opened.Load(), r.res.closed.Load(); o != cl {
				t.Errorf("resolved %d, released %d", o, cl)
			}
		})
	}
}

func TestPaneWithoutAWorkspaceYet(t *testing.T) {
	r := newRig(t)
	if _, err := r.events.Append(context.Background(), "fresh", &event.RunCreated{ProjectID: project}); err != nil {
		t.Fatal(err)
	}
	st, err := r.svc.State(context.Background(), "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled || !strings.Contains(st.Reason, "no workspace") {
		t.Errorf("state = %+v", st)
	}
}

// A model picked before the pane went off is still shown: the conversation
// ran on it.
func TestAnOffPaneShowsTheThreadsModel(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if err := r.store.SaveAskThread(ctx, Thread{RunID: runID, Model: "acme/picked"}); err != nil {
		t.Fatal(err)
	}
	r.eng.fl.Ask.Harness = "other"
	st, err := r.svc.State(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled || st.Model != "acme/picked" {
		t.Errorf("state = %+v", st)
	}
}

// A failure to read is not "the pane is off": it reaches the caller.
func TestFailuresAreNotOff(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	cases := []struct {
		name string
		arm  func(r *rig)
		want string
	}{
		{"unknown flow", func(r *rig) { r.eng.err = boom }, "its flow cannot be loaded"},
		{"resolver", func(r *rig) { r.res.err = boom }, "boom"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			c.arm(r)
			_, err := r.svc.State(ctx, runID)
			if err == nil || !strings.Contains(err.Error(), c.want) || errors.Is(err, ErrOff) {
				t.Errorf("state = %v", err)
			}
			if _, err := r.svc.Ask(ctx, runID, "u", "q"); err == nil || errors.Is(err, ErrOff) {
				t.Errorf("ask = %v", err)
			}
			if err := r.svc.SetModel(ctx, runID, "a/b"); err == nil || errors.Is(err, ErrOff) {
				t.Errorf("set model = %v", err)
			}
		})
	}
	r := newRig(t)
	if _, err := r.svc.State(ctx, "ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("unknown run = %v", err)
	}
	if _, err := r.svc.Messages(ctx, "ghost", 0); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("messages of an unknown run = %v", err)
	}
}

func TestStateReportsStoreFailures(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("db down")
	r := newRig(t)
	r.store.set(func(s *failingStore) { s.list = boom })
	if _, err := r.svc.State(ctx, runID); !errors.Is(err, boom) {
		t.Errorf("list failure = %v", err)
	}
	r.store.set(func(s *failingStore) { s.list, s.thread = nil, boom })
	if _, err := r.svc.State(ctx, runID); !errors.Is(err, boom) {
		t.Errorf("thread failure = %v", err)
	}
}

func TestAskRefusesAQuestionItWontPass(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if _, err := r.svc.Ask(ctx, runID, "u", " \n\t "); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("blank question = %v", err)
	}
	if _, err := r.svc.Ask(ctx, runID, "u", strings.Repeat("x", maxMessageLen+1)); err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Errorf("long question = %v", err)
	}
	if r.res.opened.Load() != 0 {
		t.Error("a refused question resolved the project")
	}
	// Exactly the limit passes.
	if _, err := r.svc.Ask(ctx, runID, "u", strings.Repeat("x", maxMessageLen)); err != nil {
		t.Errorf("a question at the limit = %v", err)
	}
	r.answered(t, 2)
}

// A question that fails to record leaves the pane free: not stuck busy, and
// the resolution released.
func TestAFailedAskReleasesThePane(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("db down")
	for name, arm := range map[string]func(s *failingStore){
		"thread": func(s *failingStore) { s.thread = boom },
		"append": func(s *failingStore) { s.append = boom },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.store.set(arm)
			if _, err := r.svc.Ask(ctx, runID, "u", "q"); !errors.Is(err, boom) {
				t.Fatalf("ask = %v", err)
			}
			if o, c := r.res.opened.Load(), r.res.closed.Load(); o != c {
				t.Errorf("resolved %d, released %d", o, c)
			}
			r.store.set(func(s *failingStore) { s.thread, s.append = nil, nil })
			if _, err := r.svc.Ask(ctx, runID, "u", "again"); err != nil {
				t.Fatalf("the pane stayed stuck: %v", err)
			}
			r.answered(t, 2)
		})
	}
}

// Answers are the agent's words about the workspace: what it read can hold a
// secret, and nothing the engine injected may reach the pane. That goes for
// the stream as much as for the record.
func TestAnswersAndTheirStreamAreScrubbed(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	frames, stop := r.svc.Watch(runID)
	defer stop()
	r.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Text: "the key is " + secret})
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvTool, Tool: &agentport.AgentTool{Title: "cat " + token, Status: "in_progress"}})
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvTool, Tool: &agentport.AgentTool{Title: "finished", Status: "completed"}})
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvTool, Tool: &agentport.AgentTool{Title: "failed", Status: "failed"}})
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvTool, Tool: &agentport.AgentTool{Status: "in_progress"}})
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvTool})
		return agentport.AgentResult{TurnsUsed: 2, Usage: agentport.Usage{CostUSD: 0.25}}, nil
	}
	if _, err := r.svc.Ask(ctx, runID, "u", "what is the key?"); err != nil {
		t.Fatal(err)
	}
	msgs := r.answered(t, 2)
	a := msgs[1]
	if strings.Contains(a.Text, secret) || !strings.Contains(a.Text, "the key is") {
		t.Errorf("answer = %q", a.Text)
	}
	if a.CostUSD != 0.25 || a.Turns != 2 || a.Model != "fake/model" || a.Role != RoleAnswer {
		t.Errorf("answer = %+v", a)
	}

	var tools []string
	for {
		select {
		case f := <-frames:
			blob := f.Text + f.Tool
			if f.Message != nil {
				blob += f.Message.Text
			}
			if strings.Contains(blob, secret) || strings.Contains(blob, token) {
				t.Errorf("frame leaks: %+v", f)
			}
			if f.Tool != "" {
				tools = append(tools, f.Tool)
			}
			continue
		default:
		}
		break
	}
	if len(tools) != 1 || !strings.HasPrefix(tools[0], "cat ") {
		t.Errorf("tool frames = %q: only a titled tool still running is news", tools)
	}
}

func TestAFailedAnswerRecordsItsScrubbedError(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.runner.RunFunc = func(context.Context, fake.RunCall) (agentport.AgentResult, error) {
		return agentport.AgentResult{}, errors.New("auth failed with " + secret)
	}
	if _, err := r.svc.Ask(ctx, runID, "u", "q"); err != nil {
		t.Fatal(err)
	}
	a := r.answered(t, 2)[1]
	if a.Error == "" || strings.Contains(a.Error, secret) || !strings.Contains(a.Error, "auth failed") {
		t.Errorf("error = %q", a.Error)
	}
	// The pane takes a next question.
	r.runner.RunFunc = nil
	if _, err := r.svc.Ask(ctx, runID, "u", "again"); err != nil {
		t.Fatalf("after a failed answer: %v", err)
	}
	r.answered(t, 4)
}

// An agent that streamed nothing still answers through its final output.
func TestAnswerFallsBackToTheFinalOutput(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.runner.RunFunc = func(context.Context, fake.RunCall) (agentport.AgentResult, error) {
		return agentport.AgentResult{Output: "  final words " + secret + "\n"}, nil
	}
	if _, err := r.svc.Ask(ctx, runID, "u", "q"); err != nil {
		t.Fatal(err)
	}
	a := r.answered(t, 2)[1]
	if !strings.HasPrefix(a.Text, "final words") || strings.Contains(a.Text, secret) {
		t.Errorf("answer = %q", a.Text)
	}
}

// The session id the agent reports mid-answer is saved at once: a crash
// before the answer ends must not lose the thread.
func TestSessionIsSavedAsSoonAsItIsKnown(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	seen := make(chan Thread, 1)
	r.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: ""}) // nothing yet
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "sess-early"})
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "sess-early"})
		th, _, _ := r.store.AskThread(ctx, runID)
		seen <- th
		return agentport.AgentResult{Session: "sess-final"}, nil
	}
	if _, err := r.svc.Ask(ctx, runID, "u", "q"); err != nil {
		t.Fatal(err)
	}
	r.answered(t, 2)
	if th := <-seen; th.SessionID != "sess-early" {
		t.Errorf("mid-answer thread = %+v", th)
	}
	th, ok, err := r.store.AskThread(ctx, runID)
	if err != nil || !ok || th.SessionID != "sess-final" || th.Model != "fake/model" {
		t.Errorf("thread = %+v, %v, %v", th, ok, err)
	}
}

// Failing to save the thread loses the session, not the answer.
func TestAnAnswerSurvivesAThreadItCannotSave(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.store.set(func(s *failingStore) { s.save = errors.New("db down") })
	r.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s1"})
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Text: "answer"})
		return agentport.AgentResult{}, nil
	}
	if _, err := r.svc.Ask(ctx, runID, "u", "q"); err != nil {
		t.Fatal(err)
	}
	if a := r.answered(t, 2)[1]; a.Text != "answer" {
		t.Errorf("answer = %+v", a)
	}
}

// An answer that cannot be recorded still reaches whoever is watching, and
// does not leave the pane busy.
func TestAnAnswerItCannotRecordIsStillDelivered(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.store.set(func(s *failingStore) { s.append, s.appends = errors.New("db down"), 1 })
	r.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Text: "lost to the store"})
		return agentport.AgentResult{}, nil
	}
	frames, stop := r.svc.Watch(runID)
	defer stop()
	if _, err := r.svc.Ask(ctx, runID, "u", "q"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	var got *Message
	for got == nil || got.Role != RoleAnswer {
		select {
		case f := <-frames:
			if f.Kind == "message" {
				got = f.Message
			}
		case <-deadline:
			t.Fatal("no answer frame")
		}
	}
	if got.Text != "lost to the store" {
		t.Errorf("answer = %+v", got)
	}
	r.answered(t, 1)
}

// The pane's session is switched over only when the model really changed.
func TestASameModelResumeDoesNotSwitch(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	var mu sync.Mutex
	var models []string
	r.runner.ResumeFunc = func(_ context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
		mu.Lock()
		defer mu.Unlock()
		models = append(models, call.In.Model)
		return agentport.AgentResult{Session: call.Session}, nil
	}
	ask := func(n int) {
		t.Helper()
		if _, err := r.svc.Ask(ctx, runID, "u", "q"); err != nil {
			t.Fatal(err)
		}
		r.answered(t, n)
	}
	ask(2) // opens the session
	ask(4) // same model: nothing to switch
	if err := r.svc.SetModel(ctx, runID, "acme/other"); err != nil {
		t.Fatal(err)
	}
	ask(6) // switched
	ask(8) // already there
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"", "acme/other", ""}; !reflect.DeepEqual(models, want) {
		t.Errorf("resume models = %q, want %q", models, want)
	}
}

func TestAskModel(t *testing.T) {
	fl := &flow.Flow{Defaults: flow.Defaults{Model: "acme/default"}}
	cases := []struct {
		name   string
		thread string
		models []string
		want   string
	}{
		{"the thread's pick wins", "acme/picked", []string{"acme/other"}, "acme/picked"},
		{"the flow's default, nothing listed", "", nil, "acme/default"},
		{"the flow's default, listed", "", []string{"acme/other", "acme/default"}, "acme/default"},
		{"the first offered, default out of reach", "", []string{"acme/first", "acme/second"}, "acme/first"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := askModel(Thread{Model: c.thread}, fl, c.models); got != c.want {
				t.Errorf("askModel = %q, want %q", got, c.want)
			}
		})
	}
}

// listingRunner is a harness that can say what it accepts from inside the
// workspace.
type listingRunner struct {
	*fake.AgentRunner
	mu     sync.Mutex
	calls  int
	models []string
	err    error
}

func (l *listingRunner) AgentModels(context.Context, envport.EnvID) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	return l.models, l.err
}

func TestModelsComeFromTheProviderThenTheAgentAndAreAskedOnce(t *testing.T) {
	ctx := context.Background()
	t.Run("provider list wins", func(t *testing.T) {
		r := newRig(t)
		list := &listingRunner{AgentRunner: r.runner, models: []string{"acme/agent"}}
		r.res.res.Runner = list
		var asked atomic.Int32
		r.res.res.Models = func(context.Context, string) ([]string, error) {
			asked.Add(1)
			return []string{"acme/one", "fake/model"}, nil
		}
		for range 3 {
			st, err := r.svc.State(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(st.Models, []string{"acme/one", "fake/model"}) || st.Model != "fake/model" {
				t.Fatalf("state = %+v", st)
			}
		}
		if asked.Load() != 1 || list.calls != 0 {
			t.Errorf("provider asked %d times, agent %d", asked.Load(), list.calls)
		}
	})
	t.Run("provider failure falls back to the agent", func(t *testing.T) {
		r := newRig(t)
		list := &listingRunner{AgentRunner: r.runner, models: []string{"acme/agent"}}
		r.res.res.Runner = list
		r.res.res.Models = func(context.Context, string) ([]string, error) { return nil, errors.New("no list") }
		st, err := r.svc.State(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(st.Models, []string{"acme/agent"}) || st.Model != "acme/agent" {
			t.Errorf("state = %+v", st)
		}
	})
	t.Run("an agent that cannot say yet is asked again", func(t *testing.T) {
		r := newRig(t)
		list := &listingRunner{AgentRunner: r.runner, err: errors.New("agent still starting")}
		r.res.res.Runner = list
		st, err := r.svc.State(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if st.Models != nil || st.Model != "fake/model" {
			t.Errorf("state = %+v", st)
		}
		list.mu.Lock()
		list.err, list.models = nil, []string{"acme/late"}
		list.mu.Unlock()
		if st, err = r.svc.State(ctx, runID); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(st.Models, []string{"acme/late"}) {
			t.Errorf("state = %+v", st)
		}
		if list.calls != 2 {
			t.Errorf("agent asked %d times", list.calls)
		}
	})
}

func TestSetModel(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.res.res.Models = func(context.Context, string) ([]string, error) {
		return []string{"acme/alpha-large", "acme/alpha-small", "fake/model"}, nil
	}
	if err := r.svc.SetModel(ctx, runID, "   "); err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("blank = %v", err)
	}
	err := r.svc.SetModel(ctx, runID, "acme/alpha")
	if err == nil || !strings.Contains(err.Error(), `model "acme/alpha" is not available to harness fake`) || !strings.Contains(err.Error(), "closest: acme/alpha-") {
		t.Errorf("unreachable model = %v", err)
	}
	err = r.svc.SetModel(ctx, runID, "zzzzzz")
	if err == nil || strings.Contains(err.Error(), "closest") {
		t.Errorf("nothing near = %v", err)
	}
	if th, ok, _ := r.store.AskThread(ctx, runID); ok {
		t.Errorf("a refused model was saved: %+v", th)
	}

	frames, stop := r.svc.Watch(runID)
	defer stop()
	if err := r.svc.SetModel(ctx, runID, " acme/alpha-small "); err != nil {
		t.Fatal(err)
	}
	th, _, _ := r.store.AskThread(ctx, runID)
	if th.Model != "acme/alpha-small" || th.RunID != runID {
		t.Errorf("thread = %+v", th)
	}
	select {
	case f := <-frames:
		if f.Kind != "state" || f.Model != "acme/alpha-small" || f.Busy {
			t.Errorf("frame = %+v", f)
		}
	default:
		t.Error("no state frame")
	}
	if st, _ := r.svc.State(ctx, runID); st.Model != "acme/alpha-small" {
		t.Errorf("state model = %q", st.Model)
	}

	boom := errors.New("db down")
	r.store.set(func(s *failingStore) { s.save = boom })
	if err := r.svc.SetModel(ctx, runID, "fake/model"); !errors.Is(err, boom) {
		t.Errorf("save failure = %v", err)
	}
	r.store.set(func(s *failingStore) { s.save, s.thread = nil, boom })
	if err := r.svc.SetModel(ctx, runID, "fake/model"); !errors.Is(err, boom) {
		t.Errorf("thread failure = %v", err)
	}
}

func TestMessagesAfterASeq(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	for i := range 3 {
		if _, err := r.store.AppendAskMessage(ctx, Message{RunID: runID, ID: fmt.Sprint(i), Role: RoleQuestion}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.svc.Messages(ctx, runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Seq != 2 || got[1].Seq != 3 || got[0].At.IsZero() {
		t.Errorf("messages = %+v", got)
	}
}

// A viewer that stops reading loses frames, and an answer never waits on it.
func TestASlowViewerNeverStallsTheHub(t *testing.T) {
	var h askHub
	_, stopSlow := h.subscribe("r")
	defer stopSlow()
	fast, stopFast := h.subscribe("r")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 { // far past a subscriber's buffer
			h.publish("r", AskFrame{Kind: "delta"})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publish blocked on a viewer that stopped reading")
	}
	if len(fast) == 0 {
		t.Error("nothing delivered")
	}
	stopFast()
	h.mu.Lock()
	left := len(h.subs["r"])
	h.mu.Unlock()
	if left != 1 {
		t.Errorf("subscribers left = %d", left)
	}
	stopSlow()
	h.mu.Lock()
	_, gone := h.subs["r"]
	h.mu.Unlock()
	if gone {
		t.Error("the run's subscriber map outlived its last viewer")
	}
	h.publish("nobody", AskFrame{Kind: "delta"}) // no one watching: a no-op
}

func TestDefaultIDsAreUniqueAndTheDefaultLogIsUsable(t *testing.T) {
	svc := New(nil, nil, nil, nil)
	if a, b := svc.newID(), svc.newID(); a == "" || a == b {
		t.Errorf("ids %q, %q", a, b)
	}
	if svc.log() == nil {
		t.Error("no default log")
	}
}
