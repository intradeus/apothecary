package kinds

import (
	"context"
	"errors"
	"strings"
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
		"artifacts/review/verdict.yaml is your verdict: APPROVE or REJECT.\n"
	outs := declaredOutputs(prompt)
	if len(outs) != 5 || outs[0] != (declaredOutput{path: "artifacts/plan.md"}) || outs[1] != (declaredOutput{path: "artifacts/review/verdict.yaml", typ: "verdict"}) ||
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
		!strings.HasPrefix(got["artifacts/commit.json"], "{") {
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
	if err := demoTurn(context.Background(), func(ev agentport.AgentEvent) { evs = append(evs, ev) }, 0); err != nil {
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
	if err := demoTurn(ctx, nil, time.Hour); !errors.Is(err, context.Canceled) {
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
