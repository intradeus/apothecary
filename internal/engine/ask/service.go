package ask

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"apothecary/internal/engine/modelcheck"
	"apothecary/internal/engine/redact"
	"apothecary/internal/flow"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/platform/logs"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"

	"github.com/oklog/ulid/v2"
)

// These mirror the engine's own limits: a question is held to the size of a
// message to a run's agent, and an answer's text is streamed at the pace of
// a stage's live transcript.
const (
	maxMessageLen = 32 << 10
	deltaEvery    = 120 * time.Millisecond
)

// Engine is what the pane reads from the engine: the flow a run is pinned
// to, and the run's bearer token (scrubbed from answers like any secret).
type Engine interface {
	RunFlow(ctx context.Context, v run.View) (*flow.Flow, error)
	RunToken(runID string) string
}

// Service runs the Ask panes of every run.
type Service struct {
	Runs     *run.Reader
	Resolver resolution.Resolver
	Engine   Engine
	Store    Store
	// NewID is nil for a ULID; Log nil for slog.Default.
	NewID func() string
	Log   *slog.Logger

	// mu guards asks, every askRun field but its own locks, and every
	// askThread field.
	mu   sync.Mutex
	asks map[string]*askRun
	hub  askHub
}

func New(runs *run.Reader, resolver resolution.Resolver, engine Engine, store Store) *Service {
	return &Service{Runs: runs, Resolver: resolver, Engine: engine, Store: store, asks: map[string]*askRun{}}
}

func (svc *Service) newID() string {
	if svc.NewID != nil {
		return svc.NewID()
	}
	return ulid.Make().String()
}

func (svc *Service) log() *slog.Logger {
	if svc.Log != nil {
		return svc.Log
	}
	return slog.Default()
}

// THE ASK PANE SERVICE (FLOWLANG §1 ask): a run's read-only sessions beside
// its stages, answering questions about the code as it stands. Not a stage —
// it runs in parallel with whatever stage is working, on the harness the
// flow's ask: section names, as the agent the engine prepared under the
// reserved name. A run holds any number of threads (the pane calls them
// sessions): each persists with the run and remembers its own harness
// session and model, picked in the pane, and has at most one answer in
// flight. Their spend is the conversation's, never a run budget's. Nothing
// of it is a run event.

var (
	// ErrOff: the pane is off for this run (no ask: section, no
	// workspace, a harness that cannot promise a read-only session).
	ErrOff = errors.New("the Ask pane is off for this run")
	// ErrBusy: an answer is being written in this thread; one question at a
	// time per thread.
	ErrBusy = errors.New("an answer is being written in this session; wait for it")
)

// askAnswerTimeout bounds one answer.
const askAnswerTimeout = 15 * time.Minute

// AskState is the pane as the run page shows it.
type AskState struct {
	Enabled bool `json:"enabled"`
	// Reason says why the pane is off, when it is.
	Reason  string `json:"reason,omitempty"`
	Harness string `json:"harness,omitempty"`
	// Models is what the harness can reach (empty: unknown — any model may
	// be named).
	Models []string `json:"models,omitempty"`
	// Threads are the run's conversations, oldest first.
	Threads []ThreadState `json:"threads"`
	// Messages are every thread's, in the run's seq order.
	Messages []Message `json:"messages"`
}

// ThreadState is one thread as the pane shows it.
type ThreadState struct {
	ID string `json:"id"`
	// SessionID is the harness session the thread continues (empty until
	// its first answer).
	SessionID string `json:"session_id,omitempty"`
	// Model is what the thread's next answer runs on.
	Model     string    `json:"model,omitempty"`
	Busy      bool      `json:"busy"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AskFrame is one event of the pane's stream, about one of the run's
// threads: a message landing, an answer's text so far (delta), the thread's
// state changing, or a thread opened.
type AskFrame struct {
	Kind     string       `json:"kind"` // message|delta|state|thread
	ThreadID string       `json:"thread_id,omitempty"`
	Message  *Message     `json:"message,omitempty"`
	Thread   *ThreadState `json:"thread,omitempty"` // thread: the one opened
	Text     string       `json:"text,omitempty"`   // delta: the answer SO FAR (replace, not append)
	Tool     string       `json:"tool,omitempty"`   // delta: what the agent is doing
	Busy     bool         `json:"busy,omitempty"`
	Model    string       `json:"model,omitempty"`
}

// askRun is the service's in-memory state for one run's pane.
type askRun struct {
	// models is the harness's reachable list, asked once per run.
	models      []string
	modelsAsked bool
	// threads is each thread's live state, by id.
	threads map[string]*askThread
	// appends serializes the run's message appends across its threads: the
	// store numbers a run's messages as one sequence.
	appends sync.Mutex
	// creating serializes finding (or opening) the run's latest thread, so
	// two first questions open one thread, not two.
	creating sync.Mutex
}

// askThread is the service's in-memory state for one thread.
type askThread struct {
	busy   bool
	cancel context.CancelFunc
	// sessionModel is the model the thread's live session was last put on.
	sessionModel string
}

// thread is the live state of the run's thread id; svc.mu must be held.
func (a *askRun) thread(id string) *askThread {
	t := a.threads[id]
	if t == nil {
		t = &askThread{}
		if a.threads == nil {
			a.threads = map[string]*askThread{}
		}
		a.threads[id] = t
	}
	return t
}

type askHub struct {
	mu   sync.Mutex
	subs map[string]map[chan AskFrame]struct{}
}

func (h *askHub) publish(runID string, f AskFrame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[runID] {
		select {
		case ch <- f:
		default: // a slow viewer drops frames, never stalls an answer
		}
	}
}

func (h *askHub) subscribe(runID string) (<-chan AskFrame, func()) {
	ch := make(chan AskFrame, 256)
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[string]map[chan AskFrame]struct{}{}
	}
	if h.subs[runID] == nil {
		h.subs[runID] = map[chan AskFrame]struct{}{}
	}
	h.subs[runID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[runID], ch)
		if len(h.subs[runID]) == 0 {
			delete(h.subs, runID)
		}
		h.mu.Unlock()
	}
}

// askSetup is everything an Ask call needs about a run: its view, its flow,
// its project's resolution — and why the pane is off, if it is.
type askSetup struct {
	v       run.View
	fl      *flow.Flow
	res     *resolution.Resolution
	harness string
	reason  string
}

func (s *askSetup) release() {
	if s != nil && s.res != nil {
		s.res.Close()
	}
}

func (svc *Service) setup(ctx context.Context, runID string) (*askSetup, error) {
	v, err := svc.Runs.View(ctx, runID)
	if err != nil {
		return nil, err
	}
	fl, err := svc.Engine.RunFlow(ctx, v)
	if err != nil {
		return nil, fmt.Errorf("run %s: its flow cannot be loaded: %w", runID, err)
	}
	s := &askSetup{v: v, fl: fl}
	if fl.Ask == nil {
		s.reason = "This flow has no Ask section (ask: in the flow document)."
		return s, nil
	}
	s.harness = fl.Ask.Harness
	if v.EnvID == "" || v.State == run.Archived {
		s.reason = "The run has no workspace to ask about."
		return s, nil
	}
	res, err := resolution.ForRun(ctx, svc.Resolver, v)
	if err != nil {
		return nil, err
	}
	s.res = res
	if !res.HarnessInSet(s.harness) {
		s.reason = fmt.Sprintf("Ask runs on harness %q, which is not in the project's harness set %v.", s.harness, res.HarnessSet())
		return s, nil
	}
	if rc, ok := res.RunnerFor(s.harness).(agentport.ReadOnlyCapable); ok && !rc.CanReadOnly() {
		s.reason = fmt.Sprintf("Harness %q cannot run a read-only session, and Ask must not change the code. Name another harness in the flow's ask: section.", s.harness)
		return s, nil
	}
	return s, nil
}

func (svc *Service) state(runID string) *askRun {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	a := svc.asks[runID]
	if a == nil {
		a = &askRun{}
		svc.asks[runID] = a
	}
	return a
}

// models is what the pane offers to pick from: the models the harness's
// credentials reach (the provider's list), else what the agent in the
// workspace advertises. Asked once per run; nil when neither can say.
func (svc *Service) models(ctx context.Context, s *askSetup) []string {
	a := svc.state(s.v.RunID)
	svc.mu.Lock()
	if a.modelsAsked {
		models := a.models
		svc.mu.Unlock()
		return models
	}
	svc.mu.Unlock()
	var models []string
	if s.res.Models != nil {
		if got, err := s.res.Models(ctx, s.harness); err == nil && len(got) > 0 {
			models = got
		}
	}
	if models == nil {
		if lister, ok := s.res.RunnerFor(s.harness).(agentport.AgentModelLister); ok {
			actx, cancel := context.WithTimeout(ctx, modelcheck.Timeout)
			got, err := lister.AgentModels(actx, envport.EnvID(s.v.EnvID))
			cancel()
			if err != nil {
				return nil // asked again next time
			}
			models = got
		}
	}
	svc.mu.Lock()
	a.models, a.modelsAsked = models, true
	svc.mu.Unlock()
	return models
}

// askModel is the model the next answer runs on: the thread's pick, else
// the flow's default model when the harness reaches it, else the first the
// harness offers.
func askModel(th Thread, fl *flow.Flow, models []string) string {
	if th.Model != "" {
		return th.Model
	}
	m := fl.Defaults.Model
	if len(models) > 0 && !slices.Contains(models, m) && !modelcheck.Accepts(models, m) {
		return models[0]
	}
	return m
}

// threadState is a thread as the pane shows it: on an off pane, the model
// the thread last ran on; on a live one, the model its next answer runs on.
// Busy is the caller's to fill.
func threadState(th Thread, s *askSetup, models []string) ThreadState {
	ts := ThreadState{ID: th.ID, SessionID: th.SessionID, Model: th.Model, CreatedAt: th.CreatedAt, UpdatedAt: th.UpdatedAt}
	if s.reason == "" {
		ts.Model = askModel(th, s.fl, models)
	}
	return ts
}

// State is the pane for a run: whether it is on (and why not), its threads
// (each with the model it answers with), every thread's conversation so
// far, and the models it could answer with.
func (svc *Service) State(ctx context.Context, runID string) (AskState, error) {
	s, err := svc.setup(ctx, runID)
	if err != nil {
		return AskState{}, err
	}
	defer s.release()
	st := AskState{Enabled: s.reason == "", Reason: s.reason, Harness: s.harness, Threads: []ThreadState{}, Messages: []Message{}}
	msgs, err := svc.Store.AskMessages(ctx, runID, 0)
	if err != nil {
		return AskState{}, err
	}
	if msgs != nil {
		st.Messages = msgs
	}
	ths, err := svc.Store.AskThreads(ctx, runID)
	if err != nil {
		return AskState{}, err
	}
	if st.Enabled {
		st.Models = svc.models(ctx, s)
	}
	svc.mu.Lock()
	a := svc.asks[runID]
	for _, th := range ths {
		ts := threadState(th, s, st.Models)
		if a != nil && a.threads[th.ID] != nil {
			ts.Busy = a.threads[th.ID].busy
		}
		st.Threads = append(st.Threads, ts)
	}
	svc.mu.Unlock()
	return st, nil
}

// Messages lists the conversation after seq (the stream's replay).
func (svc *Service) Messages(ctx context.Context, runID string, afterSeq int64) ([]Message, error) {
	if _, err := svc.Runs.View(ctx, runID); err != nil {
		return nil, err
	}
	return svc.Store.AskMessages(ctx, runID, afterSeq)
}

// Watch streams a run's pane: messages as they land, an answer's text
// as it is written, a thread's state as it changes, threads as they open.
func (svc *Service) Watch(runID string) (<-chan AskFrame, func()) {
	return svc.hub.subscribe(runID)
}

// NewThread opens a thread in the run's pane: no messages, no harness
// session until its first answer, and the model of the run's latest thread.
func (svc *Service) NewThread(ctx context.Context, runID string) (ThreadState, error) {
	s, err := svc.setup(ctx, runID)
	if err != nil {
		return ThreadState{}, err
	}
	defer s.release()
	if s.reason != "" {
		return ThreadState{}, fmt.Errorf("%w: %s", ErrOff, s.reason)
	}
	a := svc.state(runID)
	a.creating.Lock()
	defer a.creating.Unlock()
	ths, err := svc.Store.AskThreads(ctx, runID)
	if err != nil {
		return ThreadState{}, err
	}
	model := ""
	if len(ths) > 0 {
		model = ths[len(ths)-1].Model
	}
	th, err := svc.openThread(ctx, s, model)
	if err != nil {
		return ThreadState{}, err
	}
	return threadState(th, s, svc.models(ctx, s)), nil
}

// openThread stores a new thread of the run and tells the stream; the
// caller holds the run's creating lock.
func (svc *Service) openThread(ctx context.Context, s *askSetup, model string) (Thread, error) {
	runID := s.v.RunID
	th := Thread{RunID: runID, ID: svc.newID(), Model: model}
	if err := svc.Store.SaveAskThread(ctx, th); err != nil {
		return Thread{}, err
	}
	// Read back for the stamps the store gave it.
	saved, ok, err := svc.Store.AskThread(ctx, runID, th.ID)
	if err != nil {
		return Thread{}, err
	}
	if !ok {
		return Thread{}, fmt.Errorf("ask session %s of run %s is gone right after it was saved", th.ID, runID)
	}
	ts := threadState(saved, s, svc.models(ctx, s))
	svc.hub.publish(runID, AskFrame{Kind: "thread", ThreadID: saved.ID, Thread: &ts})
	return saved, nil
}

// thread is the run's thread the caller names, or — none named — its
// latest, opened when the run has none yet.
func (svc *Service) thread(ctx context.Context, s *askSetup, a *askRun, threadID string) (Thread, error) {
	runID := s.v.RunID
	if threadID != "" {
		th, ok, err := svc.Store.AskThread(ctx, runID, threadID)
		if err != nil {
			return Thread{}, err
		}
		if !ok {
			return Thread{}, fmt.Errorf("ask session %q of run %s not found", threadID, runID)
		}
		return th, nil
	}
	a.creating.Lock()
	defer a.creating.Unlock()
	ths, err := svc.Store.AskThreads(ctx, runID)
	if err != nil {
		return Thread{}, err
	}
	if len(ths) > 0 {
		return ths[len(ths)-1], nil
	}
	return svc.openThread(ctx, s, "")
}

// record appends a message, one at a time per run: the store numbers a
// run's messages as one sequence across its threads.
func (svc *Service) record(ctx context.Context, a *askRun, m Message) (Message, error) {
	a.appends.Lock()
	defer a.appends.Unlock()
	return svc.Store.AppendAskMessage(ctx, m)
}

// Ask records a question in a thread (threadID; empty: the run's latest,
// opened when there is none) and starts its answer; the question is
// returned at once and the answer arrives on the stream (and in the next
// State).
func (svc *Service) Ask(ctx context.Context, runID, threadID, userID, text string) (Message, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Message{}, errors.New("the question is empty")
	}
	if len(text) > maxMessageLen {
		return Message{}, fmt.Errorf("the question is longer than %d bytes", maxMessageLen)
	}
	s, err := svc.setup(ctx, runID)
	if err != nil {
		return Message{}, err
	}
	if s.reason != "" {
		s.release()
		return Message{}, fmt.Errorf("%w: %s", ErrOff, s.reason)
	}
	a := svc.state(runID)
	th, err := svc.thread(ctx, s, a, threadID)
	if err != nil {
		s.release()
		return Message{}, err
	}
	svc.mu.Lock()
	t := a.thread(th.ID)
	if t.busy {
		svc.mu.Unlock()
		s.release()
		return Message{}, ErrBusy
	}
	t.busy = true
	svc.mu.Unlock()
	model := askModel(th, s.fl, svc.models(ctx, s))
	q, err := svc.record(ctx, a, Message{RunID: runID, ThreadID: th.ID, ID: svc.newID(), Role: RoleQuestion, Text: event.CleanText(text), UserID: userID})
	if err != nil {
		svc.mu.Lock()
		t.busy = false
		svc.mu.Unlock()
		s.release()
		return Message{}, err
	}
	svc.hub.publish(runID, AskFrame{Kind: "message", ThreadID: th.ID, Message: &q})
	svc.hub.publish(runID, AskFrame{Kind: "state", ThreadID: th.ID, Busy: true, Model: model})
	go svc.answer(s, a, t, th, model, text)
	return q, nil
}

// answer runs one question through its thread's session — opened on the
// thread's first, continued after — and records what came back. Detached
// from the request: the asker may close the page and read the answer later.
func (svc *Service) answer(s *askSetup, a *askRun, t *askThread, th Thread, model, question string) {
	defer s.release()
	runID := s.v.RunID
	ctx, cancel := context.WithTimeout(context.Background(), askAnswerTimeout)
	defer cancel()
	svc.mu.Lock()
	t.cancel = cancel
	svc.mu.Unlock()

	red := redact.ForResolution(s.res, svc.Engine.RunToken(runID))
	runner := s.res.RunnerFor(s.harness)
	var mu sync.Mutex
	var text strings.Builder
	var lastDelta time.Time
	sessionSeen := func(id string) {
		if id == "" || th.SessionID == id {
			return
		}
		th.SessionID = id
		if err := svc.Store.SaveAskThread(ctx, th); err != nil {
			svc.log().Warn("ask: save thread", logs.RunID, runID, "thread", th.ID, "err", err)
		}
	}
	events := func(ev agentport.AgentEvent) {
		switch ev.Kind {
		case agentport.AgentEvSession:
			sessionSeen(ev.Text)
		case agentport.AgentEvMessage:
			mu.Lock()
			text.WriteString(ev.Text)
			due := time.Since(lastDelta) >= deltaEvery
			if due {
				lastDelta = time.Now()
			}
			soFar := text.String()
			mu.Unlock()
			if due {
				svc.hub.publish(runID, AskFrame{Kind: "delta", ThreadID: th.ID, Text: red.Scrub(soFar)})
			}
		case agentport.AgentEvTool:
			if ev.Tool != nil && ev.Tool.Title != "" && ev.Tool.Status != "completed" && ev.Tool.Status != "failed" {
				mu.Lock()
				soFar := text.String()
				mu.Unlock()
				svc.hub.publish(runID, AskFrame{Kind: "delta", ThreadID: th.ID, Text: red.Scrub(soFar), Tool: red.Scrub(ev.Tool.Title)})
			}
		}
	}
	in := agentport.AgentInput{Prompt: question, Events: events, ReadOnly: true}
	spec := agentport.AgentSpec{Agent: agentspec.AskAgent, Harness: s.harness, Model: model}
	var res agentport.AgentResult
	var err error
	if th.SessionID == "" {
		res, err = runner.Run(ctx, envport.EnvID(s.v.EnvID), spec, in)
	} else {
		svc.mu.Lock()
		if t.sessionModel != "" && t.sessionModel != model {
			in.Model = model // switch the live session over first
		}
		svc.mu.Unlock()
		res, err = runner.Resume(ctx, envport.EnvID(s.v.EnvID), agentport.SessionID(th.SessionID), in)
		mu.Lock()
		silent := text.Len() == 0
		mu.Unlock()
		// A session the workspace no longer has (its agent restarted, the
		// container was rebuilt) can never answer again: when the resume
		// failed before saying anything, the question goes to a fresh
		// session instead, once. One that failed mid-answer keeps its error.
		if err != nil && ctx.Err() == nil && silent && res.TurnsUsed == 0 {
			svc.log().Info("ask: the thread's session is gone; starting a fresh one", logs.RunID, runID, "thread", th.ID, "session", th.SessionID, "err", red.Scrub(err.Error()))
			th.SessionID, in.Model = "", ""
			res, err = runner.Run(ctx, envport.EnvID(s.v.EnvID), spec, in)
		}
	}

	// What the answer is recorded with survives the answer's own context.
	sctx, scancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer scancel()
	if res.Session != "" {
		th.SessionID = string(res.Session)
	}
	th.Model = model
	if serr := svc.Store.SaveAskThread(sctx, th); serr != nil {
		svc.log().Warn("ask: save thread", logs.RunID, runID, "thread", th.ID, "err", serr)
	}
	mu.Lock()
	said := strings.TrimSpace(text.String())
	mu.Unlock()
	ans := Message{RunID: runID, ThreadID: th.ID, ID: svc.newID(), Role: RoleAnswer, Text: red.Scrub(said),
		Model: model, CostUSD: res.Usage.CostUSD, Turns: res.TurnsUsed}
	if err != nil {
		ans.Error = red.Scrub(err.Error())
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			ans.Error = fmt.Sprintf("no answer within %s", askAnswerTimeout)
		}
	} else if ans.Text == "" && res.Output != "" {
		ans.Text = red.Scrub(strings.TrimSpace(res.Output))
	}
	ans.Text, ans.Error = event.CleanText(ans.Text), event.CleanText(ans.Error)
	m, aerr := svc.record(sctx, a, ans)
	if aerr != nil {
		svc.log().Error("ask: record answer", logs.RunID, runID, "thread", th.ID, "err", aerr)
		m = ans
	}
	svc.mu.Lock()
	t.busy = false
	t.cancel = nil
	if err == nil || th.SessionID != "" {
		t.sessionModel = model
	}
	svc.mu.Unlock()
	svc.hub.publish(runID, AskFrame{Kind: "message", ThreadID: th.ID, Message: &m})
	svc.hub.publish(runID, AskFrame{Kind: "state", ThreadID: th.ID, Busy: false, Model: model})
}

// SetModel picks the model a thread (threadID; empty: the run's latest,
// opened when there is none) answers with from here on: its live session is
// switched over at its next question.
func (svc *Service) SetModel(ctx context.Context, runID, threadID, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return errors.New("model is required")
	}
	s, err := svc.setup(ctx, runID)
	if err != nil {
		return err
	}
	defer s.release()
	if s.reason != "" {
		return fmt.Errorf("%w: %s", ErrOff, s.reason)
	}
	if models := svc.models(ctx, s); len(models) > 0 && !slices.Contains(models, model) && !modelcheck.Accepts(models, model) {
		msg := fmt.Sprintf("model %q is not available to harness %s", model, s.harness)
		if near := modelcheck.Closest(model, models, 3); len(near) > 0 {
			msg += " (closest: " + strings.Join(near, ", ") + ")"
		}
		return errors.New(msg)
	}
	a := svc.state(runID)
	th, err := svc.thread(ctx, s, a, threadID)
	if err != nil {
		return err
	}
	th.Model = model
	if err := svc.Store.SaveAskThread(ctx, th); err != nil {
		return err
	}
	svc.mu.Lock()
	busy := a.thread(th.ID).busy
	svc.mu.Unlock()
	svc.hub.publish(runID, AskFrame{Kind: "state", ThreadID: th.ID, Busy: busy, Model: model})
	return nil
}
