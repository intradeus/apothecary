package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/agentspec"
	askstore "apothecary/internal/ask"
	"apothecary/internal/ports"
	"apothecary/internal/run"
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
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (ports.AgentResult, error) {
		if call.Spec.Agent == agentspec.AskAgent {
			mu.Lock()
			askCalls = append(askCalls, call)
			mu.Unlock()
			call.In.Events(ports.AgentEvent{Kind: ports.AgentEvSession, Text: "ask-sess"})
			call.In.Events(ports.AgentEvent{Kind: ports.AgentEvTool, Tool: &ports.AgentTool{ID: "t1", Title: "Read main.go", Kind: "read", Status: "in_progress"}})
			call.In.Events(ports.AgentEvent{Kind: ports.AgentEvMessage, Text: "It lives in "})
			<-askGate
			call.In.Events(ports.AgentEvent{Kind: ports.AgentEvMessage, Text: "main.go, line 12."})
			return ports.AgentResult{Session: "ask-sess", TurnsUsed: 1, Usage: ports.Usage{CostUSD: 0.05}, Output: "It lives in main.go, line 12."}, nil
		}
		// The stage's agent works until the test lets it finish.
		select {
		case <-stageGate:
		case <-ctx.Done():
			return ports.AgentResult{}, ctx.Err()
		}
		return ports.AgentResult{Session: "s", TurnsUsed: 1}, nil
	}
	h.runner.ResumeFunc = func(ctx context.Context, call fake.ResumeCall) (ports.AgentResult, error) {
		mu.Lock()
		resumes = append(resumes, call)
		mu.Unlock()
		call.In.Events(ports.AgentEvent{Kind: ports.AgentEvMessage, Text: "Still main.go."})
		return ports.AgentResult{Session: call.Session, TurnsUsed: 1, Usage: ports.Usage{CostUSD: 0.02}}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "askpane", testProject)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Live once the workspace is up, while the stage is still working.
	waitUntil(t, "the pane to come on", func() bool {
		st, err := h.e.AskState(ctx, runID)
		return err == nil && st.Enabled
	})
	st, err := h.e.AskState(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Harness != "fake" || st.Model != "fake/model" || st.Busy || len(st.Messages) != 0 {
		t.Fatalf("state = %+v", st)
	}
	frames, stop := h.e.AskWatch(runID)
	defer stop()

	q, err := h.e.Ask(ctx, runID, "u1", "  where is the entrypoint?  ")
	if err != nil {
		t.Fatal(err)
	}
	if q.Role != askstore.RoleQuestion || q.Text != "where is the entrypoint?" || q.UserID != "u1" || q.Seq != 1 {
		t.Fatalf("question = %+v", q)
	}
	// One at a time: a question while the answer is being written is refused.
	waitUntil(t, "the pane to be busy", func() bool {
		st, _ := h.e.AskState(ctx, runID)
		return st.Busy
	})
	if _, err := h.e.Ask(ctx, runID, "u1", "and again?"); !errors.Is(err, ErrAskBusy) {
		t.Errorf("second question while answering = %v, want ErrAskBusy", err)
	}
	close(askGate)
	waitUntil(t, "the answer", func() bool {
		st, _ := h.e.AskState(ctx, runID)
		return len(st.Messages) == 2 && !st.Busy
	})
	st, _ = h.e.AskState(ctx, runID)
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
	deadline := time.Now().Add(2 * time.Second)
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
	if err := h.e.AskSetModel(ctx, runID, "other/model"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.Ask(ctx, runID, "u1", "sure?"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the second answer", func() bool {
		st, _ := h.e.AskState(ctx, runID)
		return len(st.Messages) == 4 && !st.Busy
	})
	mu.Lock()
	if len(resumes) != 1 || resumes[0].Session != "ask-sess" || resumes[0].Reply != "sure?" || resumes[0].In.Model != "other/model" || !resumes[0].In.ReadOnly {
		t.Errorf("resume = %+v", resumes)
	}
	mu.Unlock()
	st, _ = h.e.AskState(ctx, runID)
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
	st, err = h.e.AskState(ctx, runID)
	if err != nil || len(st.Messages) != 4 {
		t.Errorf("after the run: %+v, %v", st, err)
	}
}

func TestAskPaneIsOffWithoutASection(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	gate := make(chan struct{})
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (ports.AgentResult, error) {
		<-gate
		return ports.AgentResult{Session: "s", TurnsUsed: 1}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := h.e.AskState(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled || !strings.Contains(st.Reason, "no Ask section") {
		t.Errorf("state = %+v", st)
	}
	if _, err := h.e.Ask(ctx, runID, "u1", "hello?"); !errors.Is(err, ErrAskOff) {
		t.Errorf("ask = %v, want ErrAskOff", err)
	}
	if err := h.e.AskSetModel(ctx, runID, "x/y"); !errors.Is(err, ErrAskOff) {
		t.Errorf("set model = %v, want ErrAskOff", err)
	}
	close(gate)
	h.e.Wait(runID)
}

// The pane's harness is checked at run creation like a stage's.
func TestAskHarnessMustBeInTheProjectSet(t *testing.T) {
	h := newHarness(t, mustFlow(t, strings.Replace(askPaneFlowDoc, "ask: { harness: fake }", "ask: { harness: nope }", 1)))
	_, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "askpane", testProject)
	if err == nil || !strings.Contains(err.Error(), `ask: harness "nope"`) {
		t.Fatalf("create run = %v", err)
	}
}

// A harness that cannot promise a read-only session keeps the pane off.
type noReadOnlyRunner struct{ *fake.AgentRunner }

func (noReadOnlyRunner) CanReadOnly() bool { return false }

func TestAskPaneIsOffOnAHarnessThatCannotBeReadOnly(t *testing.T) {
	h := newHarness(t, mustFlow(t, askPaneFlowDoc))
	h.res.Runners = map[string]ports.AgentRunner{"fake": noReadOnlyRunner{h.runner}}
	gate := make(chan struct{})
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (ports.AgentResult, error) {
		<-gate
		return ports.AgentResult{Session: "s", TurnsUsed: 1}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "askpane", testProject)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the workspace", func() bool { return h.view(t, runID).EnvID != "" })
	st, err := h.e.AskState(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled || !strings.Contains(st.Reason, "cannot run a read-only session") {
		t.Errorf("state = %+v", st)
	}
	close(gate)
	h.e.Wait(runID)
}
