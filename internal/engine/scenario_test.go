// §16 M1 scenario tests: the whole engine drives against the in-memory
// fakes — the proof the port boundaries are real.
package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// testProject is the project id served by the harness's StaticResolver.
const testProject = "prj-test"

type harness struct {
	e      *Engine
	store  *event.MemStore
	envp   *fake.EnvProvisioner
	runner *fake.AgentRunner
	forge  *fake.Forge
	ch     *fake.Channel
	// res is the resolution every run gets; tests mutate it (Commands,
	// GitPass, …) the way v1 tests mutated engine.Config.
	res *Resolution
	// settings is mutable so tests can shrink limits mid-test.
	settings *varSettings
}

// varSettings is a mutable SettingsSource for tests.
type varSettings struct{ L Limits }

func (v *varSettings) Limits(context.Context) (Limits, error) { return v.L, nil }

func mustFlow(t *testing.T, doc string) *flow.Flow {
	t.Helper()
	f, err := flow.Parse("test.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func newHarness(t *testing.T, flows ...*flow.Flow) *harness {
	t.Helper()
	h := &harness{
		store:  event.NewMemStore(),
		envp:   fake.NewEnvProvisioner(),
		runner: &fake.AgentRunner{},
		forge:  &fake.Forge{},
		ch:     &fake.Channel{},
	}
	fm := flow.MapSource{}
	for _, f := range flows {
		fm[f.Name] = f
	}
	h.res = &Resolution{
		ProjectID:    testProject,
		ProjectName:  "test",
		RepoRef:      ports.RepoRef{URL: "https://gitlab.local/g/p.git", DefaultBranch: "main", Locator: "1"},
		AgentsMDPath: "AGENTS.md",
		DefaultFlow:  flows[0].Name,
		Commands:     map[string]string{"setup": "./scripts/setup.sh", "verify": "make lint && make test"},
		ProviderEnv:  map[string]string{"ANTHROPIC_API_KEY": "sk-test"},
		// The project's agent library (DESIGN3 §1): superset of every agent
		// the test flows reference.
		AgentNames:  []string{"builder", "fallback", "planner", "redactor", "reviewer", "sleeper"},
		AgentHashes: map[string]string{"builder": "hash-builder", "planner": "hash-planner"},
		HarnessKind: "fake",
		Envp:        h.envp,
		Runner:      h.runner,
		Forge:       h.forge,
	}
	h.settings = &varSettings{L: Limits{}.withDefaults()}
	cfg := Config{MRPollInterval: 5 * time.Millisecond}
	h.e = New(cfg, h.store, StaticResolver{R: h.res}, fm, h.settings, []ports.NotificationChannel{h.ch})
	t.Cleanup(h.e.Shutdown)
	return h
}

// writeFile writes into a fake env through the port (lock-safe).
func (h *harness) writeFile(t *testing.T, env ports.EnvID, path, content string) {
	t.Helper()
	if err := h.envp.WriteFile(context.Background(), env, path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitPing polls until a pending ping of the given type exists, skipping
// any listed (already-answered) ping ids.
func (h *harness) waitPing(t *testing.T, ptype string, except ...string) *Ping {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
	pings:
		for _, p := range h.e.PendingPings() {
			if p.Type != ptype {
				continue
			}
			for _, ex := range except {
				if p.ID == ex {
					continue pings
				}
			}
			return p
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no pending %s ping appeared", ptype)
	return nil
}

func (h *harness) events(t *testing.T, runID string) []event.Event {
	t.Helper()
	evs, err := h.store.Events(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func (h *harness) view(t *testing.T, runID string) run.View {
	return run.Reduce(h.events(t, runID))
}

func eventTypes(evs []event.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = string(e.Type)
	}
	return out
}

func countType(evs []event.Event, ty event.Type) int {
	n := 0
	for _, e := range evs {
		if e.Type == ty {
			n++
		}
	}
	return n
}

func findPayload[T event.Payload](evs []event.Event) (T, bool) {
	for _, e := range evs {
		if p, ok := e.Payload.(T); ok {
			return p, true
		}
	}
	var zero T
	return zero, false
}

// agentScript dispatches RunFunc by agent name.
func (h *harness) agentScript(fns map[string]func(call fake.RunCall) (ports.AgentResult, error)) {
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (ports.AgentResult, error) {
		if fn, ok := fns[call.Spec.Agent]; ok {
			return fn(call)
		}
		return ports.AgentResult{TurnsUsed: 1}, nil
	}
}

const loopFlowDoc = `name: loop
defaults: { model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: build, type: agent, agent: builder }
  - { id: test,  type: command, run_ref: verify, on_fail: { to: build, max: 5, feed: output } }
`

// --- scenarios --------------------------------------------------------------

func TestHappyPathFullV1Flow(t *testing.T) {
	v1, err := flow.Load("../flow/testdata/v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, v1)
	h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"planner": func(c fake.RunCall) (ports.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/plan.md", "## Plan\nsteps")
			return ports.AgentResult{TurnsUsed: 5, Usage: ports.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.10}}, nil
		},
		"builder": func(c fake.RunCall) (ports.AgentResult, error) {
			return ports.AgentResult{TurnsUsed: 20, Usage: ports.Usage{CostUSD: 0.50}}, nil
		},
		"reviewer": func(c fake.RunCall) (ports.AgentResult, error) {
			return ports.AgentResult{Verdict: &ports.Verdict{Approve: true}, TurnsUsed: 8, Usage: ports.Usage{CostUSD: 0.20}}, nil
		},
		"redactor": func(c fake.RunCall) (ports.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/mr.md", "# Add login form\n\nSummary of changes")
			return ports.AgentResult{TurnsUsed: 3, Usage: ports.Usage{CostUSD: 0.05}}, nil
		},
	})

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "Add login", Goal: "Users must log in"}, "v1", testProject)
	if err != nil {
		t.Fatal(err)
	}

	// plan_gate: approve.
	p := h.waitPing(t, "plan_approval")
	if p.PayloadRef != "artifacts/plan.md" {
		t.Errorf("plan gate payload = %q", p.PayloadRef)
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}

	// final_gate: auto-completes when the MR merges.
	fp := h.waitPing(t, "final_review")
	if !strings.HasPrefix(fp.PayloadRef, "https://gitlab.local/mr/") {
		t.Errorf("final gate payload = %q, want MR URL", fp.PayloadRef)
	}
	h.forge.SetMRState(1, ports.MRMerged)
	h.e.Wait(runID)

	v := h.view(t, runID)
	if v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	evs := h.events(t, runID)

	if got := countType(evs, event.TypeStageStarted); got != 9 {
		t.Errorf("stage_started count = %d, want 9", got)
	}
	rc, ok := findPayload[*event.RunCompleted](evs)
	if !ok {
		t.Fatal("no run_completed")
	}
	if rc.UnwantedPings != 0 {
		t.Errorf("unwanted_pings = %d, want 0", rc.UnwantedPings)
	}
	if rc.TotalTurns != 36 {
		t.Errorf("total_turns = %d, want 36", rc.TotalTurns)
	}
	if rc.TotalCostUSD < 0.84 || rc.TotalCostUSD > 0.86 {
		t.Errorf("total_cost = %v, want 0.85", rc.TotalCostUSD)
	}

	if len(h.forge.MRs) != 1 {
		t.Fatalf("MRs = %d", len(h.forge.MRs))
	}
	mr := h.forge.MRs[0]
	if mr.Title != "Add login form" {
		t.Errorf("MR title = %q (should come from mr.md heading)", mr.Title)
	}
	if !strings.HasPrefix(mr.Branch, "apothecary/") || !strings.Contains(mr.Branch, "add-login") {
		t.Errorf("MR branch = %q", mr.Branch)
	}
	if got := len(h.ch.Sent()); got != 2 {
		t.Errorf("notifications = %d, want 2 (both gates)", got)
	}

	// The builder prompt embeds the plan artifact and the task.
	var builderPrompt string
	for _, c := range h.runner.RunCalls {
		if c.Spec.Agent == "builder" {
			builderPrompt = c.In.Prompt
		}
	}
	for _, want := range []string{"# TASK\nAdd login", "Users must log in", "# ARTIFACT: artifacts/plan.md", "## Plan"} {
		if !strings.Contains(builderPrompt, want) {
			t.Errorf("builder prompt missing %q:\n%s", want, builderPrompt)
		}
	}
}

func TestTestFailBuildLoopThenPass(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	verifyRuns := 0
	h.envp.ExecFunc = func(env *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "make lint") {
			verifyRuns++
			if verifyRuns <= 2 {
				return ports.ExecResult{ExitCode: 1, Output: "FAIL: TestLogin assertion mismatch"}, nil
			}
		}
		return ports.ExecResult{ExitCode: 0}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	evs := h.events(t, runID)
	if got := countType(evs, event.TypeEdgeTaken); got != 2 {
		t.Errorf("edge_taken = %d, want 2", got)
	}

	var builds []string
	for _, c := range h.runner.RunCalls {
		builds = append(builds, c.In.Prompt)
	}
	if len(builds) != 3 {
		t.Fatalf("builder ran %d times, want 3", len(builds))
	}
	if strings.Contains(builds[0], "PREVIOUS ATTEMPT FEEDBACK") {
		t.Error("first attempt must not carry feedback")
	}
	for i, iter := range []int{2, 3} {
		p := builds[i+1]
		want := fmt.Sprintf("# PREVIOUS ATTEMPT FEEDBACK (iteration %d of 6)", iter)
		if !strings.Contains(p, want) || !strings.Contains(p, "FAIL: TestLogin") {
			t.Errorf("attempt %d prompt missing %q + command output:\n%s", iter, want, p)
		}
	}
}

func TestReviewRejectLoopThenApprove(t *testing.T) {
	doc := `name: rev
defaults: { model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: build,  type: agent, agent: builder }
  - { id: review, type: agent, agent: reviewer, verdict: true,
      on_reject: { to: build, max: 3, feed: comments } }
`
	h := newHarness(t, mustFlow(t, doc))
	reviews := 0
	h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"reviewer": func(c fake.RunCall) (ports.AgentResult, error) {
			reviews++
			if reviews == 1 {
				return ports.AgentResult{Verdict: &ports.Verdict{Approve: false,
					Comments: []string{"repository leaks into domain layer", "missing test for empty cart"}}}, nil
			}
			return ports.AgentResult{Verdict: &ports.Verdict{Approve: true}}, nil
		},
	})

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "rev", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	evs := h.events(t, runID)
	var verdicts []*event.VerdictReceived
	for _, e := range evs {
		if p, ok := e.Payload.(*event.VerdictReceived); ok {
			verdicts = append(verdicts, p)
		}
	}
	if len(verdicts) != 2 || verdicts[0].Approve || verdicts[0].NComments != 2 || !verdicts[1].Approve {
		t.Errorf("verdicts = %+v", verdicts)
	}

	second := h.runner.RunCalls[2].In.Prompt // build1, review1, build2, review2
	if !strings.Contains(second, "repository leaks into domain layer") ||
		!strings.Contains(second, "(iteration 2 of 4)") {
		t.Errorf("rebuild prompt missing review comments:\n%s", second)
	}
}

func TestEdgeExhaustionAbandons(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.envp.ExecFunc = func(env *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "make lint") {
			return ports.ExecResult{ExitCode: 2, Output: "boom"}, nil
		}
		return ports.ExecResult{ExitCode: 0}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if v := h.view(t, runID); v.State != run.Abandoned {
		t.Fatalf("state = %s", v.State)
	}
	evs := h.events(t, runID)
	ra, _ := findPayload[*event.RunAbandoned](evs)
	if ra.StalledAt != "test" || ra.Edge != "on_fail" {
		t.Errorf("run_abandoned = %+v", ra)
	}
	if got := countType(evs, event.TypeEdgeTaken); got != 5 {
		t.Errorf("edge_taken = %d, want 5 (max)", got)
	}
	if got := len(h.runner.RunCalls); got != 6 {
		t.Errorf("build attempts = %d, want 6", got)
	}
}

func TestPingMidBuildAnswered(t *testing.T) {
	doc := `name: ping
defaults: { model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: build, type: agent, agent: builder }
`
	h := newHarness(t, mustFlow(t, doc))
	runIDCh := make(chan string, 1)
	gotAnswer := make(chan string, 1)
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (ports.AgentResult, error) {
		id := <-runIDCh
		p, err := h.e.RaisePing(id, "clarification", "which database?", "postgres or sqlite")
		if err != nil {
			return ports.AgentResult{}, err
		}
		ans, _ := p.Await(context.Background())
		gotAnswer <- ans.Answer
		return ports.AgentResult{TurnsUsed: 2}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "ping", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID

	p := h.waitPing(t, "clarification")
	if p.Question != "which database?" {
		t.Errorf("question = %q", p.Question)
	}
	// While parked the run reads waiting_on_human.
	if v := h.view(t, runID); v.State != run.WaitingOnHuman {
		t.Errorf("state while parked = %s", v.State)
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "use postgres"}); err != nil {
		t.Fatal(err)
	}
	if got := <-gotAnswer; got != "use postgres" {
		t.Errorf("agent received %q", got)
	}
	h.e.Wait(runID)

	evs := h.events(t, runID)
	pa, ok := findPayload[*event.PingAnswered](evs)
	if !ok || pa.Answer != "text" || pa.Feedback != "use postgres" {
		t.Errorf("ping_answered = %+v", pa)
	}
	rc, _ := findPayload[*event.RunCompleted](evs)
	if rc.UnwantedPings != 1 {
		t.Errorf("unwanted_pings = %d, want 1", rc.UnwantedPings)
	}
	if v := h.view(t, runID); v.State != run.Completed {
		t.Errorf("final state = %s", v.State)
	}
}

func TestPlanGateRejectWithFeedback(t *testing.T) {
	doc := `name: gated
defaults: { model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: plan,      type: agent, agent: planner, output: artifacts/plan.md }
  - { id: plan_gate, type: human_gate, ping: plan_approval, payload: artifacts/plan.md,
      on_reject: { to: plan, max: 3, feed: feedback } }
`
	h := newHarness(t, mustFlow(t, doc))
	h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"planner": func(c fake.RunCall) (ports.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/plan.md", "plan v?")
			return ports.AgentResult{TurnsUsed: 1}, nil
		},
	})

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "gated", testProject)
	if err != nil {
		t.Fatal(err)
	}

	p1 := h.waitPing(t, "plan_approval")
	if err := h.e.AnswerPing(p1.ID, PingAnswer{Answer: "reject", Feedback: "add a rollback step and cover the migration with tests"}); err != nil {
		t.Fatal(err)
	}
	p2 := h.waitPing(t, "plan_approval", p1.ID) // re-raised after replanning
	if err := h.e.AnswerPing(p2.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	if len(h.runner.RunCalls) != 2 {
		t.Fatalf("planner ran %d times, want 2", len(h.runner.RunCalls))
	}
	rerun := h.runner.RunCalls[1].In.Prompt
	if !strings.Contains(rerun, "add a rollback step") || !strings.Contains(rerun, "(iteration 2 of 4)") {
		t.Errorf("replan prompt missing human feedback:\n%s", rerun)
	}
	// Gate rejection must not count as an unwanted ping.
	rc, _ := findPayload[*event.RunCompleted](h.events(t, runID))
	if rc.UnwantedPings != 0 {
		t.Errorf("unwanted_pings = %d, want 0", rc.UnwantedPings)
	}
}

func TestBudgetExceededAbandons(t *testing.T) {
	doc := `name: pricey
defaults: { model: anthropic/claude-sonnet-4-6 }
budget: { max_cost_usd: 1.0 }
stages:
  - { id: build, type: agent, agent: builder }
  - { id: test,  type: command, run_ref: verify, on_fail: { to: build, max: 10, feed: output } }
`
	h := newHarness(t, mustFlow(t, doc))
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (ports.AgentResult, error) {
		return ports.AgentResult{TurnsUsed: 5, Usage: ports.Usage{CostUSD: 0.60}}, nil
	}
	h.envp.ExecFunc = func(env *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "make lint") {
			return ports.ExecResult{ExitCode: 1, Output: "fail"}, nil
		}
		return ports.ExecResult{ExitCode: 0}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "pricey", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if v := h.view(t, runID); v.State != run.Abandoned {
		t.Fatalf("state = %s", v.State)
	}
	ra, _ := findPayload[*event.RunAbandoned](h.events(t, runID))
	if !strings.Contains(ra.Reason, "budget exceeded") {
		t.Errorf("reason = %q", ra.Reason)
	}
	if got := len(h.runner.RunCalls); got != 2 {
		t.Errorf("build ran %d times, want 2 (0.6 + 0.6 > 1.0)", got)
	}
}

// TestCrashRecoveryResumesViaResume simulates kill -9 mid-build: the event
// log ends at an open stage_started for build iteration 2, with a session
// recorded from iteration 1. Recovery must Resume, not re-Run, and finish.
func TestCrashRecoveryResumesViaResume(t *testing.T) {
	fl := mustFlow(t, loopFlowDoc)
	h := newHarness(t, fl)

	envID, err := h.envp.Provision(context.Background(), ports.EnvSpec{RunID: "r-crash"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	usage := &event.Usage{InputTokens: 100, OutputTokens: 50, CostUSD: 0.30}
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: "loop", ProjectID: testProject, FlowHash: fl.Hash, Branch: "apothecary/r-crash-t"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: string(envID)},
		&event.StageStarted{StageID: "build", Iteration: 1},
		&event.StageCompleted{StageID: "build", TurnsUsed: 10, Usage: usage, SessionID: "sess-before-crash"},
		&event.StageStarted{StageID: "test", Iteration: 1},
		&event.StageFailed{StageID: "test", Reason: event.ReasonExitNonzero},
		&event.EdgeTaken{From: "test", To: "build", Edge: "on_fail", Count: 1, Max: 5},
		&event.StageStarted{StageID: "build", Iteration: 2},
		// kill -9 here
	} {
		if _, err := h.store.Append(ctx, "r-crash", p); err != nil {
			t.Fatal(err)
		}
	}

	h.runner.ResumeFunc = func(_ context.Context, call fake.ResumeCall) (ports.AgentResult, error) {
		return ports.AgentResult{Session: call.Session, TurnsUsed: 4, Usage: ports.Usage{CostUSD: 0.10}}, nil
	}
	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-crash")

	if v := h.view(t, "r-crash"); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, "r-crash")))
	}
	if len(h.runner.ResumeCalls) != 1 || h.runner.ResumeCalls[0].Session != "sess-before-crash" {
		t.Fatalf("resume calls = %+v, want 1 with recorded session", h.runner.ResumeCalls)
	}
	if len(h.runner.RunCalls) != 0 {
		t.Errorf("re-Run called %d times; recovery must Resume", len(h.runner.RunCalls))
	}
	// No duplicate stage_started for the resumed attempt; no re-provision.
	evs := h.events(t, "r-crash")
	builds := 0
	for _, e := range evs {
		if p, ok := e.Payload.(*event.StageStarted); ok && p.StageID == "build" {
			builds++
		}
	}
	if builds != 2 {
		t.Errorf("build stage_started = %d, want 2", builds)
	}
	if countType(evs, event.TypeEnvRequested) != 1 {
		t.Error("recovery re-provisioned an existing running env")
	}
	// Budgets are reconstructed from events: 0.30 replayed + 0.10 resumed.
	rc, _ := findPayload[*event.RunCompleted](evs)
	if rc.TotalCostUSD < 0.39 || rc.TotalCostUSD > 0.41 || rc.TotalTurns != 14 {
		t.Errorf("totals after recovery = %+v (want cost 0.40, turns 14)", rc)
	}
}

func TestCrashRecoveryWithoutSessionReRuns(t *testing.T) {
	fl := mustFlow(t, loopFlowDoc)
	h := newHarness(t, fl)
	envID, _ := h.envp.Provision(context.Background(), ports.EnvSpec{RunID: "r-c2"})
	ctx := context.Background()
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: "loop", ProjectID: testProject, FlowHash: fl.Hash, Branch: "b"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: string(envID)},
		&event.StageStarted{StageID: "build", Iteration: 1},
		// kill -9 here: no session ever recorded
	} {
		if _, err := h.store.Append(ctx, "r-c2", p); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-c2")

	if v := h.view(t, "r-c2"); v.State != run.Completed {
		t.Fatalf("state = %s", v.State)
	}
	if len(h.runner.RunCalls) != 1 || len(h.runner.ResumeCalls) != 0 {
		t.Errorf("runs=%d resumes=%d, want re-Run once", len(h.runner.RunCalls), len(h.runner.ResumeCalls))
	}
}

func TestStageTimeoutTakesOnFail(t *testing.T) {
	doc := `name: slow
defaults: { model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: a, type: agent, agent: sleeper, timeout: 30ms, on_fail: { to: b, max: 1 } }
  - { id: b, type: agent, agent: fallback }
`
	h := newHarness(t, mustFlow(t, doc))
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (ports.AgentResult, error) {
		if call.Spec.Agent == "sleeper" {
			<-ctx.Done() // emulate a harness hanging until killed
			return ports.AgentResult{}, ctx.Err()
		}
		return ports.AgentResult{TurnsUsed: 1}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "slow", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	evs := h.events(t, runID)
	sf, ok := findPayload[*event.StageFailed](evs)
	if !ok || sf.StageID != "a" || sf.Reason != event.ReasonTimeout {
		t.Errorf("stage_failed = %+v, want a/timeout", sf)
	}
	et, ok := findPayload[*event.EdgeTaken](evs)
	if !ok || et.From != "a" || et.To != "b" {
		t.Errorf("edge_taken = %+v", et)
	}
}

func TestAbortMarksRunFailed(t *testing.T) {
	doc := `name: hang
defaults: { model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: a, type: agent, agent: sleeper }
`
	h := newHarness(t, mustFlow(t, doc))
	started := make(chan struct{})
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (ports.AgentResult, error) {
		close(started)
		<-ctx.Done()
		return ports.AgentResult{}, ctx.Err()
	}

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "hang", testProject)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := h.e.Abort(runID); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	v := h.view(t, runID)
	if v.State != run.Failed {
		t.Fatalf("state = %s", v.State)
	}
	rf, _ := findPayload[*event.RunFailed](h.events(t, runID))
	if rf.Reason != "aborted" {
		t.Errorf("reason = %q", rf.Reason)
	}
}

func TestEnvFailureAfterRetry(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.envp.ProvisionErrs = []error{fmt.Errorf("docker daemon unreachable"), fmt.Errorf("still unreachable")}

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if v := h.view(t, runID); v.State != run.Failed {
		t.Fatalf("state = %s", v.State)
	}
	ef, _ := findPayload[*event.EnvFailed](h.events(t, runID))
	if !strings.Contains(ef.Error, "unreachable") {
		t.Errorf("env_failed = %+v", ef)
	}
}

func TestSetupFailureIsEnvFailed(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.envp.ExecFunc = func(env *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "setup.sh") {
			return ports.ExecResult{ExitCode: 1, Output: "npm install failed: ENOSPC"}, nil
		}
		return ports.ExecResult{ExitCode: 0}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if v := h.view(t, runID); v.State != run.Failed {
		t.Fatalf("state = %s", v.State)
	}
	ef, _ := findPayload[*event.EnvFailed](h.events(t, runID))
	if !strings.Contains(ef.Error, "ENOSPC") {
		t.Errorf("env_failed should carry setup output, got %+v", ef)
	}
	if len(h.runner.RunCalls) != 0 {
		t.Error("no agent may run when setup failed")
	}
}

func TestStageStartedCarriesAgentHash(t *testing.T) {
	// DESIGN3 §1: stage events pin the agent-library content version so
	// analytics can correlate outcomes with prompt versions.
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	found := false
	for _, ev := range h.events(t, runID) {
		if ss, ok := ev.Payload.(*event.StageStarted); ok && ss.StageID == "build" {
			found = true
			if ss.Agent != "builder" || ss.AgentHash != "hash-builder" {
				t.Errorf("stage_started agent = %q hash = %q", ss.Agent, ss.AgentHash)
			}
		}
	}
	if !found {
		t.Fatal("no stage_started for build")
	}
}

func TestPreflightRefusalsNameTheMissingItem(t *testing.T) {
	// v3: agents live in the project library, not the repo — a flow naming
	// an agent the project lacks is refused by name.
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.res.AgentNames = []string{"planner"} // flow needs builder too
	_, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "loop", testProject)
	if err == nil || !strings.Contains(err.Error(), `agent "builder" not found in the project's agent library`) {
		t.Errorf("err = %v, want missing-agent refusal", err)
	}

	h2 := newHarness(t, mustFlow(t, loopFlowDoc))
	delete(h2.res.Commands, "verify")
	_, err = h2.e.CreateRun(context.Background(), Ticket{Title: "T"}, "loop", testProject)
	if err == nil || !strings.Contains(err.Error(), `run_ref "verify"`) {
		t.Errorf("err = %v, want mention of run_ref verify", err)
	}

	// A flow pinning a foreign harness is refused at preflight (DESIGN2
	// §6.3), naming stage and kinds.
	h4 := newHarness(t, mustFlow(t, `name: pinned
defaults: { model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: build, type: agent, agent: builder, harness: codex }
`))
	_, err = h4.e.CreateRun(context.Background(), Ticket{Title: "T"}, "pinned", testProject)
	if err == nil || !strings.Contains(err.Error(), `pins harness "codex"`) {
		t.Errorf("err = %v, want harness pin refusal", err)
	}

	// No events may exist after refusals.
	for _, hh := range []*harness{h, h2, h4} {
		ids, _ := hh.store.RunIDs(context.Background())
		if len(ids) != 0 {
			t.Errorf("refused runs left events: %v", ids)
		}
	}
}

// commands.setup is OPTIONAL in v2 (DESIGN2 §8): a project without one
// runs fine — bootstrap step 6 is simply skipped.
func TestSetupIsOptional(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	delete(h.res.Commands, "setup")
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatalf("setup-less project refused: %v", err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, want completed; events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	// No exec may have run the (absent) setup script.
	for _, c := range h.envp.ExecCalls {
		if strings.Contains(c.Cmd.Script, "setup.sh") {
			t.Errorf("setup script executed despite being unset: %q", c.Cmd.Script)
		}
	}
}

func TestOutputContractViolationFailsStage(t *testing.T) {
	doc := `name: contract
defaults: { model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: plan, type: agent, agent: planner, output: artifacts/plan.md }
`
	h := newHarness(t, mustFlow(t, doc))
	// Planner never writes the declared artifact.
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "contract", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if v := h.view(t, runID); v.State != run.Abandoned {
		t.Fatalf("state = %s, want abandoned (no on_fail)", v.State)
	}
	sf, _ := findPayload[*event.StageFailed](h.events(t, runID))
	if sf.Reason != event.ReasonOutputContract {
		t.Errorf("reason = %q", sf.Reason)
	}
}

// TestExplicitSuccessRouting proves the engine follows explicit on_success
// edges (DESIGN3 §2.1): the entry stage skips over a fallback stage that
// is only reachable via on_fail and itself completes the run early.
func TestExplicitSuccessRouting(t *testing.T) {
	branchDoc := `name: branch
defaults: { model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: probe,   type: command, run: probe.sh, on_success: finish, on_fail: { to: repair, max: 1 } }
  - { id: repair,  type: command, run: repair.sh, on_success: done }
  - { id: finish,  type: command, run: finish.sh }
`
	// Happy path: probe succeeds -> finish runs, repair never does.
	h := newHarness(t, mustFlow(t, branchDoc))
	h.envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		return ports.ExecResult{ExitCode: 0, Output: cmd.Script}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "branch", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(h.events(t, runID)))
	}
	var started []string
	for _, ev := range h.events(t, runID) {
		if ss, ok := ev.Payload.(*event.StageStarted); ok {
			started = append(started, ss.StageID)
		}
	}
	if len(started) != 2 || started[0] != "probe" || started[1] != "finish" {
		t.Errorf("stages run = %v, want [probe finish]", started)
	}

	// Failure path: probe fails -> repair runs and completes the run early;
	// finish never runs.
	h2 := newHarness(t, mustFlow(t, branchDoc))
	h2.envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "probe.sh") {
			return ports.ExecResult{ExitCode: 1, Output: "probe broke"}, nil
		}
		return ports.ExecResult{ExitCode: 0}, nil
	}
	runID2, err := h2.e.CreateRun(context.Background(), Ticket{Title: "T"}, "branch", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h2.e.Wait(runID2)
	if v := h2.view(t, runID2); v.State != run.Completed {
		t.Fatalf("failure path state = %s: %v", v.State, eventTypes(h2.events(t, runID2)))
	}
	started = nil
	for _, ev := range h2.events(t, runID2) {
		if ss, ok := ev.Payload.(*event.StageStarted); ok {
			started = append(started, ss.StageID)
		}
	}
	if len(started) != 2 || started[0] != "probe" || started[1] != "repair" {
		t.Errorf("failure-path stages = %v, want [probe repair]", started)
	}
}
