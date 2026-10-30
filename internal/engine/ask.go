package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"apothecary/internal/agentspec"
	askstore "apothecary/internal/ask"
	"apothecary/internal/flow"
	"apothecary/internal/modelcheck"
	"apothecary/internal/ports"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
)

// THE ASK PANE (FLOWLANG §1 ask): a run's read-only session beside its
// stages, answering questions about the code as it stands. Not a stage —
// it runs in parallel with whatever stage is working, on the harness the
// flow's ask: section names, as the agent the engine prepared under the
// reserved name. Its session persists with the run (the thread remembers
// it), its model is picked in the pane, and its spend is the
// conversation's, never a run budget's. Nothing of it is a run event.

var (
	// ErrAskOff: the pane is off for this run (no ask: section, no
	// workspace, a harness that cannot promise a read-only session).
	ErrAskOff = errors.New("the Ask pane is off for this run")
	// ErrAskBusy: an answer is being written; one question at a time.
	ErrAskBusy = errors.New("an answer is being written; wait for it")
)

// askAnswerTimeout bounds one answer.
const askAnswerTimeout = 15 * time.Minute

// AskState is the pane as the run page shows it.
type AskState struct {
	Enabled bool `json:"enabled"`
	// Reason says why the pane is off, when it is.
	Reason  string `json:"reason,omitempty"`
	Harness string `json:"harness,omitempty"`
	// Model is what the next answer runs on; Models what the harness can
	// reach (empty: unknown — any model may be named).
	Model    string             `json:"model,omitempty"`
	Models   []string           `json:"models,omitempty"`
	Busy     bool               `json:"busy"`
	Messages []askstore.Message `json:"messages"`
}

// AskFrame is one event of the pane's stream: a message landing, an
// answer's text so far (delta), or the pane's state changing.
type AskFrame struct {
	Kind    string            `json:"kind"` // message|delta|state
	Message *askstore.Message `json:"message,omitempty"`
	Text    string            `json:"text,omitempty"` // delta: the answer SO FAR (replace, not append)
	Tool    string            `json:"tool,omitempty"` // delta: what the agent is doing
	Busy    bool              `json:"busy,omitempty"`
	Model   string            `json:"model,omitempty"`
}

// askRun is the engine's in-memory state for one run's pane.
type askRun struct {
	busy   bool
	cancel context.CancelFunc
	// models is the harness's reachable list, asked once per run.
	models      []string
	modelsAsked bool
	// sessionModel is the model the live session was last put on.
	sessionModel string
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

// SetAskStore replaces the in-memory conversation store (the daemon's
// Postgres).
func (e *Engine) SetAskStore(s askstore.Store) { e.askStore = s }

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

func (e *Engine) askSetup(ctx context.Context, runID string) (*askSetup, error) {
	v, err := e.runView(ctx, runID)
	if err != nil {
		return nil, err
	}
	fl, err := e.runFlow(ctx, v)
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
	res, err := e.resolveForRun(ctx, v)
	if err != nil {
		return nil, err
	}
	s.res = res
	if !res.HarnessInSet(s.harness) {
		s.reason = fmt.Sprintf("Ask runs on harness %q, which is not in the project's harness set %v.", s.harness, res.HarnessSet())
		return s, nil
	}
	if rc, ok := res.RunnerFor(s.harness).(ports.ReadOnlyCapable); ok && !rc.CanReadOnly() {
		s.reason = fmt.Sprintf("Harness %q cannot run a read-only session, and Ask must not change the code. Name another harness in the flow's ask: section.", s.harness)
		return s, nil
	}
	return s, nil
}

func (e *Engine) askRunState(runID string) *askRun {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.asks[runID]
	if a == nil {
		a = &askRun{}
		e.asks[runID] = a
	}
	return a
}

// askModels is what the pane offers to pick from: the models the harness's
// credentials reach (the provider's list), else what the agent in the
// workspace advertises. Asked once per run; nil when neither can say.
func (e *Engine) askModels(ctx context.Context, s *askSetup) []string {
	a := e.askRunState(s.v.RunID)
	e.mu.Lock()
	if a.modelsAsked {
		models := a.models
		e.mu.Unlock()
		return models
	}
	e.mu.Unlock()
	var models []string
	if s.res.Models != nil {
		if got, err := s.res.Models(ctx, s.harness); err == nil && len(got) > 0 {
			models = got
		}
	}
	if models == nil {
		if lister, ok := s.res.RunnerFor(s.harness).(ports.AgentModelLister); ok {
			actx, cancel := context.WithTimeout(ctx, modelcheck.Timeout)
			got, err := lister.AgentModels(actx, ports.EnvID(s.v.EnvID))
			cancel()
			if err != nil {
				return nil // asked again next time
			}
			models = got
		}
	}
	e.mu.Lock()
	a.models, a.modelsAsked = models, true
	e.mu.Unlock()
	return models
}

// askModel is the model the next answer runs on: the thread's pick, else
// the flow's default model when the harness reaches it, else the first the
// harness offers.
func askModel(th askstore.Thread, fl *flow.Flow, models []string) string {
	if th.Model != "" {
		return th.Model
	}
	m := fl.Defaults.Model
	if len(models) > 0 && !slices.Contains(models, m) && !modelcheck.Accepts(models, m) {
		return models[0]
	}
	return m
}

// AskState is the pane for a run: whether it is on (and why not), its
// conversation so far, the model it answers with and the ones it could.
func (e *Engine) AskState(ctx context.Context, runID string) (AskState, error) {
	s, err := e.askSetup(ctx, runID)
	if err != nil {
		return AskState{}, err
	}
	defer s.release()
	st := AskState{Enabled: s.reason == "", Reason: s.reason, Harness: s.harness, Messages: []askstore.Message{}}
	msgs, err := e.askStore.AskMessages(ctx, runID, 0)
	if err != nil {
		return AskState{}, err
	}
	if msgs != nil {
		st.Messages = msgs
	}
	th, _, err := e.askStore.AskThread(ctx, runID)
	if err != nil {
		return AskState{}, err
	}
	if st.Enabled {
		st.Models = e.askModels(ctx, s)
		st.Model = askModel(th, s.fl, st.Models)
	} else if th.Model != "" {
		st.Model = th.Model
	}
	e.mu.Lock()
	if a := e.asks[runID]; a != nil {
		st.Busy = a.busy
	}
	e.mu.Unlock()
	return st, nil
}

// AskMessages lists the conversation after seq (the stream's replay).
func (e *Engine) AskMessages(ctx context.Context, runID string, afterSeq int64) ([]askstore.Message, error) {
	if _, err := e.runView(ctx, runID); err != nil {
		return nil, err
	}
	return e.askStore.AskMessages(ctx, runID, afterSeq)
}

// AskWatch streams a run's pane: messages as they land, an answer's text
// as it is written, the state as it changes.
func (e *Engine) AskWatch(runID string) (<-chan AskFrame, func()) {
	return e.askHub.subscribe(runID)
}

// Ask records a question and starts its answer; the question is returned
// at once and the answer arrives on the stream (and in the next AskState).
func (e *Engine) Ask(ctx context.Context, runID, userID, text string) (askstore.Message, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return askstore.Message{}, errors.New("the question is empty")
	}
	if len(text) > maxMessageLen {
		return askstore.Message{}, fmt.Errorf("the question is longer than %d bytes", maxMessageLen)
	}
	s, err := e.askSetup(ctx, runID)
	if err != nil {
		return askstore.Message{}, err
	}
	if s.reason != "" {
		s.release()
		return askstore.Message{}, fmt.Errorf("%w: %s", ErrAskOff, s.reason)
	}
	a := e.askRunState(runID)
	e.mu.Lock()
	if a.busy {
		e.mu.Unlock()
		s.release()
		return askstore.Message{}, ErrAskBusy
	}
	a.busy = true
	e.mu.Unlock()
	fail := func(err error) (askstore.Message, error) {
		e.mu.Lock()
		a.busy = false
		e.mu.Unlock()
		s.release()
		return askstore.Message{}, err
	}
	th, _, err := e.askStore.AskThread(ctx, runID)
	if err != nil {
		return fail(err)
	}
	th.RunID = runID
	model := askModel(th, s.fl, e.askModels(ctx, s))
	q, err := e.askStore.AppendAskMessage(ctx, askstore.Message{RunID: runID, ID: e.newID(), Role: askstore.RoleQuestion, Text: text, UserID: userID})
	if err != nil {
		return fail(err)
	}
	e.askHub.publish(runID, AskFrame{Kind: "message", Message: &q})
	e.askHub.publish(runID, AskFrame{Kind: "state", Busy: true, Model: model})
	go e.answerAsk(s, a, th, model, text)
	return q, nil
}

// answerAsk runs one question through the pane's session — opened on the
// first, continued after — and records what came back. Detached from the
// request: the asker may close the page and read the answer later.
func (e *Engine) answerAsk(s *askSetup, a *askRun, th askstore.Thread, model, question string) {
	defer s.release()
	runID := s.v.RunID
	ctx, cancel := context.WithTimeout(context.Background(), askAnswerTimeout)
	defer cancel()
	e.mu.Lock()
	a.cancel = cancel
	e.mu.Unlock()

	redact := newRedactor(s.res, e.RunToken(runID))
	runner := s.res.RunnerFor(s.harness)
	var mu sync.Mutex
	var text strings.Builder
	var lastDelta time.Time
	sessionSeen := func(id string) {
		if id == "" || th.SessionID == id {
			return
		}
		th.SessionID = id
		if err := e.askStore.SaveAskThread(ctx, th); err != nil {
			e.log.Warn("ask: save thread", "run", runID, "err", err)
		}
	}
	events := func(ev ports.AgentEvent) {
		switch ev.Kind {
		case ports.AgentEvSession:
			sessionSeen(ev.Text)
		case ports.AgentEvMessage:
			mu.Lock()
			text.WriteString(ev.Text)
			due := time.Since(lastDelta) >= deltaEvery
			if due {
				lastDelta = time.Now()
			}
			soFar := text.String()
			mu.Unlock()
			if due {
				e.askHub.publish(runID, AskFrame{Kind: "delta", Text: redact.scrub(soFar)})
			}
		case ports.AgentEvTool:
			if ev.Tool != nil && ev.Tool.Title != "" && ev.Tool.Status != "completed" && ev.Tool.Status != "failed" {
				mu.Lock()
				soFar := text.String()
				mu.Unlock()
				e.askHub.publish(runID, AskFrame{Kind: "delta", Text: redact.scrub(soFar), Tool: redact.scrub(ev.Tool.Title)})
			}
		}
	}
	in := ports.AgentInput{Prompt: question, Events: events, ReadOnly: true}
	spec := ports.AgentSpec{Agent: agentspec.AskAgent, Harness: s.harness, Model: model}
	var res ports.AgentResult
	var err error
	if th.SessionID == "" {
		res, err = runner.Run(ctx, ports.EnvID(s.v.EnvID), spec, in)
	} else {
		e.mu.Lock()
		if a.sessionModel != "" && a.sessionModel != model {
			in.Model = model // switch the live session over first
		}
		e.mu.Unlock()
		res, err = runner.Resume(ctx, ports.EnvID(s.v.EnvID), ports.SessionID(th.SessionID), in)
	}

	// What the answer is recorded with survives the answer's own context.
	sctx, scancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer scancel()
	if res.Session != "" {
		th.SessionID = string(res.Session)
	}
	th.Model = model
	if serr := e.askStore.SaveAskThread(sctx, th); serr != nil {
		e.log.Warn("ask: save thread", "run", runID, "err", serr)
	}
	mu.Lock()
	said := strings.TrimSpace(text.String())
	mu.Unlock()
	ans := askstore.Message{RunID: runID, ID: e.newID(), Role: askstore.RoleAnswer, Text: redact.scrub(said),
		Model: model, CostUSD: res.Usage.CostUSD, Turns: res.TurnsUsed}
	if err != nil {
		ans.Error = redact.scrub(err.Error())
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			ans.Error = fmt.Sprintf("no answer within %s", askAnswerTimeout)
		}
	} else if ans.Text == "" && res.Output != "" {
		ans.Text = redact.scrub(strings.TrimSpace(res.Output))
	}
	m, aerr := e.askStore.AppendAskMessage(sctx, ans)
	if aerr != nil {
		e.log.Error("ask: record answer", "run", runID, "err", aerr)
		m = ans
	}
	e.mu.Lock()
	a.busy = false
	a.cancel = nil
	if err == nil || th.SessionID != "" {
		a.sessionModel = model
	}
	e.mu.Unlock()
	e.askHub.publish(runID, AskFrame{Kind: "message", Message: &m})
	e.askHub.publish(runID, AskFrame{Kind: "state", Busy: false, Model: model})
}

// AskSetModel picks the model the pane answers with from here on: the live
// session is switched over at the next question.
func (e *Engine) AskSetModel(ctx context.Context, runID, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return errors.New("model is required")
	}
	s, err := e.askSetup(ctx, runID)
	if err != nil {
		return err
	}
	defer s.release()
	if s.reason != "" {
		return fmt.Errorf("%w: %s", ErrAskOff, s.reason)
	}
	if models := e.askModels(ctx, s); len(models) > 0 && !slices.Contains(models, model) && !modelcheck.Accepts(models, model) {
		msg := fmt.Sprintf("model %q is not available to harness %s", model, s.harness)
		if near := modelcheck.Closest(model, models, 3); len(near) > 0 {
			msg += " (closest: " + strings.Join(near, ", ") + ")"
		}
		return errors.New(msg)
	}
	th, _, err := e.askStore.AskThread(ctx, runID)
	if err != nil {
		return err
	}
	th.RunID, th.Model = runID, model
	if err := e.askStore.SaveAskThread(ctx, th); err != nil {
		return err
	}
	e.mu.Lock()
	busy := e.asks[runID] != nil && e.asks[runID].busy
	e.mu.Unlock()
	e.askHub.publish(runID, AskFrame{Kind: "state", Busy: busy, Model: model})
	return nil
}
