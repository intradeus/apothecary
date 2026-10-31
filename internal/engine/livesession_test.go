package engine

import (
	"context"
	"errors"
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

// The agent runs as a live session: its transcript is recorded as it
// happens, the human can message it mid-run, and a question blocks the turn
// until the human answers — or parks the run when they take too long.

func emitAll(in agentport.AgentInput, evs ...agentport.AgentEvent) {
	for _, ev := range evs {
		in.Events(ev)
	}
}

func TestTranscriptIsRecordedCoalescedAndRedacted(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	var deltas []LiveDelta
	// Deltas are live, never replayed: the viewer must be listening before the
	// agent speaks, or the whole transcript streams to nobody. The fake agent
	// runs on the run's own goroutine, which may well reach it before this
	// test has subscribed — so it waits for the subscription first.
	subscribed := make(chan struct{})
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		<-subscribed
		emitAll(call.In,
			agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "sess-t"},
			agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "T1", Cursor: "b:1"},
			agentport.AgentEvent{Kind: agentport.AgentEvThought, Turn: "T1", Text: "let me look", Cursor: "b:2"},
			agentport.AgentEvent{Kind: agentport.AgentEvMessage, Turn: "T1", Text: "The key is ", Cursor: "b:3"},
			agentport.AgentEvent{Kind: agentport.AgentEvMessage, Turn: "T1", Text: "sk-test, reading.", Cursor: "b:4"},
			agentport.AgentEvent{Kind: agentport.AgentEvTool, Turn: "T1", Cursor: "b:5",
				Tool: &agentport.AgentTool{ID: "c1", Title: "Read .env", Kind: "read", Status: "pending"}},
			agentport.AgentEvent{Kind: agentport.AgentEvTool, Turn: "T1", Cursor: "b:6",
				Tool: &agentport.AgentTool{ID: "c1", Status: "completed", Output: "ANTHROPIC_API_KEY=sk-test"}},
			agentport.AgentEvent{Kind: agentport.AgentEvPlan, Turn: "T1", Cursor: "b:7",
				Plan: []agentport.AgentPlanEntry{{Content: "add endpoint", Status: "in_progress"}}},
			// An unchanged plan re-sent is not a new record.
			agentport.AgentEvent{Kind: agentport.AgentEvPlan, Turn: "T1", Cursor: "b:8",
				Plan: []agentport.AgentPlanEntry{{Content: "add endpoint", Status: "in_progress"}}},
			agentport.AgentEvent{Kind: agentport.AgentEvMessage, Turn: "T1", Text: "Done.", Cursor: "b:9"},
			agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "T1", StopReason: "end_turn", Cursor: "b:10"},
		)
		return agentport.AgentResult{Session: "sess-t", TurnsUsed: 1, Output: "Done."}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	live, stop := h.e.SubscribeLive(runID)
	defer stop()
	close(subscribed)
	h.e.Wait(runID)
	// Every delta was published on the run's goroutine before it ended, so
	// they are all buffered by now: drain, rather than race a collector.
	for drained := false; !drained; {
		select {
		case d := <-live:
			deltas = append(deltas, d)
		default:
			drained = true
		}
	}
	evs := h.events(t, runID)

	var kinds []string
	for _, ev := range evs {
		switch ev.Payload.(type) {
		case *event.AgentPrompted, *event.AgentSession, *event.AgentMessage, *event.AgentTool, *event.AgentPlan:
			kinds = append(kinds, string(ev.Type))
		}
	}
	want := "agent_prompted agent_session agent_message agent_message agent_tool agent_plan agent_message"
	if got := strings.Join(kinds, " "); got != want {
		t.Fatalf("transcript = %s\nwant         %s", got, want)
	}
	var msgs []*event.AgentMessage
	for _, ev := range evs {
		if m, ok := ev.Payload.(*event.AgentMessage); ok {
			msgs = append(msgs, m)
		}
	}
	if !msgs[0].Thought || msgs[0].Text != "let me look" {
		t.Errorf("thought = %+v", msgs[0])
	}
	// Chunks coalesce into one segment, and secrets never reach the log.
	if msgs[1].Text != "The key is [REDACTED], reading." && strings.Contains(msgs[1].Text, "sk-test") {
		t.Errorf("message not redacted: %q", msgs[1].Text)
	}
	if msgs[1].Segment == msgs[2].Segment || msgs[2].Cursor != "b:10" && msgs[2].Cursor != "b:9" {
		t.Errorf("segments/cursors = %+v / %+v", msgs[1], msgs[2])
	}
	tool, _ := findPayload[*event.AgentTool](evs)
	if tool.Title != "Read .env" || tool.Status != "completed" || tool.Kind != "read" || strings.Contains(tool.Output, "sk-test") {
		t.Errorf("tool = %+v", tool)
	}
	prompted, _ := findPayload[*event.AgentPrompted](evs)
	if prompted.Kind != event.PromptStage || prompted.Text == "" {
		t.Errorf("prompted = %+v", prompted)
	}
	sess, _ := findPayload[*event.AgentSession](evs)
	if sess.SessionID != "sess-t" {
		t.Errorf("session = %+v", sess)
	}
	sawTool := false
	for _, d := range deltas {
		if d.Kind == "tool" && d.Tool != nil && d.Tool.ToolCallID == "c1" {
			sawTool = true
			if strings.Contains(d.Tool.Output, "sk-test") {
				t.Error("live delta leaked a secret")
			}
		}
	}
	if !sawTool {
		t.Errorf("no live tool delta in %+v", deltas)
	}
}

// sendWhenLive retries until the agent invocation is up to take a message.
func sendWhenLive(t *testing.T, h *harness, runID, text string, interrupt bool) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		id, err := h.e.SendMessage(runID, text, interrupt)
		if err == nil {
			return id
		}
		if !errors.Is(err, ErrNoAgentRunning) && !errors.Is(err, ErrRunNotActive) || time.Now().After(deadline) {
			t.Fatalf("send message: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMessageIsDeliveredIntoTheRunningInvocation(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	got := make(chan []agentport.UserMessage, 1)
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		select {
		case <-call.In.Inbox.Wake():
		case <-ctx.Done():
			return agentport.AgentResult{}, ctx.Err()
		}
		if !call.In.Inbox.Interrupting() {
			t.Error("the message asked to interrupt")
		}
		msgs := call.In.Inbox.Take()
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvUserMessage, Messages: msgs})
		got <- msgs
		return agentport.AgentResult{Session: "s", TurnsUsed: 2}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	id := sendWhenLive(t, h, runID, "  use sqlite  ", true)
	msgs := <-got
	if len(msgs) != 1 || msgs[0].ID != id || msgs[0].Text != "use sqlite" {
		t.Fatalf("delivered = %+v", msgs)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	q, ok := findPayload[*event.MessageQueued](evs)
	if !ok || q.MessageID != id || !q.Interrupt || q.StageID != "build" {
		t.Errorf("message_queued = %+v", q)
	}
	d, ok := findPayload[*event.MessageDelivered](evs)
	if !ok || len(d.MessageIDs) != 1 || d.MessageIDs[0] != id {
		t.Errorf("message_delivered = %+v", d)
	}
	// Nothing to take a message any more.
	if _, err := h.e.SendMessage(runID, "late", false); err == nil {
		t.Error("a finished run accepted a message")
	}
}

const twoAgentFlowDoc = `name: two
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: scout, type: agent, harness: $harness, model: $model, agent: builder }
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`

// A message the agent never took (the invocation ended first) rides into the
// next prompt rather than being lost.
func TestUndeliveredMessageRidesIntoTheNextPrompt(t *testing.T) {
	h := newHarness(t, mustFlow(t, twoAgentFlowDoc))
	var prompts []string
	var pmu sync.Mutex
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		pmu.Lock()
		prompts = append(prompts, call.In.Prompt)
		first := len(prompts) == 1
		pmu.Unlock()
		if first {
			// Sees the message arrive but ends without taking it.
			select {
			case <-call.In.Inbox.Wake():
			case <-ctx.Done():
			}
		}
		return agentport.AgentResult{Session: "s", TurnsUsed: 1}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "two", testProject)
	if err != nil {
		t.Fatal(err)
	}
	id := sendWhenLive(t, h, runID, "remember the cache", false)
	h.e.Wait(runID)
	pmu.Lock()
	defer pmu.Unlock()
	if len(prompts) != 2 || !strings.Contains(prompts[1], "Message from the human operator:\n\nremember the cache") {
		t.Fatalf("prompts = %q", prompts)
	}
	var delivered *event.MessageDelivered
	for _, ev := range h.events(t, runID) {
		if d, ok := ev.Payload.(*event.MessageDelivered); ok {
			delivered = d
		}
	}
	if delivered == nil || delivered.StageID != "build" || delivered.MessageIDs[0] != id {
		t.Errorf("message_delivered = %+v", delivered)
	}
}

// A question the human answers while the turn is still live goes back
// in-band: no pause, no resume, the same invocation carries on.
func TestQuestionAnsweredDuringTheLiveHold(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	answer := make(chan string, 1)
	h.runner.RunFunc = func(ctx context.Context, _ fake.RunCall) (agentport.AgentResult, error) {
		id := <-runIDCh
		p, err := h.e.RaisePingKeyed(id, "k1", "clarification", "which db?", "", nil, nil)
		if err != nil {
			return agentport.AgentResult{}, err
		}
		// The ask_human call blocks, as the shim's long-poll does.
		if _, ok := p.Await(ctx); !ok {
			return agentport.AgentResult{}, ctx.Err()
		}
		answer <- ComposeAnswer(p)
		return agentport.AgentResult{Session: "s", TurnsUsed: 3}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID
	p := h.waitPing(t, "clarification")
	// A retried raise (lost response) gets the same question back.
	if again, err := h.e.RaisePingKeyed(runID, "k1", "clarification", "which db?", "", nil, nil); err != nil || again.ID != p.ID {
		t.Fatalf("keyed re-raise = %v, %v", again, err)
	}
	if err := h.e.AnswerPing(p.ID, textAnswer("postgres")); err != nil {
		t.Fatal(err)
	}
	if a := <-answer; !strings.Contains(a, "postgres") {
		t.Errorf("in-band answer = %q", a)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if _, ok := findPayload[*event.StagePaused](evs); ok {
		t.Error("a question answered live paused the attempt")
	}
	if n := len(h.runner.ResumeCalls); n != 0 {
		t.Errorf("resume calls = %d, want 0", n)
	}
	raised := 0
	for _, ev := range evs {
		if _, ok := ev.Payload.(*event.PingRaised); ok {
			raised++
		}
	}
	if raised != 1 {
		t.Errorf("ping_raised = %d, want 1 (the retry is the same question)", raised)
	}
	if v := h.view(t, runID); v.State != run.Completed {
		t.Errorf("state = %s", v.State)
	}
}

// Nobody answers within the live hold: the engine parks the run itself —
// cancels the turn, frees the slot — and resumes the session with the answer.
// The agent never has to be told "end your turn".
func TestUnansweredQuestionParksAfterTheHold(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	h.e.cfg.AskHumanHold = 30 * time.Millisecond
	runIDCh := make(chan string, 1)
	h.runner.RunFunc = func(ctx context.Context, _ fake.RunCall) (agentport.AgentResult, error) {
		id := <-runIDCh
		p, err := h.e.RaisePing(id, "clarification", "one endpoint or two?", "", nil, nil)
		if err != nil {
			return agentport.AgentResult{}, err
		}
		p.Await(ctx) // blocks until the engine cancels the turn
		return agentport.AgentResult{Session: "sess-p", TurnsUsed: 1, Usage: agentport.Usage{CostUSD: 0.1}}, ctx.Err()
	}
	h.runner.ResumeFunc = func(_ context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
		return agentport.AgentResult{Session: "sess-p", TurnsUsed: 1}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID
	p := h.waitPing(t, "clarification")
	paused := waitEvent[*event.StagePaused](t, h, runID)
	if len(paused.PingIDs) != 1 || paused.PingIDs[0] != p.ID || paused.SessionID != "sess-p" ||
		paused.Usage == nil || paused.Usage.CostUSD != 0.1 {
		t.Fatalf("stage_paused = %+v", paused)
	}
	if !p.HandedBack() {
		t.Error("a parked question must read as handed back (the long-poll answers 410)")
	}
	h.e.Wait(runID) // the loop exits: parked, slot given back
	if v := h.view(t, runID); v.State != run.WaitingOnHuman {
		t.Fatalf("state while parked = %s", v.State)
	}
	if err := h.e.AnswerPing(p.ID, textAnswer("two")); err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.RunCompleted](t, h, runID)
	if r := h.runner.ResumeCalls; len(r) != 1 || r[0].Session != "sess-p" || !strings.Contains(r[0].Reply, "two") {
		t.Fatalf("resume calls = %+v", r)
	}
	prompted := []string{}
	for _, ev := range h.events(t, runID) {
		if pp, ok := ev.Payload.(*event.AgentPrompted); ok {
			prompted = append(prompted, pp.Kind)
		}
	}
	if strings.Join(prompted, ",") != "stage,answer" {
		t.Errorf("prompted kinds = %v", prompted)
	}
}

// reattachRunner is a harness whose agent outlived an engine restart.
type reattachRunner struct {
	*fake.AgentRunner
	mu       sync.Mutex
	can      bool
	attached []string // "session@cursor"
	result   agentport.AgentResult
}

func (r *reattachRunner) Reattachable(_ context.Context, _ envport.EnvID, _ agentport.SessionID, _ string) bool {
	return r.can
}

func (r *reattachRunner) Reattach(_ context.Context, _ envport.EnvID, s agentport.SessionID, cursor string, in agentport.AgentInput) (agentport.AgentResult, bool, error) {
	r.mu.Lock()
	r.attached = append(r.attached, string(s)+"@"+cursor)
	r.mu.Unlock()
	in.Events(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Text: "finished while you were away", Cursor: "b:9"})
	return r.result, true, nil
}

func seedLiveAttempt(t *testing.T, h *harness, runID string, extra ...event.Payload) {
	t.Helper()
	ctx := context.Background()
	fl := h.flowOf(t, "ask")
	envID, _ := h.envp.Provision(ctx, envport.EnvSpec{RunID: runID})
	base := []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: "ask", ProjectID: testProject, FlowHash: fl.Hash, Branch: "b"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: string(envID)},
		&event.StageStarted{StageID: "build", Iteration: 1},
		&event.AgentSession{StageID: "build", Iteration: 1, SessionID: "sess-r"},
		&event.AgentMessage{StageID: "build", Iteration: 1, Segment: "build.1.1", Text: "working", Cursor: "b:5"},
	}
	for _, p := range append(base, extra...) {
		if _, err := h.store.Append(ctx, runID, p); err != nil {
			t.Fatal(err)
		}
	}
}

// After a restart, the turn still running in the workspace is FOLLOWED, from
// the last transcript event recorded — not restarted, not re-prompted.
func TestRestartReattachesToTheTurnInFlight(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	rr := &reattachRunner{AgentRunner: h.runner, can: true,
		result: agentport.AgentResult{Session: "sess-r", TurnsUsed: 2, Output: "finished while you were away"}}
	h.res.Runner = rr
	seedLiveAttempt(t, h, "r-a")
	if err := h.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.RunCompleted](t, h, "r-a")
	if len(rr.attached) != 1 || rr.attached[0] != "sess-r@b:5" {
		t.Fatalf("reattached = %v", rr.attached)
	}
	if n, m := len(h.runner.SnapshotRunCalls()), len(h.runner.ResumeCalls); n+m != 0 {
		t.Errorf("run/resume calls = %d/%d, want none", n, m)
	}
	evs := h.events(t, "r-a")
	res, _ := findPayload[*event.StageResumed](evs)
	if res.Reason != event.ResumeRestart || res.SessionID != "sess-r" {
		t.Errorf("stage_resumed = %+v", res)
	}
	if _, ok := findPayload[*event.AgentPrompted](evs); ok {
		t.Error("reattaching sent the agent a prompt")
	}
}

// The turn is gone (the container restarted with the engine), but it had
// asked a question: that question is still answerable — the attempt parks on
// it and the session resumes with the answer.
func TestRestartWithALiveQuestionButNoTurnParksOnIt(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	rr := &reattachRunner{AgentRunner: h.runner, can: false}
	h.res.Runner = rr
	h.runner.ResumeFunc = func(_ context.Context, _ fake.ResumeCall) (agentport.AgentResult, error) {
		return agentport.AgentResult{Session: "sess-r", TurnsUsed: 1}, nil
	}
	seedLiveAttempt(t, h, "r-q",
		&event.PingRaised{PingID: "p-live", Type: "clarification", Question: "rename it?", StageID: "build", Form: textForm(), AnswerKind: event.AnswerForm})
	if err := h.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	paused := waitEvent[*event.StagePaused](t, h, "r-q")
	if len(paused.PingIDs) != 1 || paused.PingIDs[0] != "p-live" || paused.SessionID != "sess-r" {
		t.Fatalf("stage_paused = %+v", paused)
	}
	h.e.Wait("r-q")
	if err := h.e.AnswerPing("p-live", textAnswer("yes")); err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.RunCompleted](t, h, "r-q")
	if r := h.runner.ResumeCalls; len(r) != 1 || !strings.Contains(r[0].Reply, "yes") {
		t.Fatalf("resume calls = %+v", r)
	}
	if len(rr.attached) != 0 {
		t.Errorf("reattached to nothing: %v", rr.attached)
	}
}
