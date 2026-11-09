package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	askstore "apothecary/internal/engine/ask"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
)

// The Ask pane (FLOWLANG §1 ask): a read-only session beside the run that
// answers questions about the code, on the harness the flow names, as the
// agent the engine prepares under the reserved name. Not a stage: it runs
// while a stage is working, its conversation is its own, and its model is
// picked in the pane.

const askPaneFlowDoc = `name: askpane
version: 2
parameters: { harness: fake, model: fake/model }
ask: { harness: fake }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder, input: { ticket: { from: run.ticket, type: ticket } } }
`

func TestAskPaneAnswersBesideTheRunningStage(t *testing.T) {
	h := newHarness(t, mustFlow(t, askPaneFlowDoc))
	stageGate := make(chan struct{})
	askGate := make(chan struct{}) // the first answer waits here mid-way
	var mu sync.Mutex
	var askCalls []fake.RunCall
	var resumes []fake.ResumeCall
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		if call.Spec.Agent == agentspec.AskAgent {
			mu.Lock()
			askCalls = append(askCalls, call)
			mu.Unlock()
			call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "ask-sess"})
			call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvTool, Tool: &agentport.AgentTool{ID: "t1", Title: "Read main.go", Kind: "read", Status: "in_progress"}})
			call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Text: "It lives in "})
			<-askGate
			call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Text: "main.go, line 12."})
			return agentport.AgentResult{Session: "ask-sess", TurnsUsed: 1, Usage: agentport.Usage{CostUSD: 0.05}, Output: "It lives in main.go, line 12."}, nil
		}
		// The stage's agent works until the test lets it finish.
		select {
		case <-stageGate:
		case <-ctx.Done():
			return agentport.AgentResult{}, ctx.Err()
		}
		return agentport.AgentResult{Session: "s", TurnsUsed: 1}, nil
	}
	h.runner.ResumeFunc = func(ctx context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
		mu.Lock()
		resumes = append(resumes, call)
		mu.Unlock()
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Text: "Still main.go."})
		return agentport.AgentResult{Session: call.Session, TurnsUsed: 1, Usage: agentport.Usage{CostUSD: 0.02}}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "askpane", testProject)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Live once the workspace is up, while the stage is still working.
	waitUntil(t, "the pane to come on", func() bool {
		st, err := h.askPane().State(ctx, runID)
		return err == nil && st.Enabled
	})
	st, err := h.askPane().State(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Harness != "fake" || st.Model != "fake/model" || st.Busy || len(st.Messages) != 0 {
		t.Fatalf("state = %+v", st)
	}
	frames, stop := h.askPane().Watch(runID)
	defer stop()

	q, err := h.askPane().Ask(ctx, runID, "u1", "  where is the entrypoint?  ")
	if err != nil {
		t.Fatal(err)
	}
	if q.Role != askstore.RoleQuestion || q.Text != "where is the entrypoint?" || q.UserID != "u1" || q.Seq != 1 {
		t.Fatalf("question = %+v", q)
	}
	// One at a time: a question while the answer is being written is refused.
	waitUntil(t, "the pane to be busy", func() bool {
		st, _ := h.askPane().State(ctx, runID)
		return st.Busy
	})
	if _, err := h.askPane().Ask(ctx, runID, "u1", "and again?"); !errors.Is(err, askstore.ErrBusy) {
		t.Errorf("second question while answering = %v, want askstore.ErrBusy", err)
	}
	close(askGate)
	waitUntil(t, "the answer", func() bool {
		st, _ := h.askPane().State(ctx, runID)
		return len(st.Messages) == 2 && !st.Busy
	})
	st, _ = h.askPane().State(ctx, runID)
	a := st.Messages[1]
	if a.Role != askstore.RoleAnswer || a.Text != "It lives in main.go, line 12." || a.CostUSD != 0.05 || a.Turns != 1 || a.Model != "fake/model" || a.Error != "" {
		t.Fatalf("answer = %+v", a)
	}
	mu.Lock()
	if len(askCalls) != 1 || !askCalls[0].In.ReadOnly || askCalls[0].Spec.Harness != "fake" || askCalls[0].In.Prompt != "where is the entrypoint?" {
		t.Errorf("ask invocation = %+v", askCalls)
	}
	mu.Unlock()
	// The stream carried the question, the answer being written, and the answer.
	var kinds []string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && strings.Count(strings.Join(kinds, " "), "message") < 2 {
		select {
		case f := <-frames:
			kinds = append(kinds, f.Kind)
		case <-time.After(50 * time.Millisecond):
		}
	}
	joined := strings.Join(kinds, " ")
	if !strings.HasPrefix(joined, "message state") || !strings.Contains(joined, "delta") || strings.Count(joined, "message") < 2 {
		t.Errorf("frames = %s", joined)
	}

	// The next question continues the same session; a model picked in the
	// pane switches it over first.
	if err := h.askPane().SetModel(ctx, runID, "other/model"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.askPane().Ask(ctx, runID, "u1", "sure?"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the second answer", func() bool {
		st, _ := h.askPane().State(ctx, runID)
		return len(st.Messages) == 4 && !st.Busy
	})
	mu.Lock()
	if len(resumes) != 1 || resumes[0].Session != "ask-sess" || resumes[0].Reply != "sure?" || resumes[0].In.Model != "other/model" || !resumes[0].In.ReadOnly {
		t.Errorf("resume = %+v", resumes)
	}
	mu.Unlock()
	st, _ = h.askPane().State(ctx, runID)
	if st.Model != "other/model" || st.Messages[3].Model != "other/model" || st.Messages[3].Text != "Still main.go." {
		t.Errorf("state after the switch = %+v", st)
	}
	// The stage was never touched: it is still working.
	if v := h.view(t, runID); v.State != run.Running {
		t.Errorf("run state while asking = %s", v.State)
	}
	close(stageGate)
	h.e.Wait(runID)
	// The conversation outlives the run while the workspace does.
	st, err = h.askPane().State(ctx, runID)
	if err != nil || len(st.Messages) != 4 {
		t.Errorf("after the run: %+v, %v", st, err)
	}
}

// A pane whose session the workspace no longer has (its agent restarted,
// the container was rebuilt) answers on a fresh one instead of failing every
// question from then on. A resume that already said something is not run
// again: its error is the answer.
func TestAskPaneStartsAFreshSessionWhenItsOldOneIsGone(t *testing.T) {
	h := newHarness(t, mustFlow(t, askPaneFlowDoc))
	stageGate := make(chan struct{})
	var mu sync.Mutex
	var fresh int
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		if call.Spec.Agent == agentspec.AskAgent {
			mu.Lock()
			fresh++
			mu.Unlock()
			call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Text: "In main.go."})
			return agentport.AgentResult{Session: "new-sess", TurnsUsed: 1}, nil
		}
		select {
		case <-stageGate:
		case <-ctx.Done():
			return agentport.AgentResult{}, ctx.Err()
		}
		return agentport.AgentResult{Session: "s", TurnsUsed: 1}, nil
	}
	h.runner.ResumeFunc = func(context.Context, fake.ResumeCall) (agentport.AgentResult, error) {
		return agentport.AgentResult{}, errors.New("session/load gone-sess: ACP session not found")
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "askpane", testProject)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	waitUntil(t, "the pane to come on", func() bool {
		st, err := h.askPane().State(ctx, runID)
		return err == nil && st.Enabled
	})
	// The thread remembers a session from before the workspace's agent
	// restarted.
	if err := h.askPane().Store.SaveAskThread(ctx, askstore.Thread{RunID: runID, SessionID: "gone-sess"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.askPane().Ask(ctx, runID, "u1", "where?"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the answer", func() bool {
		st, _ := h.askPane().State(ctx, runID)
		return len(st.Messages) == 2 && !st.Busy
	})
	st, _ := h.askPane().State(ctx, runID)
	if a := st.Messages[1]; a.Text != "In main.go." || a.Error != "" {
		t.Errorf("answer = %+v, want one from a fresh session", a)
	}
	th, _, _ := h.askPane().Store.AskThread(ctx, runID)
	mu.Lock()
	if th.SessionID != "new-sess" || fresh != 1 {
		t.Errorf("thread session = %q after %d fresh sessions, want new-sess after 1", th.SessionID, fresh)
	}
	mu.Unlock()

	// A resume that fails after it has spoken keeps its error.
	h.runner.ResumeFunc = func(_ context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Text: "Halfway"})
		return agentport.AgentResult{TurnsUsed: 1}, errors.New("the provider hung up")
	}
	if _, err := h.askPane().Ask(ctx, runID, "u1", "and?"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the second answer", func() bool {
		st, _ := h.askPane().State(ctx, runID)
		return len(st.Messages) == 4 && !st.Busy
	})
	st, _ = h.askPane().State(ctx, runID)
	mu.Lock()
	if a := st.Messages[3]; !strings.Contains(a.Error, "hung up") || fresh != 1 {
		t.Errorf("answer = %+v after %d fresh sessions, want the resume's error and no new session", a, fresh)
	}
	mu.Unlock()
	close(stageGate)
	h.e.Wait(runID)
}

func TestAskPaneIsOffWithoutASection(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	gate := make(chan struct{})
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		<-gate
		return agentport.AgentResult{Session: "s", TurnsUsed: 1}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := h.askPane().State(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled || !strings.Contains(st.Reason, "no Ask section") {
		t.Errorf("state = %+v", st)
	}
	if _, err := h.askPane().Ask(ctx, runID, "u1", "hello?"); !errors.Is(err, askstore.ErrOff) {
		t.Errorf("ask = %v, want askstore.ErrOff", err)
	}
	if err := h.askPane().SetModel(ctx, runID, "x/y"); !errors.Is(err, askstore.ErrOff) {
		t.Errorf("set model = %v, want askstore.ErrOff", err)
	}
	close(gate)
	h.e.Wait(runID)
}

// The pane's harness is checked at run creation like a stage's.
func TestAskHarnessMustBeInTheProjectSet(t *testing.T) {
	h := newHarness(t, mustFlow(t, strings.Replace(askPaneFlowDoc, "ask: { harness: fake }", "ask: { harness: nope }", 1)))
	_, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "askpane", testProject)
	if err == nil || !strings.Contains(err.Error(), `ask: harness "nope"`) {
		t.Fatalf("create run = %v", err)
	}
}

// A harness that cannot promise a read-only session keeps the pane off.
type noReadOnlyRunner struct{ *fake.AgentRunner }

func (noReadOnlyRunner) CanReadOnly() bool { return false }

func TestAskPaneIsOffOnAHarnessThatCannotBeReadOnly(t *testing.T) {
	h := newHarness(t, mustFlow(t, askPaneFlowDoc))
	h.res.Runners = map[string]agentport.AgentRunner{"fake": noReadOnlyRunner{h.runner}}
	gate := make(chan struct{})
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		<-gate
		return agentport.AgentResult{Session: "s", TurnsUsed: 1}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "askpane", testProject)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the workspace", func() bool { return h.view(t, runID).EnvID != "" })
	st, err := h.askPane().State(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled || !strings.Contains(st.Reason, "cannot run a read-only session") {
		t.Errorf("state = %+v", st)
	}
	close(gate)
	h.e.Wait(runID)
}
