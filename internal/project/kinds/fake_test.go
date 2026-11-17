package kinds

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow/reviewfile"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
)

// The dev fake writes, for each file the OUTPUT CONTRACT names, a document
// that holds its type's shape — found by the type the contract prints
// beside the role, wherever the stage binds the path — and a stub for the
// rest.
func TestDevFakeWritesWhatTheContractAsks(t *testing.T) {
	prompt := "# INPUT: comments — the threads (file: artifacts/threads.yaml)\n" +
		"merge_request: https://x/mr/1\nthreads:\n  - id: d1\n    resolved: false\n    ours: false\n    comments:\n      - { id: c1, author: rev, body: rename }\n  - id: d2\n    resolved: false\n    ours: false\n    comments: []\n\n" +
		"# OUTPUT CONTRACT\nBefore finishing you MUST write these files (relative to repo root):\n" +
		"- artifacts/plan.md — plan: the plan\n" +
		"- artifacts/review/verdict.yaml — verdict (verdict): APPROVE or REJECT\n" +
		"- artifacts/review.yaml — review (review_threads): the review\n" +
		"- artifacts/replies.yaml — replies (review_replies)\n" +
		"- artifacts/commit.json — message (commit_message): the commit\n" +
		"- artifacts/journal-notes.yaml — notes (journal_notes): significant departures\n" +
		"- artifacts/notes.json — notes (journal_notes)\n" +
		"artifacts/review/verdict.yaml is your verdict: APPROVE or REJECT.\n"
	outs := declaredOutputs(prompt)
	if len(outs) != 7 || outs[5] != (declaredOutput{path: "artifacts/journal-notes.yaml", typ: "journal_notes"}) || outs[0] != (declaredOutput{path: "artifacts/plan.md"}) || outs[1] != (declaredOutput{path: "artifacts/review/verdict.yaml", typ: "verdict"}) ||
		outs[2].typ != "review_threads" || outs[3].typ != "review_replies" || outs[4] != (declaredOutput{path: "artifacts/commit.json", typ: "commit_message"}) {
		t.Fatalf("declared = %+v", outs)
	}
	call := fake.RunCall{Spec: agentport.AgentSpec{Agent: "dev"}, In: agentport.AgentInput{Prompt: prompt}}
	got := map[string]string{}
	for _, out := range outs {
		b, err := stubOutput(out, call)
		if err != nil {
			t.Fatal(err)
		}
		got[out.path] = string(b)
	}
	if !strings.HasPrefix(got["artifacts/plan.md"], "# Dev stub") || !strings.Contains(got["artifacts/review/verdict.yaml"], "verdict: APPROVE") ||
		!strings.HasPrefix(got["artifacts/commit.json"], "{") || got["artifacts/journal-notes.yaml"] != "notes: []\n" || got["artifacts/notes.json"] != "{\n  \"notes\": []\n}\n" {
		t.Errorf("stubs = %q", got)
	}
	if rev, err := reviewfile.ParseReview("artifacts/review.yaml", []byte(got["artifacts/review.yaml"])); err != nil || rev.Summary == "" || len(rev.Notes) != 0 {
		t.Errorf("review stub = %q (%v)", got["artifacts/review.yaml"], err)
	}
	replies, err := reviewfile.ParseReplies("artifacts/replies.yaml", []byte(got["artifacts/replies.yaml"]))
	if err != nil || len(replies) != 2 || replies[0].Thread != "d1" || replies[1].Thread != "d2" || replies[0].Did != reviewfile.DidAnswered {
		t.Errorf("replies stub = %q (%v)", got["artifacts/replies.yaml"], err)
	}
	if err := reviewfile.CheckReplies(replies, []string{"d1", "d2"}); err != nil {
		t.Errorf("the replies stub answers every thread: %v", err)
	}
}

// The dev sandboxes' synthetic stats stay inside their host, whatever the
// workspace and the hour.
func TestFakeSamplesStayInBounds(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	for _, env := range []envport.EnvID{"a", "fake-env-1", "fake-env-2", "zzz"} {
		for step := range 500 {
			s := syntheticSample(env, start.Add(time.Duration(step)*7*time.Second))
			if s.CPUs != fakeCPUs || s.CPUPerc < 0 || s.CPUPerc > fakeCPUs*100 {
				t.Fatalf("%s: cpu %.1f%% of %d cores", env, s.CPUPerc, s.CPUs)
			}
			if s.MemUsed <= 0 || s.MemUsed >= s.MemLimit || s.PIDs <= 0 || s.NetRx < 0 || s.NetTx < 0 {
				t.Fatalf("%s: %+v", env, s)
			}
		}
	}
	p, _ := FakeProvisioner{}.New(Config{}, ProvisionerDeps{})
	if _, ok := p.(envport.ResourceSampler); !ok {
		t.Fatal("the fake provisioner samples no resources")
	}
}

// A demo turn: the turn opens, ordinary tools and subagent spawns (one, then
// a batch) play out, the turn closes.
func TestDemoTurn(t *testing.T) {
	var evs []agentport.AgentEvent
	if err := demoTurn(context.Background(), func(ev agentport.AgentEvent) { evs = append(evs, ev) }, nil, 0); err != nil {
		t.Fatal(err)
	}
	if len(evs) < 3 || evs[0].Kind != agentport.AgentEvTurnStart || evs[len(evs)-1].Kind != agentport.AgentEvTurnEnd {
		t.Fatalf("events = %+v", evs)
	}
	var single, batch, ordinary bool
	for _, ev := range evs {
		if ev.Tool == nil || ev.Tool.Status != "in_progress" {
			continue
		}
		switch in := ev.Tool.Input; {
		case strings.Contains(in, `"tasks":[`):
			batch = true
		case strings.Contains(in, `"solutionSpace"`):
			single = true
		default:
			ordinary = true
		}
	}
	if !single || !batch || !ordinary {
		t.Errorf("single spawn %v, batch %v, ordinary tools %v", single, batch, ordinary)
	}
}

// A demo turn stops when its run does.
func TestDemoTurnHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := demoTurn(ctx, nil, nil, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > time.Second {
		t.Error("a cancelled demo turn kept going")
	}
}

// Without demo the fake is the quick one every test relies on; with it, the
// turn is acted out.
func TestFakeHarnessDemoIsOptIn(t *testing.T) {
	envp := fake.NewEnvProvisioner()
	env, err := envp.Provision(context.Background(), envport.EnvSpec{})
	if err != nil {
		t.Fatal(err)
	}
	run := func(cfg Config, ctx context.Context) ([]agentport.AgentEvent, error) {
		h, err := FakeHarness{}.New(cfg, envp)
		if err != nil {
			t.Fatal(err)
		}
		var evs []agentport.AgentEvent
		_, err = h.Run(ctx, env, agentport.AgentSpec{Agent: "dev"}, agentport.AgentInput{
			Events: func(ev agentport.AgentEvent) { evs = append(evs, ev) }})
		return evs, err
	}
	start := time.Now()
	if evs, err := run(Config{}, context.Background()); err != nil || len(evs) != 0 || time.Since(start) > 5*time.Second {
		t.Errorf("plain fake: %d events, %v, took %s", len(evs), err, time.Since(start))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	evs, err := run(Config{Values: map[string]any{"demo": "on"}}, ctx)
	if !errors.Is(err, context.DeadlineExceeded) || len(evs) != 1 || evs[0].Kind != agentport.AgentEvTurnStart {
		t.Errorf("demo fake under a short deadline: %+v, %v", evs, err)
	}
}

// testInbox is an engine inbox for the fake: messages, the hold, a stop.
type testInbox struct {
	mu      sync.Mutex
	q       []agentport.UserMessage
	held    bool
	stopped string
	wake    chan struct{}
}

func newTestInbox(held bool) *testInbox {
	return &testInbox{held: held, wake: make(chan struct{}, 1)}
}

func (b *testInbox) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *testInbox) push(m agentport.UserMessage) {
	b.mu.Lock()
	b.q = append(b.q, m)
	b.mu.Unlock()
	b.signal()
}

func (b *testInbox) release(handback string) {
	b.mu.Lock()
	b.q = append(b.q, agentport.UserMessage{Text: handback, Verbatim: true})
	b.held = false
	b.mu.Unlock()
	b.signal()
}

func (b *testInbox) stop(turn string) {
	b.mu.Lock()
	b.stopped = turn
	b.mu.Unlock()
	b.signal()
}

func (b *testInbox) Wake() <-chan struct{} { return b.wake }
func (b *testInbox) Take() []agentport.UserMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.q
	b.q = nil
	return q
}
func (b *testInbox) Held() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.held
}
func (b *testInbox) StopTurn(turn string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return turn != "" && turn == b.stopped
}

// eventLog collects a run's events from the runner's goroutine.
type eventLog struct {
	mu  sync.Mutex
	evs []agentport.AgentEvent
}

func (l *eventLog) on(ev agentport.AgentEvent) {
	l.mu.Lock()
	l.evs = append(l.evs, ev)
	l.mu.Unlock()
}

// ended reports whether turn has ended, and how.
func (l *eventLog) ended(turn string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ev := range l.evs {
		if ev.Kind == agentport.AgentEvTurnEnd && ev.Turn == turn {
			return ev.StopReason, true
		}
	}
	return "", false
}

func (l *eventLog) wait(t *testing.T, turn string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if how, ok := l.ended(turn); ok {
			return how
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn %s never ended", turn)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// A forced take-over ends the demo turn at once, cancelled; a stop for any
// other turn leaves it playing.
func TestDemoTurnStopsWhenTakenOver(t *testing.T) {
	in := newTestInbox(true)
	in.stop("other")
	var log eventLog
	done := make(chan error, 1)
	go func() { done <- demoTurn(context.Background(), log.on, in, time.Hour) }()
	time.Sleep(10 * time.Millisecond)
	if _, ok := log.ended("demo"); ok {
		t.Fatal("a stop for another turn ended the demo turn")
	}
	in.stop("demo")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the demo turn played on")
	}
	if len(log.evs) != 2 || log.evs[0].Kind != agentport.AgentEvTurnStart || log.evs[1].Kind != agentport.AgentEvTurnEnd ||
		log.evs[1].StopReason != "cancelled" {
		t.Errorf("events = %+v", log.evs)
	}
}

// The fake serves a take-over end to end: its turn stops, each holder
// message and then the release's handback get a reply turn, and only then
// does it write its outputs and return.
func TestFakeHarnessServesATakeOver(t *testing.T) {
	envp := fake.NewEnvProvisioner()
	env, err := envp.Provision(context.Background(), envport.EnvSpec{})
	if err != nil {
		t.Fatal(err)
	}
	runner := newFakeRunner(envp, true, 5*time.Millisecond)
	in := newTestInbox(true)
	in.stop("demo")
	var log eventLog
	prompt := "you MUST write these files (relative to repo root):\n- artifacts/plan.md — plan: the plan\n\nrest"
	type result struct {
		res agentport.AgentResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := runner.Run(context.Background(), env, agentport.AgentSpec{Agent: "dev"},
			agentport.AgentInput{Prompt: prompt, Inbox: in, Events: log.on})
		done <- result{res, err}
	}()
	if how := log.wait(t, "demo"); how != "cancelled" {
		t.Fatalf("demo turn ended %q", how)
	}
	in.push(agentport.UserMessage{Text: "use sqlite"})
	if how := log.wait(t, "reply-1"); how != "end_turn" {
		t.Fatalf("reply ended %q", how)
	}
	select {
	case r := <-done:
		t.Fatalf("the fake returned under the hold: %+v, %v", r.res, r.err)
	default:
	}
	if _, err := envp.ReadFile(context.Background(), env, "artifacts/plan.md"); err == nil {
		t.Fatal("outputs written under the hold")
	}
	in.release("Hand back.\nThe rest.")
	var r result
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the fake did not return after the release")
	}
	if r.err != nil || r.res.TurnsUsed != 5 {
		t.Fatalf("= %+v, %v", r.res, r.err)
	}
	if _, err := envp.ReadFile(context.Background(), env, "artifacts/plan.md"); err != nil {
		t.Errorf("outputs: %v", err)
	}
	var got []string
	for _, ev := range log.evs {
		switch ev.Kind {
		case agentport.AgentEvUserMessage:
			got = append(got, "user:"+ev.Messages[0].Text)
		case agentport.AgentEvTurnStart:
			got = append(got, "start:"+ev.Turn)
		case agentport.AgentEvMessage:
			got = append(got, "say:"+ev.Text)
		}
	}
	want := "start:demo|user:use sqlite|start:reply-1|say:(fake agent) Received: use sqlite|" +
		"user:Hand back.\nThe rest.|start:reply-2|say:(fake agent) Received: Hand back.…"
	if g := strings.Join(got, "|"); g != want {
		t.Errorf("events = %q\nwant      %q", g, want)
	}
}

// Nobody holding the session, the fake returns after its turn.
func TestFakeHarnessWithoutAHoldReturnsAfterItsTurn(t *testing.T) {
	envp := fake.NewEnvProvisioner()
	env, err := envp.Provision(context.Background(), envport.EnvSpec{})
	if err != nil {
		t.Fatal(err)
	}
	var log eventLog
	res, err := newFakeRunner(envp, true, time.Millisecond).Run(context.Background(), env, agentport.AgentSpec{Agent: "dev"},
		agentport.AgentInput{Inbox: newTestInbox(false), Events: log.on})
	if err != nil || res.TurnsUsed != 3 {
		t.Fatalf("= %+v, %v", res, err)
	}
	if how, ok := log.ended("demo"); !ok || how != "" {
		t.Errorf("demo turn ended %q (%v)", how, ok)
	}
	for _, ev := range log.evs {
		if ev.Kind == agentport.AgentEvUserMessage {
			t.Errorf("a reply turn without a hold: %+v", ev)
		}
	}
}
