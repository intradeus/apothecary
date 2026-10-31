// §16 M1 scenario tests: the whole engine drives against the in-memory
// fakes — the proof the port boundaries are real.
package engine

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"apothecary/internal/adapters/fake"
	askstore "apothecary/internal/ask"
	"apothecary/internal/flow"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workspace/portrelay"
	"apothecary/internal/workspace/runports"
	"apothecary/internal/workspace/services"
	"apothecary/internal/workspace/wsinfo"
)

// testProject is the project id served by the harness's StaticResolver.
const testProject = "prj-test"

type harness struct {
	e *Engine
	// live is e for what may outlast a crashed engine: an agent's goroutine
	// keeps running after Shutdown, while the test swaps e.
	live     atomic.Pointer[Engine]
	flows    flow.MapSource
	store    *event.MemStore
	envp     *fake.EnvProvisioner
	runner   *fake.AgentRunner
	repoHost *fake.RepoHost
	ch       *fake.Channel
	// res is the resolution every run gets; tests mutate it (Commands,
	// GitPass, …) the way v1 tests mutated engine.Config.
	res *resolution.Resolution
	// settings is mutable so tests can shrink limits mid-test.
	settings *varSettings
	// askSvc is the run Ask panes, built on first use (askPane).
	askSvc *askstore.Service
	// rp is port exposure, wired as the daemon wires it (the engine's
	// RunPorts and first teardown hook); relay-less until setRelay.
	rp *runports.Service
}

// harnessOpt adjusts what a harness's engine is built from; it runs once the
// harness's fakes and resolution exist, before the engine does.
type harnessOpt func(h *harness, d *Deps)

// varSettings is a mutable SettingsSource for tests.
type varSettings struct{ L Limits }

func (v *varSettings) Limits(context.Context) (Limits, error) { return v.L, nil }

// testAgentSpecs is the scenario suite's agent library: the same contracts the
// built-ins carry (FLOWLANG §4), so test flows bind inputs by name. Every
// input is optional so a scenario can use a bare `agent: builder` stage when
// the binding is not what it is testing — and `rejection`/`failure` (the
// edge-carried values) must be optional anyway. An agent's outputs are the
// stage's to place: a stage running planner, committer, reviewer or redactor
// says where its output goes.
func testAgentSpecs() map[string]*agentspec.Spec {
	types := map[string]string{
		"ticket": agentspec.TypeTicket, "plan": agentspec.TypeFile, "changes": agentspec.TypeChangeset,
		"rejection": agentspec.TypeVerdict, "failure": agentspec.TypeFailure,
	}
	opt := func(names ...string) []agentspec.InputDecl {
		var in []agentspec.InputDecl
		for _, n := range names {
			in = append(in, agentspec.InputDecl{Name: n, Description: n + " for the stage", Type: types[n], Optional: true})
		}
		return in
	}
	return map[string]*agentspec.Spec{
		"planner": {Name: "planner", Inputs: opt("ticket", "rejection", "failure"),
			Outputs: []agentspec.OutputDecl{{Name: "plan", Type: agentspec.TypeFile, Description: "the implementation plan"}}},
		"builder": {Name: "builder", Inputs: opt("ticket", "plan", "rejection", "failure")},
		"committer": {Name: "committer", Inputs: opt("ticket", "plan", "failure"),
			Outputs: []agentspec.OutputDecl{{Name: "commit", Type: agentspec.TypeFile, Description: "the commit subject"}}},
		"reviewer": {Name: "reviewer", Inputs: opt("ticket", "plan", "changes", "failure"),
			Outputs: []agentspec.OutputDecl{{Name: "verdict", Type: agentspec.TypeVerdict,
				Description: "APPROVE or REJECT with comments"}}},
		"redactor": {Name: "redactor", Inputs: opt("ticket", "changes", "failure"),
			Outputs: []agentspec.OutputDecl{{Name: "mr", Type: agentspec.TypeFile, Description: "the merge request description"}}},
		// fallback and sleeper deliberately have no contract: stages using them
		// declare nothing and nothing is enforced.
		"fallback": {Name: "fallback"},
		"sleeper":  {Name: "sleeper"},
	}
}

func mustFlow(t *testing.T, doc string) *flow.Flow {
	t.Helper()
	return mustFlowWith(t, doc, testAgentSpecs())
}

// mustFlowWith parses a scenario document after splicing in the agent
// definitions it runs. Since FLOWLANG v3 a flow carries its agents, but a
// scenario is almost never about the contracts — so the suite injects its
// standard library here and each document keeps declaring only stages. A
// document that brings its own `agents:` block is left alone.
func mustFlowWith(t *testing.T, doc string, specs map[string]*agentspec.Spec) *flow.Flow {
	t.Helper()
	f, err := flow.Parse("test.yaml", []byte(withAgents(doc, specs)))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// withAgents renders specs as a v3 `agents:` block ahead of `stages:`.
func withAgents(doc string, specs map[string]*agentspec.Spec) string {
	doc = strings.Replace(doc, "version: 2", "version: 5", 1)
	if strings.Contains(doc, "\nagents:") {
		return doc
	}
	names := make([]string, 0, len(specs))
	for n := range specs {
		names = append(names, n)
	}
	sort.Strings(names)
	list := make([]agentspec.Spec, 0, len(names))
	for _, n := range names {
		sp := *specs[n]
		sp.Name = n
		if sp.Prompt == "" {
			sp.Prompt = "You are the " + n + " of the test factory."
		}
		list = append(list, sp)
	}
	y, err := yaml.Marshal(map[string]any{"agents": list})
	if err != nil {
		panic(err)
	}
	// Ahead of the document's sequences, else its first chain.
	for _, key := range []string{"\nsequences:", "\nmain:", "\nwatchers:", "\noutcomes:"} {
		if i := strings.Index(doc, key); i >= 0 {
			return doc[:i+1] + string(y) + doc[i+1:]
		}
	}
	return doc
}

func newHarness(t *testing.T, flows ...*flow.Flow) *harness {
	t.Helper()
	return newHarnessWith(t, nil, flows...)
}

// newHarnessWith is newHarness with the engine's Deps adjusted by opt.
func newHarnessWith(t *testing.T, opt harnessOpt, flows ...*flow.Flow) *harness {
	t.Helper()
	return newHarnessOnWith(t, nil, opt, flows...)
}

// newHarnessOn is newHarness with the engine reading and writing through
// `store` — a wrapper over the harness's MemStore (which stays h.store), for
// tests about what the engine asks a store that has more to offer (a runs
// projection). nil is the plain MemStore.
func newHarnessOn(t *testing.T, store func(*event.MemStore) event.Store, flows ...*flow.Flow) *harness {
	t.Helper()
	return newHarnessOnWith(t, store, nil, flows...)
}

func newHarnessOnWith(t *testing.T, store func(*event.MemStore) event.Store, opt harnessOpt, flows ...*flow.Flow) *harness {
	t.Helper()
	fm := flow.MapSource{}
	for _, f := range flows {
		fm[f.Name] = f
	}
	h := newHarnessSourceOn(t, store, fm, opt, flows...)
	h.flows = fm
	return h
}

// flowOf returns a flow the harness serves, for assertions about what the
// document itself declares (agent hashes, contracts).
func (h *harness) flowOf(t *testing.T, name string) *flow.Flow {
	t.Helper()
	f, ok := h.flows[name]
	if !ok {
		t.Fatalf("harness serves no flow %q", name)
	}
	return f
}

// newHarnessSource is newHarness over a caller-supplied flow.Source, for tests
// about what happens when the source itself misbehaves.
func newHarnessSource(t *testing.T, src flow.Source, flows ...*flow.Flow) *harness {
	t.Helper()
	return newHarnessSourceOn(t, nil, src, nil, flows...)
}

func newHarnessSourceOn(t *testing.T, store func(*event.MemStore) event.Store, src flow.Source, opt harnessOpt, flows ...*flow.Flow) *harness {
	t.Helper()
	h := &harness{
		store:    event.NewMemStore(),
		envp:     fake.NewEnvProvisioner(),
		runner:   &fake.AgentRunner{},
		repoHost: &fake.RepoHost{},
		ch:       &fake.Channel{},
	}
	h.res = &resolution.Resolution{
		ProjectID:    testProject,
		ProjectName:  "test",
		RepoRef:      repoport.RepoRef{URL: "https://gitlab.local/g/p.git", DefaultBranch: "main", Locator: "1"},
		AgentsMDPath: "AGENTS.md",
		DefaultFlow:  flows[0].Name,
		Commands:     map[string]string{"verify": "make lint && make test"},
		ProviderEnv:  map[string]string{"ANTHROPIC_API_KEY": "sk-test"},
		HarnessKind:  "fake",
		Envp:         h.envp,
		Runner:       h.runner,
		RepoHost:     h.repoHost,
		// The daemon always wires a tracker writer; whether a scenario's
		// ticket has a tracker to write to is the ticket's business (a
		// ticket without one skips those stages). trackerHarness swaps in
		// the tracker it asserts on.
		TicketWriter: func(context.Context, string) (trackerport.TicketWriter, error) { return newTracker(), nil },
	}
	h.settings = &varSettings{L: Limits{}.withDefaults()}
	cfg := Config{MRPollInterval: 5 * time.Millisecond}
	var es event.Store = h.store
	if store != nil {
		es = store(h.store)
	}
	h.rp = h.ports(nil)
	d := Deps{
		Config: cfg, Store: es, Resolver: resolution.StaticResolver{R: h.res}, Flows: src,
		Settings: h.settings, Channels: []notifyport.NotificationChannel{h.ch},
		RunPorts: h.rp, Teardown: []EnvTeardown{h.rp},
	}
	if opt != nil {
		opt(h, &d)
	}
	h.e = New(d)
	registerGolden(t, h)
	// Registered before Shutdown, so it runs after: every run has stopped.
	t.Cleanup(func() { checkFoldAfter(t, h.store) })
	t.Cleanup(h.e.Shutdown)
	return h
}

// workspaces is the workspace service over the harness's store and
// resolution, sharing the engine's env state (the API's view of a run's
// workspace).
func (h *harness) workspaces() *wsinfo.Service {
	return wsinfo.New(&run.Reader{Store: h.store}, resolution.StaticResolver{R: h.res}, h.e.envState)
}

// askPane is the Ask pane service over the harness's store, resolution and
// engine — one per harness, so a pane's busy state carries across calls.
func (h *harness) askPane() *askstore.Service {
	if h.askSvc == nil {
		h.askSvc = askstore.New(&run.Reader{Store: h.store}, resolution.StaticResolver{R: h.res}, h.e, askstore.NewMemStore())
	}
	return h.askSvc
}

// ports is a port-exposure service over the harness's store and resolution,
// through relay (nil: exposure off).
func (h *harness) ports(relay *portrelay.Manager) *runports.Service {
	return runports.New(&run.Reader{Store: h.store}, resolution.StaticResolver{R: h.res}, relay, runports.Config{}, nil)
}

// setRelay turns the harness's port exposure on through relay. Call it
// before any run starts.
func (h *harness) setRelay(relay *portrelay.Manager) *runports.Service {
	h.rp.Relay = relay
	return h.rp
}

// services is the run page's service control over the harness, appending
// through the engine and exposing through its port service.
func (h *harness) services() *services.Control {
	return services.New(&run.Reader{Store: h.store}, resolution.StaticResolver{R: h.res}, h.e, h.e.runPorts, nil)
}

// writeFile writes into a fake env through the port (lock-safe).
func (h *harness) writeFile(t *testing.T, env envport.EnvID, path, content string) {
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
func (h *harness) agentScript(fns map[string]func(call fake.RunCall) (agentport.AgentResult, error)) {
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		if fn, ok := fns[call.Spec.Agent]; ok {
			return fn(call)
		}
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}
}

// loopFlowDoc: the classic retry loop. The builder reads the ticket and
// edge.failure, so a test failure comes back as a named input rather than
// arriving invisibly.
const loopFlowDoc = `name: loop
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder,
      input: { ticket: { from: run.ticket, type: ticket }, failure: { from: edge.failure, type: failure, optional: true } } }
  - { id: test,  type: command, run_ref: verify, on_fail: { to: build, max: 5 } }
`

// --- scenarios --------------------------------------------------------------

func TestHappyPathFullV4Flow(t *testing.T) {
	v1, err := flow.Load("../flow/testdata/v5.yaml")
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, v1)
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"planner": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/plan.md", "## Plan\nsteps")
			return agentport.AgentResult{TurnsUsed: 5, Usage: agentport.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.10}}, nil
		},
		"builder": func(c fake.RunCall) (agentport.AgentResult, error) {
			return agentport.AgentResult{TurnsUsed: 20, Usage: agentport.Usage{CostUSD: 0.50}}, nil
		},
		"committer": func(c fake.RunCall) (agentport.AgentResult, error) {
			// The stage says where the committer's output goes.
			h.writeFile(t, c.Env, "artifacts/commit-subject.md", "feat: add login form")
			return agentport.AgentResult{TurnsUsed: 2, Usage: agentport.Usage{CostUSD: 0.02}}, nil
		},
		"reviewer": func(c fake.RunCall) (agentport.AgentResult, error) {
			// The verdict is an output like any other: the file must exist at
			// the path the stage bound it to, not at a hardcoded one.
			h.writeFile(t, c.Env, "artifacts/review/verdict.json", `{"verdict":"APPROVE","comments":[]}`)
			return agentport.AgentResult{TurnsUsed: 8, Usage: agentport.Usage{CostUSD: 0.20}}, nil
		},
		"redactor": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/mr.md", "# Add login form\n\nSummary of changes")
			return agentport.AgentResult{TurnsUsed: 3, Usage: agentport.Usage{CostUSD: 0.05}}, nil
		},
	})

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "Add login", Goal: "Users must log in"}, "canonical", testProject)
	if err != nil {
		t.Fatal(err)
	}

	// plan_gate: approve.
	p := h.waitPing(t, PingGate)
	if len(p.Files) != 1 || p.Files[0] != "artifacts/plan.md" || p.Link != "" {
		t.Errorf("plan gate payload = files %v link %q", p.Files, p.Link)
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}

	// final_gate: gates are one kind now, so the already-answered one is
	// excluded by id rather than by type.
	fp := h.waitPing(t, PingGate, p.ID)
	if !strings.HasPrefix(fp.Link, "https://gitlab.local/mr/") || len(fp.Files) != 0 {
		t.Errorf("final gate payload = link %q files %v, want MR URL link", fp.Link, fp.Files)
	}
	if err := h.e.AnswerPing(fp.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	v := h.view(t, runID)
	if v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	evs := h.events(t, runID)

	// 14 = the 11 working stages plus the three tracker moves, which skip on
	// a ticket written by hand (started and completed, nothing called).
	if got := countType(evs, event.TypeStageStarted); got != 14 {
		t.Errorf("stage_started count = %d, want 14", got)
	}
	rc, ok := findPayload[*event.RunCompleted](evs)
	if !ok {
		t.Fatal("no run_completed")
	}
	if rc.UnwantedPings != 0 {
		t.Errorf("unwanted_pings = %d, want 0", rc.UnwantedPings)
	}
	if rc.TotalTurns != 38 {
		t.Errorf("total_turns = %d, want 38", rc.TotalTurns)
	}
	if rc.TotalCostUSD < 0.86 || rc.TotalCostUSD > 0.88 {
		t.Errorf("total_cost = %v, want 0.87", rc.TotalCostUSD)
	}

	if len(h.repoHost.MRs) != 1 {
		t.Fatalf("MRs = %d", len(h.repoHost.MRs))
	}
	mr := h.repoHost.MRs[0]
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
	for _, want := range []string{"# TASK\nAdd login", "Users must log in", "# INPUT: plan", "(file: artifacts/plan.md)", "## Plan"} {
		if !strings.Contains(builderPrompt, want) {
			t.Errorf("builder prompt missing %q:\n%s", want, builderPrompt)
		}
	}
}

// TestDeclaredInputAndFeedbackRendering proves the decl-driven prompt shape
// (DESIGN4 §1): an input port renders "# INPUT: <name> — <desc> (file:…)",
// and the agent's declared feedback input renames the retry section.
func TestDeclaredInputAndFeedbackRendering(t *testing.T) {
	doc := `name: declfb
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: plan,  type: agent, harness: $harness, model: $model, agent: planner, on_success: build,
      output: { plan: { to: artifacts/plan.md, type: file } } }
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder, on_success: test,
      input: { plan: { from: artifacts/plan.md, type: file },
               what_broke: { from: edge.failure, type: failure, optional: true } } }
  - { id: test,  type: command, run_ref: verify, on_success: done,
      on_fail: { to: build, max: 3 } }
`
	// The agent names and describes its own retry slot — there is no reserved
	// name, so "what_broke" is as valid as anything else.
	h := newHarness(t, mustFlowWith(t, doc, map[string]*agentspec.Spec{
		"planner": {Outputs: []agentspec.OutputDecl{{Name: "plan", Description: "the plan", Type: agentspec.TypeFile}}},
		"builder": {Inputs: []agentspec.InputDecl{
			{Name: "plan", Description: "the approved implementation plan to execute", Type: agentspec.TypeFile},
			{Name: "what_broke", Description: "what the failing stage printed", Type: agentspec.TypeFailure, Optional: true},
		}},
	}))
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"planner": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/plan.md", "## Plan\nsteps")
			return agentport.AgentResult{}, nil
		},
		"builder": func(fake.RunCall) (agentport.AgentResult, error) { return agentport.AgentResult{}, nil },
	})
	// verify fails the first time (feeds "BOOM" into build), then passes.
	verifyRuns := 0
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "make lint") {
			verifyRuns++
			if verifyRuns == 1 {
				return envport.ExecResult{ExitCode: 1, Output: "BOOM"}, nil
			}
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "declfb", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(h.events(t, runID)))
	}

	var builds []string
	for _, c := range h.runner.RunCalls {
		if c.Spec.Agent == "builder" {
			builds = append(builds, c.In.Prompt)
		}
	}
	if len(builds) != 2 {
		t.Fatalf("builder ran %d times, want 2", len(builds))
	}
	// First pass: named input section, nothing fed yet — the edge.failure binding
	// renders nothing at all rather than an empty section.
	if !strings.Contains(builds[0], "# INPUT: plan — the approved implementation plan to execute (file: artifacts/plan.md)") {
		t.Errorf("build[0] missing named INPUT section:\n%s", builds[0])
	}
	if strings.Contains(builds[0], "# INPUT: what_broke") {
		t.Errorf("build[0] should carry no failure:\n%s", builds[0])
	}
	// Second pass: the failure lands in the slot the agent named for it.
	if !strings.Contains(builds[1], "# INPUT: what_broke — what the failing stage printed (attempt 2 of 4)") {
		t.Errorf("build[1] missing the failure INPUT section:\n%s", builds[1])
	}
	if !strings.Contains(builds[1], "BOOM") {
		t.Errorf("build[1] missing fed output:\n%s", builds[1])
	}
	if strings.Contains(builds[1], "PREVIOUS ATTEMPT FEEDBACK") { //nolint:staticcheck // retired header must never reappear
		t.Errorf("build[1] should use the named feedback header, not the generic one:\n%s", builds[1])
	}
}

func TestTestFailBuildLoopThenPass(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	verifyRuns := 0
	h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "make lint") {
			verifyRuns++
			if verifyRuns <= 2 {
				return envport.ExecResult{ExitCode: 1, Output: "FAIL: TestLogin assertion mismatch"}, nil
			}
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
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
	if strings.Contains(builds[0], "# INPUT: failure") {
		t.Error("first attempt must not carry a failure")
	}
	for i, iter := range []int{2, 3} {
		p := builds[i+1]
		// The builder declares the reserved feedback input, so the retry
		// content lands in a named, described INPUT section.
		want := fmt.Sprintf("# INPUT: failure — failure for the stage (attempt %d of 6)", iter)
		if !strings.Contains(p, want) || !strings.Contains(p, "FAIL: TestLogin") {
			t.Errorf("attempt %d prompt missing %q + command output:\n%s", iter, want, p)
		}
	}
}

func TestReviewRejectLoopThenApprove(t *testing.T) {
	doc := `name: rev
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: build,  type: agent, harness: $harness, model: $model, agent: builder, input: { rejection: { from: edge.verdict, type: verdict, optional: true } } }
  - { id: review, type: agent, harness: $harness, model: $model, agent: reviewer, output: { verdict: { to: artifacts/verdict.json, type: verdict } },
      on_reject: { to: build, max: 3 } }
`
	h := newHarness(t, mustFlow(t, doc))
	reviews := 0
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"reviewer": func(c fake.RunCall) (agentport.AgentResult, error) {
			reviews++
			// The verdict is the stage's output file, at the path its port
			// names — not something the harness reports out of band.
			if reviews == 1 {
				h.writeFile(t, c.Env, "artifacts/verdict.json",
					`{"verdict":"REJECT","comments":["repository leaks into domain layer","missing test for empty cart"]}`)
				return agentport.AgentResult{}, nil
			}
			h.writeFile(t, c.Env, "artifacts/verdict.json", `{"verdict":"APPROVE","comments":[]}`)
			return agentport.AgentResult{}, nil
		},
	})

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "rev", testProject)
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
		!strings.Contains(second, "# INPUT: rejection") || !strings.Contains(second, "(attempt 2 of 4)") {
		t.Errorf("rebuild prompt missing review comments:\n%s", second)
	}
}

func TestEdgeExhaustionAbandons(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "make lint") {
			return envport.ExecResult{ExitCode: 2, Output: "boom"}, nil
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
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
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`
	h := newHarness(t, mustFlow(t, doc))
	runIDCh := make(chan string, 1)
	gotAnswer := make(chan string, 1)
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		id := <-runIDCh
		p, err := h.e.RaisePing(id, "clarification", "which database?", "postgres or sqlite", nil, nil)
		if err != nil {
			return agentport.AgentResult{}, err
		}
		p.Await(context.Background())
		gotAnswer <- ComposeAnswer(p)
		return agentport.AgentResult{TurnsUsed: 2}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ping", testProject)
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
	if err := h.e.AnswerPing(p.ID, textAnswer("use postgres")); err != nil {
		t.Fatal(err)
	}
	if got := <-gotAnswer; got != "use postgres" {
		t.Errorf("agent received %q", got)
	}
	h.e.Wait(runID)

	evs := h.events(t, runID)
	pa, ok := findPayload[*event.PingAnswered](evs)
	if !ok || pa.Answer != "accept" || pa.Content[AnswerField] != "use postgres" {
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

// TestRecoveryAtGateSkipsEnv: a run parked at a human_gate whose workspace was
// reaped must NOT block recovery re-provisioning it — a gate needs no
// workspace. Before the fix, ensureEnv ran up front, saw the env gone, and
// rebuilt the whole workspace before re-arming the gate; meanwhile the
// buffered answers channel accepted answers that nothing drained, so
// AnswerPing "succeeded" while the ping stayed pending forever.
func TestRecoveryAtGateSkipsEnv(t *testing.T) {
	doc := `name: gated
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: plan,      type: agent, harness: $harness, model: $model, agent: planner, output: { plan: { to: artifacts/plan.md, type: file } }, on_success: plan_gate }
  - { id: plan_gate, type: human_gate, question: "Approve the plan?", input: { doc: { from: artifacts/plan.md, type: file } }, on_success: done,
      on_reject: { to: plan, max: 3 } }
`
	fl := mustFlow(t, doc)
	h := newHarness(t, fl)
	ctx := context.Background()

	envID, err := h.envp.Provision(ctx, envport.EnvSpec{RunID: "r-gate"})
	if err != nil {
		t.Fatal(err)
	}
	// Event log of a run parked at plan_gate with an open ping.
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: "gated", ProjectID: testProject, FlowHash: fl.Hash, Branch: "apothecary/r-gate-t"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: string(envID)},
		&event.StageStarted{StageID: "plan", Iteration: 1},
		&event.StageCompleted{StageID: "plan", TurnsUsed: 1},
		&event.StageStarted{StageID: "plan_gate", Iteration: 1},
		&event.PingRaised{PingID: "ping-gate", Type: PingGate, Question: "plan_approval: T", StageID: "plan_gate", Files: []string{"artifacts/plan.md"}},
	} {
		if _, err := h.store.Append(ctx, "r-gate", p); err != nil {
			t.Fatal(err)
		}
	}

	// The workspace is gone (reaped): recovery must not resurrect it for a gate.
	if err := h.envp.Destroy(ctx, envID); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}

	// The gate re-arms and the answer is actually processed (not buffered and
	// dropped) — the run completes on approve.
	p := h.waitPing(t, PingGate)
	if p.ID != "ping-gate" {
		t.Fatalf("re-armed ping id = %q, want ping-gate", p.ID)
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-gate")
	if v := h.view(t, "r-gate"); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, "r-gate")))
	}
	// Never re-provisioned: only the original EnvReady exists (a rebuild would
	// append a second one).
	if n := countType(h.events(t, "r-gate"), event.TypeEnvReady); n != 1 {
		t.Errorf("EnvReady events = %d; the gate path must never provision the workspace", n)
	}
}

func TestPlanGateRejectWithFeedback(t *testing.T) {
	doc := `name: gated
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: plan,      type: agent, harness: $harness, model: $model, agent: planner, output: { plan: { to: artifacts/plan.md, type: file } }, input: { rejection: { from: edge.verdict, type: verdict, optional: true } } }
  - { id: plan_gate, type: human_gate, question: "Approve the plan?", input: { plan: { from: artifacts/plan.md, type: file } },
      on_reject: { to: plan, max: 3 } }
`
	h := newHarness(t, mustFlow(t, doc))
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"planner": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/plan.md", "plan v?")
			return agentport.AgentResult{TurnsUsed: 1}, nil
		},
	})

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "gated", testProject)
	if err != nil {
		t.Fatal(err)
	}

	p1 := h.waitPing(t, PingGate)
	if err := h.e.AnswerPing(p1.ID, PingAnswer{Answer: "reject", Feedback: "add a rollback step and cover the migration with tests"}); err != nil {
		t.Fatal(err)
	}
	p2 := h.waitPing(t, PingGate, p1.ID) // re-raised after replanning
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
	// A human's rejection is a edge.verdict like a reviewer's: same slot, same
	// shape — the planner cannot tell (and need not care) which it was.
	if !strings.Contains(rerun, "add a rollback step") ||
		!strings.Contains(rerun, "# INPUT: rejection") || !strings.Contains(rerun, "(attempt 2 of 4)") {
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
version: 2
parameters: { harness: fake, model: fake/model, max_cost_usd: 1.0 }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
  - { id: test,  type: command, run_ref: verify, on_fail: { to: build, max: 10 } }
`
	h := newHarness(t, mustFlow(t, doc))
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		return agentport.AgentResult{TurnsUsed: 5, Usage: agentport.Usage{CostUSD: 0.60}}, nil
	}
	h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "make lint") {
			return envport.ExecResult{ExitCode: 1, Output: "fail"}, nil
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "pricey", testProject)
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
// log ends inside build iteration 2, with that attempt's session recorded.
// Recovery must Resume, not re-Run, and finish.
func TestCrashRecoveryResumesViaResume(t *testing.T) {
	fl := mustFlow(t, loopFlowDoc)
	h := newHarness(t, fl)

	envID, err := h.envp.Provision(context.Background(), envport.EnvSpec{RunID: "r-crash"})
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
		&event.StageCompleted{StageID: "build", TurnsUsed: 10, Usage: usage, SessionID: "sess-iteration-1"},
		&event.StageStarted{StageID: "test", Iteration: 1},
		&event.StageFailed{StageID: "test", Reason: event.ReasonExitNonzero},
		&event.EdgeTaken{From: "test", To: "build", Edge: "on_fail", Count: 1, Max: 5},
		&event.StageStarted{StageID: "build", Iteration: 2},
		&event.AgentSession{StageID: "build", Iteration: 2, SessionID: "sess-before-crash"},
		// kill -9 here
	} {
		if _, err := h.store.Append(ctx, "r-crash", p); err != nil {
			t.Fatal(err)
		}
	}

	h.runner.ResumeFunc = func(_ context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
		return agentport.AgentResult{Session: call.Session, TurnsUsed: 4, Usage: agentport.Usage{CostUSD: 0.10}}, nil
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
	envID, _ := h.envp.Provision(context.Background(), envport.EnvSpec{RunID: "r-c2"})
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
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: a, type: agent, harness: $harness, model: $model, agent: sleeper, timeout: 30ms, on_fail: { to: b, max: 1 } }
  - { id: b, type: agent, harness: $harness, model: $model, agent: fallback }
`
	h := newHarness(t, mustFlow(t, doc))
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		if call.Spec.Agent == "sleeper" {
			<-ctx.Done() // emulate a harness hanging until killed
			return agentport.AgentResult{}, ctx.Err()
		}
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "slow", testProject)
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

func TestAbortAbandonsRun(t *testing.T) {
	doc := `name: hang
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: a, type: agent, harness: $harness, model: $model, agent: sleeper }
`
	h := newHarness(t, mustFlow(t, doc))
	started := make(chan struct{})
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		close(started)
		<-ctx.Done()
		return agentport.AgentResult{}, ctx.Err()
	}

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "hang", testProject)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := h.e.Abort(runID); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	// A deliberate stop is ABANDONED, not failed (PLAN5 M24): `failed` is
	// reserved for the engine's own faults, so a human saying "stop" is not
	// reported as something breaking.
	v := h.view(t, runID)
	if v.State != run.Abandoned {
		t.Fatalf("state = %s, want abandoned", v.State)
	}
	ra, ok := findPayload[*event.RunAbandoned](h.events(t, runID))
	if !ok || ra.Reason != "aborted by user" {
		t.Errorf("run_abandoned = %+v, want reason %q", ra, "aborted by user")
	}
}

// TestAMergeWhileTheEngineWasDownFiresTheOutcome: a run parked at a final gate whose
// MR merged while the engine was NOT watching (down, or before the gate
// armed) must complete on recovery via the immediate poll — with a huge
// poll interval so a ticker tick can never be the reason it worked.
func TestAMergeWhileTheEngineWasDownFiresTheOutcome(t *testing.T) {
	doc := `name: ac
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: mr,   type: action, action: create_mr, input: { body: { from: repo:artifacts/mr.md, type: file } }, on_success: gate }
  - { id: gate, type: human_gate, question: "Merge when ready.", input: { mr: { from: run.mr.url, type: link } }, on_success: done }
outcomes:
  - { id: shipped, on: mr_merged, then: done }
`
	fl := mustFlow(t, doc)
	h := newHarness(t, fl)
	h.e.cfg.MRPollInterval = time.Hour
	ctx := context.Background()

	// The MR exists on the repository host and merged during the "downtime".
	ref, err := h.repoHost.CreateDraftMR(ctx, repoport.RepoRef{}, "apothecary/r-ac", "", "T", "body")
	if err != nil {
		t.Fatal(err)
	}
	h.repoHost.SetMRState(ref.IID, repoport.MRMerged)

	// Event log of a run parked at the gate with an open final_review ping.
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: "ac", ProjectID: testProject, FlowHash: fl.Hash, Branch: "apothecary/r-ac"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: "env-ac"},
		&event.StageStarted{StageID: "mr", Iteration: 1},
		&event.MRCreated{MRURL: ref.URL, MRIID: ref.IID, Branch: "apothecary/r-ac"},
		&event.StageCompleted{StageID: "mr"},
		&event.StageStarted{StageID: "gate", Iteration: 1},
		&event.PingRaised{PingID: "ping-ac", Type: PingGate, Question: "final_review: T", Link: ref.URL, StageID: "gate"},
	} {
		if _, err := h.store.Append(ctx, "r-ac", p); err != nil {
			t.Fatal(err)
		}
	}

	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-ac")

	if v := h.view(t, "r-ac"); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, "r-ac")))
	}
	evs := h.events(t, "r-ac")
	if pa, ok := findPayload[*event.PingAnswered](evs); ok {
		t.Errorf("ping_answered = %+v, want the question left unanswered", pa)
	}
	if n := countType(evs, event.TypePingRaised); n != 1 {
		t.Errorf("ping_raised = %d, want 1 (re-arm must not re-raise)", n)
	}
}

// TestAFailingRepoHostPollNotifies: a repository host that keeps failing
// MRState must surface ONE blocker notification (not silent warn-forever), and
// the gate keeps working — completing automatically once the repository host
// recovers.
func TestAFailingRepoHostPollNotifies(t *testing.T) {
	doc := `name: acf
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: prep, type: agent, harness: $harness, model: $model, agent: builder }
  - { id: mr,   type: action, action: create_mr, input: { body: { from: repo:artifacts/mr.md, type: file } } }
  - { id: gate, type: human_gate, question: "Merge when ready.", input: { mr: { from: run.mr.url, type: link } } }
outcomes:
  - { id: shipped, on: mr_merged, then: done }
`
	h := newHarness(t, mustFlow(t, doc))
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"builder": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/mr.md", "# T\n\nbody")
			return agentport.AgentResult{TurnsUsed: 1}, nil
		},
	})
	h.repoHost.SetMRStateErr(fmt.Errorf("gitlab GET /projects/7/merge_requests/1: 401: token expired"))

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "acf", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.waitPing(t, PingGate)

	// The 3rd consecutive failed poll of the idle loop raises a blocker;
	// further failures in the same streak stay quiet.
	blockers := func() int {
		n := 0
		for _, s := range h.ch.Sent() {
			if strings.Contains(s.Title, "Cannot reach the repository") {
				n++
			}
		}
		return n
	}
	deadline := time.Now().Add(5 * time.Second)
	for blockers() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := blockers(); got != 1 {
		t.Fatalf("auto_answer blockers = %d, want 1", got)
	}
	time.Sleep(50 * time.Millisecond) // many more failed polls at 5ms cadence
	if got := blockers(); got != 1 {
		t.Errorf("auto_answer blockers after streak = %d, want still 1 (no spam)", got)
	}

	// RepoHost recovers and the MR is merged: the run completes on its own.
	h.repoHost.SetMRStateErr(nil)
	h.repoHost.SetMRState(1, repoport.MRMerged)
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
}

// TestUnlimitedRescueLoop: a max -1 link to a rescue gate never
// exhausts — the human keeps rescuing as long as they approve, and ends
// the run by rejecting. Each firing is human-gated, so "unlimited" is
// bounded by human attention, not a runaway loop.
func TestUnlimitedRescueLoop(t *testing.T) {
	doc := `name: unl
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: work,   type: command, run_ref: verify, on_success: done,
      on_fail: { to: rescue, max: -1 } }
  - { id: rescue, type: human_gate, question: "Inspect the workspace.", input: { workspace: { from: run.workspace, type: link } },
      on_success: work, on_reject: abort }
`
	h := newHarness(t, mustFlow(t, doc))
	h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "make lint") {
			return envport.ExecResult{ExitCode: 1, Output: "still broken"}, nil
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "unl", testProject)
	if err != nil {
		t.Fatal(err)
	}
	// Three rescue rounds (well past any finite default), then give up.
	var seen []string
	for range 3 {
		p := h.waitPing(t, PingGate, seen...)
		seen = append(seen, p.ID)
		if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve"}); err != nil {
			t.Fatal(err)
		}
	}
	p := h.waitPing(t, PingGate, seen...)
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "reject", Feedback: "unfixable"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if v := h.view(t, runID); v.State != run.Abandoned {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	evs := h.events(t, runID)
	var last *event.EdgeTaken
	edges := 0
	for _, e := range evs {
		if p, ok := e.Payload.(*event.EdgeTaken); ok && p.Edge == "on_fail" {
			edges++
			last = p
		}
	}
	// 4 failures each fired the same unlimited link: counts 1..4, never
	// exhausted, Max recorded as -1.
	if edges != 4 || last.Count != 4 || last.Max != -1 {
		t.Errorf("on_fail edges = %d, last = %+v (want 4 firings, count 4, max -1)", edges, last)
	}
	ra, _ := findPayload[*event.RunAbandoned](evs)
	if !strings.Contains(ra.Reason, "unfixable") {
		t.Errorf("run_abandoned = %+v", ra)
	}
}

// TestAbortEdgeStopsRunDeliberately: on_reject: abort is the explicit,
// flow-authored stop — the run lands on abandoned with the human's
// feedback in the reason, not on an "unrecoverable failure" message.
func TestAbortEdgeStopsRunDeliberately(t *testing.T) {
	doc := `name: ab
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
  - { id: gate,  type: human_gate, question: "Merge when ready.", on_reject: abort }
`
	h := newHarness(t, mustFlow(t, doc))
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ab", testProject)
	if err != nil {
		t.Fatal(err)
	}
	p := h.waitPing(t, PingGate)
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "reject", Feedback: "we pivoted; not needed"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if v := h.view(t, runID); v.State != run.Abandoned {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	ra, _ := findPayload[*event.RunAbandoned](h.events(t, runID))
	if ra.StalledAt != "gate" || ra.Edge != "on_reject" ||
		!strings.Contains(ra.Reason, "aborted by flow") || !strings.Contains(ra.Reason, "we pivoted") {
		t.Errorf("run_abandoned = %+v", ra)
	}
	if got := countType(h.events(t, runID), event.TypeEdgeTaken); got != 0 {
		t.Errorf("edge_taken = %d, want 0 (abort is terminal, not a redirect)", got)
	}
}

// TestTimeoutEdgeRoutesSeparately: a stage declaring on_timeout routes
// timeouts there, not through on_fail.
func TestTimeoutEdgeRoutesSeparately(t *testing.T) {
	doc := `name: tmo
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: a, type: agent, harness: $harness, model: $model, agent: sleeper, timeout: 30ms,
      on_fail: { to: c, max: 1 }, on_timeout: { to: b, max: 1 } }
  - { id: b, type: agent, harness: $harness, model: $model, agent: fallback, on_success: done }
  - { id: c, type: agent, harness: $harness, model: $model, agent: fallback }
`
	h := newHarness(t, mustFlow(t, doc))
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		if call.Spec.Agent == "sleeper" {
			<-ctx.Done()
			return agentport.AgentResult{}, ctx.Err()
		}
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "tmo", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	et, ok := findPayload[*event.EdgeTaken](h.events(t, runID))
	if !ok || et.Edge != "on_timeout" || et.From != "a" || et.To != "b" || et.Depth != 0 {
		t.Errorf("edge_taken = %+v, want on_timeout a->b", et)
	}
}

// TestThenChainRetryThenEscalate: on_fail retries the stage itself, then
// falls through to the escalation link, then abandons with the combined
// budget in the reason.
func TestThenChainRetryThenEscalate(t *testing.T) {
	doc := `name: chain
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
  - { id: test,  type: command, run_ref: verify,
      on_fail: { to: test, max: 2, then: { to: build, max: 1 } } }
`
	h := newHarness(t, mustFlow(t, doc))
	verifies := 0
	h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "make lint") {
			verifies++
			return envport.ExecResult{ExitCode: 1, Output: "FAIL: flaky"}, nil
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "chain", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if v := h.view(t, runID); v.State != run.Abandoned {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	evs := h.events(t, runID)
	var edges []*event.EdgeTaken
	for _, e := range evs {
		if p, ok := e.Payload.(*event.EdgeTaken); ok {
			edges = append(edges, p)
		}
	}
	// 2× retry test (depth 0), then 1× escalate to build (depth 1).
	if len(edges) != 3 ||
		edges[0].To != "test" || edges[0].Depth != 0 || edges[0].Count != 1 ||
		edges[1].To != "test" || edges[1].Depth != 0 || edges[1].Count != 2 ||
		edges[2].To != "build" || edges[2].Depth != 1 || edges[2].Count != 1 {
		t.Errorf("edges = %+v", edges)
	}
	// Every link carries the failing output — an edge no longer decides
	// whether to; its target does, by binding edge.failure or not.
	if !strings.Contains(edges[0].Feed, "FAIL: flaky") || !strings.Contains(edges[2].Feed, "FAIL: flaky") {
		t.Errorf("feeds = %q / %q", edges[0].Feed, edges[2].Feed)
	}
	if verifies != 4 { // initial + 2 retries + 1 after the escalation build
		t.Errorf("verify ran %d times, want 4", verifies)
	}
	ra, _ := findPayload[*event.RunAbandoned](evs)
	if ra.StalledAt != "test" || ra.Edge != "on_fail" || !strings.Contains(ra.Reason, "max 2+1") {
		t.Errorf("run_abandoned = %+v", ra)
	}
}

// TestPausePlayMidAgent: pause parks the run (controlled crash), play
// re-enters the in-flight stage and the run finishes. Aborting a paused
// run (no live goroutine) also works.
func TestPausePlayMidAgent(t *testing.T) {
	doc := `name: pp
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`
	h := newHarness(t, mustFlow(t, doc))
	started := make(chan struct{})
	attempt := 0
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		attempt++
		if attempt == 1 {
			close(started)
			<-ctx.Done() // in flight when pause hits
			return agentport.AgentResult{}, ctx.Err()
		}
		return agentport.AgentResult{TurnsUsed: 2}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "pp", testProject)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := h.e.Pause(runID); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	v := h.view(t, runID)
	if v.State != run.Paused {
		t.Fatalf("state after pause = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	rp, ok := findPayload[*event.RunPaused](h.events(t, runID))
	if !ok || rp.StageID != "build" {
		t.Errorf("run_paused = %+v", rp)
	}
	// Paused runs are invisible to a second pause and to Recover.
	if err := h.e.Pause(runID); err == nil {
		t.Error("pausing a paused run must fail")
	}
	if err := h.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := h.view(t, runID); v.State != run.Paused {
		t.Fatalf("recover restarted a paused run: %s", v.State)
	}

	if err := h.e.Play(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state after play = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	if attempt != 2 {
		t.Errorf("builder attempts = %d, want 2 (re-entered after play)", attempt)
	}
	if _, ok := findPayload[*event.RunUnpaused](h.events(t, runID)); !ok {
		t.Error("no run_unpaused event")
	}

	// And a paused run can be aborted directly (no goroutine to cancel).
	run2, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T2"}, "pp", testProject)
	if err != nil {
		t.Fatal(err)
	}
	// The shared RunFunc is past attempt 1 now, so this run completes on
	// its own too fast to pause reliably; give it its own blocking gate.
	h.e.Wait(run2)
	if v := h.view(t, run2); v.State != run.Completed {
		t.Fatalf("run2 = %s", v.State)
	}
	if err := h.e.Abort(run2); err == nil {
		t.Error("aborting a completed run must fail")
	}
}

// debugLeafDoc: a build/test loop whose test stage escalates to the debug
// leaf after one retry.
const debugLeafDoc = `name: dbg
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
  - { id: test,  type: command, run_ref: verify, on_success: done,
      on_fail: { to: build, max: 1, then: debug } }
`

// TestDebugLeaf: the inspect-and-fix stop, built in. A failing test escalates
// to the debug leaf: the run parks on a decision attributed to the failing
// stage — its output as context, its app link if any — with no stage of its
// own. Continuing re-runs that stage; the stop is a WANTED ping; rejecting
// abandons the run at that stage.
func TestDebugLeaf(t *testing.T) {
	h := newHarness(t, mustFlow(t, debugLeafDoc))
	verifies := 0
	h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "make lint") {
			verifies++
			if verifies <= 2 { // fails until a human "fixed" the workspace
				return envport.ExecResult{ExitCode: 1, Output: "segfault"}, nil
			}
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "dbg", testProject)
	if err != nil {
		t.Fatal(err)
	}
	p := h.waitPing(t, PingDebug)
	// The fake exposes no app port, so the ping carries no link: the
	// workspace itself is on the run page, not behind a recorded URL.
	if p.StageID != "test" || p.Link != "" || !strings.Contains(p.Context, "segfault") || p.Question != "test failed" {
		t.Errorf("ping = stage %q link %q question %q context %q; want the failing stage, no link, its output", p.StageID, p.Link, p.Question, p.Context)
	}
	// The main chain's debug leaf: giving up there abandons the run.
	if p.Phase != flow.MainPhase || p.EndsPhase {
		t.Errorf("ping phase %q ends_phase %v, want the main chain's, abandoning the run", p.Phase, p.EndsPhase)
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve", Feedback: "fixed a stale lockfile"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(evs))
	}
	tests := 0
	for _, e := range evs {
		if st, ok := e.Payload.(*event.StageStarted); ok {
			if st.StageID == "debug" {
				t.Error("the debug leaf must not start a stage of its own")
			}
			if st.StageID == "test" {
				tests++
			}
		}
	}
	if tests != 3 || verifies != 3 {
		t.Errorf("test attempts = %d (verify ran %d), want 3: fail, fail, then the re-run after the human", tests, verifies)
	}
	rc, _ := findPayload[*event.RunCompleted](evs)
	if rc.UnwantedPings != 0 {
		t.Errorf("unwanted_pings = %d, want 0 (the debug stop is asked on purpose)", rc.UnwantedPings)
	}

	// Reject path: abandoned at the failing stage, with the human's words.
	verifies = -1000 // keep verify failing forever for the second run
	run2, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T2"}, "dbg", testProject)
	if err != nil {
		t.Fatal(err)
	}
	p2 := h.waitPing(t, PingDebug, p.ID)
	if err := h.e.AnswerPing(p2.ID, PingAnswer{Answer: "reject", Feedback: "cannot reproduce"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(run2)
	if v := h.view(t, run2); v.State != run.Abandoned {
		t.Fatalf("run2 state = %s", v.State)
	}
	ra, _ := findPayload[*event.RunAbandoned](h.events(t, run2))
	if ra.StalledAt != "test" || !strings.Contains(ra.Reason, "cannot reproduce") {
		t.Errorf("run_abandoned = %+v", ra)
	}
}

// A run parked at the debug leaf survives a restart: the log ends in the
// open ping, recovery leaves it parked (nothing to do), the ping is still
// answerable, and the answer sends the run back into the stage. The crash
// window between the answer and its routing is covered too.
func TestDebugLeafSurvivesRestart(t *testing.T) {
	fl := mustFlow(t, debugLeafDoc)
	parked := func(t *testing.T, h *harness, runID string, answered bool) {
		t.Helper()
		ctx := context.Background()
		envID, err := h.envp.Provision(ctx, envport.EnvSpec{RunID: runID})
		if err != nil {
			t.Fatal(err)
		}
		evs := []event.Payload{
			&event.RunCreated{TicketTitle: "T", FlowName: "dbg", ProjectID: testProject, FlowHash: fl.Hash, Branch: "apothecary/" + runID},
			&event.EnvRequested{},
			&event.EnvReady{EnvID: string(envID), Links: []event.Link{{Kind: "app", URL: "http://ws/" + runID + "/app"}}},
			&event.StageStarted{StageID: "build", Iteration: 1},
			&event.StageCompleted{StageID: "build", SessionID: "sess-1"},
			&event.StageStarted{StageID: "test", Iteration: 1},
			&event.StageFailed{StageID: "test", Reason: event.ReasonExitNonzero, Error: "segfault"},
			&event.EdgeTaken{From: "test", To: "debug", Edge: "on_fail", Count: 1, Max: -1, Feed: "segfault"},
			&event.PingRaised{PingID: "dbg-" + runID, Type: event.PingDebug, Question: "test failed", Context: "segfault",
				Link: "http://ws/" + runID + "/app", StageID: "test", AnswerKind: event.AnswerYesNo},
			&event.RunPaused{StageID: "test", Reason: event.PauseWaiting},
		}
		if answered {
			evs = append(evs, &event.PingAnswered{PingID: "dbg-" + runID, Answer: "approve", Feedback: "fixed"})
		}
		for _, p := range evs {
			if _, err := h.store.Append(ctx, runID, p); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Parked, unanswered: recovery leaves it alone; the answer wakes it.
	h := newHarness(t, fl)
	parked(t, h, "r-dbg", false)
	if err := h.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-dbg")
	if v := h.view(t, "r-dbg"); v.State != run.WaitingOnHuman || !v.Dormant {
		t.Fatalf("after boot: state %s dormant %v, want parked on the debug stop", v.State, v.Dormant)
	}
	if err := h.e.AnswerPing("dbg-r-dbg", PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-dbg")
	if v := h.view(t, "r-dbg"); v.State != run.Completed {
		t.Fatalf("after the answer: state %s, events %v", v.State, eventTypes(h.events(t, "r-dbg")))
	}
	if n := countType(h.events(t, "r-dbg"), event.TypePingRaised); n != 1 {
		t.Errorf("ping_raised ×%d, want the one the log already had", n)
	}

	// Answered, then the crash before routing: recovery routes the answer.
	h2 := newHarness(t, fl)
	parked(t, h2, "r-dbg2", true)
	if err := h2.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	h2.e.Wait("r-dbg2")
	if v := h2.view(t, "r-dbg2"); v.State != run.Completed {
		t.Fatalf("recovered answer: state %s, events %v", v.State, eventTypes(h2.events(t, "r-dbg2")))
	}
	if n := countType(h2.events(t, "r-dbg2"), event.TypePingRaised); n != 1 {
		t.Errorf("ping_raised ×%d after recovery, want 1 (no second question)", n)
	}
}

func TestEnvFailureAfterRetry(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.envp.ProvisionErrs = []error{fmt.Errorf("docker daemon unreachable"), fmt.Errorf("still unreachable")}

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
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

func TestBootstrapFailureIsEnvFailed(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "git clone") {
			return envport.ExecResult{ExitCode: 1, Output: "fatal: could not write: ENOSPC"}, nil
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if v := h.view(t, runID); v.State != run.Failed {
		t.Fatalf("state = %s", v.State)
	}
	ef, _ := findPayload[*event.EnvFailed](h.events(t, runID))
	if !strings.Contains(ef.Error, "ENOSPC") {
		t.Errorf("env_failed should carry the failing step's output, got %+v", ef)
	}
	if len(h.runner.RunCalls) != 0 {
		t.Error("no agent may run when bootstrap failed")
	}
}

// A workspace whose traffic never leaves (found live: br_netfilter dropping it
// before the egress gateway) fails at provisioning, naming the host — not an
// agent hung on its model request until the stage timeout.
func TestEgressCheckFailureIsEnvFailed(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	var probe string
	h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "shim probe") {
			probe = cmd.Script
			return envport.ExecResult{ExitCode: 1, Output: "probe gitlab.local:443: dial tcp: i/o timeout"}, nil
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if !strings.Contains(probe, "--tls 'gitlab.local:443'") {
		t.Errorf("probe should handshake with the repository host, ran %q", probe)
	}
	if v := h.view(t, runID); v.State != run.Failed {
		t.Fatalf("state = %s", v.State)
	}
	ef, _ := findPayload[*event.EnvFailed](h.events(t, runID))
	if ef == nil || !strings.Contains(ef.Error, "i/o timeout") || ef.EnvID == "" {
		t.Errorf("env_failed should carry the probe's reason and keep the workspace, got %+v", ef)
	}
	if len(h.runner.RunCalls) != 0 {
		t.Error("no agent may run when the workspace cannot reach the network")
	}
}

func TestEgressProbeTarget(t *testing.T) {
	for _, tc := range []struct {
		url     string
		domains []string
		addr    string
		tls     bool
	}{
		{"https://gitlab.com/g/p.git", nil, "gitlab.com:443", true},
		{"https://git.corp:8443/g/p.git", nil, "git.corp:8443", true},
		{"http://git.corp/g/p.git", nil, "git.corp:80", false},
		{"git@github.com:o/r.git", []string{"*.npmjs.org", "github.com"}, "github.com:443", true},
		{"/srv/repo.git", []string{"*.npmjs.org"}, "", false},
	} {
		addr, tls := egressProbeTarget(&resolution.Resolution{RepoRef: repoport.RepoRef{URL: tc.url}, EgressDomains: tc.domains})
		if addr != tc.addr || tls != tc.tls {
			t.Errorf("%s: got %q tls=%v, want %q tls=%v", tc.url, addr, tls, tc.addr, tc.tls)
		}
	}
}

func TestStageStartedCarriesAgentHash(t *testing.T) {
	// DESIGN3 §1: stage events pin the agent-library content version so
	// analytics can correlate outcomes with prompt versions.
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	found := false
	for _, ev := range h.events(t, runID) {
		if ss, ok := ev.Payload.(*event.StageStarted); ok && ss.StageID == "build" {
			found = true
			// The hash is the agent's content address, computed from the
			// definition the flow carries (FLOWLANG §1, v3).
			if want := h.flowOf(t, "loop").AgentHash("builder"); ss.Agent != "builder" || ss.AgentHash != want {
				t.Errorf("stage_started agent = %q hash = %q, want builder/%s", ss.Agent, ss.AgentHash, want)
			}
		}
	}
	if !found {
		t.Fatal("no stage_started for build")
	}
}

func TestPreflightRefusalsNameTheMissingItem(t *testing.T) {
	// v3: agents live in the FLOW, so a stage naming an undeclared one never
	// reaches preflight — it is refused at parse time, by name.
	_, perr := flow.Parse("t.yaml", []byte(withAgents(loopFlowDoc, map[string]*agentspec.Spec{
		"planner": {}, // the flow needs builder too
	})))
	if perr == nil || !strings.Contains(perr.Error(), `agent "builder" is not declared in this flow`) {
		t.Errorf("err = %v, want undeclared-agent parse error", perr)
	}

	h2 := newHarness(t, mustFlow(t, loopFlowDoc))
	delete(h2.res.Commands, "verify")
	_, err := h2.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
	if err == nil || !strings.Contains(err.Error(), `run_ref "verify"`) {
		t.Errorf("err = %v, want mention of run_ref verify", err)
	}

	// A flow pinning a foreign harness is refused at preflight (DESIGN2
	// §6.3), naming stage and kinds.
	h4 := newHarness(t, mustFlow(t, `name: pinned
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: agent, model: $model, agent: builder, harness: codex }
`))
	_, err = h4.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "pinned", testProject)
	if err == nil || !strings.Contains(err.Error(), `pins harness "codex"`) {
		t.Errorf("err = %v, want harness pin refusal", err)
	}

	// No events may exist after refusals.
	for _, hh := range []*harness{h2, h4} {
		ids, _ := hh.store.RunIDs(context.Background())
		if len(ids) != 0 {
			t.Errorf("refused runs left events: %v", ids)
		}
	}
}

// A repo without a devcontainer is refused at run creation — apothecary runs
// the repo's own devcontainer, there is no built fallback image.
func TestPreflightRejectsRepoWithoutDevcontainer(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.repoHost.MissingFiles = map[string]bool{
		".devcontainer/devcontainer.json": true,
		".devcontainer.json":              true,
	}
	_, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
	if err == nil || !strings.Contains(err.Error(), "devcontainer") {
		t.Fatalf("err = %v, want devcontainer refusal", err)
	}
	if ids, _ := h.store.RunIDs(context.Background()); len(ids) != 0 {
		t.Errorf("refused run left events: %v", ids)
	}
}

func TestOutputContractViolationFailsStage(t *testing.T) {
	doc := `name: contract
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: plan, type: agent, harness: $harness, model: $model, agent: planner, output: { plan: { to: artifacts/plan.md, type: file } } }
`
	h := newHarness(t, mustFlow(t, doc))
	// Planner never writes the declared artifact.
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "contract", testProject)
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
	if !strings.Contains(sf.Error, "artifacts/plan.md") {
		t.Errorf("failure should name the missing path: %q", sf.Error)
	}
}

// TestExplicitSuccessRouting proves the engine follows explicit on_success
// edges (DESIGN3 §2.1): the entry stage skips over a fallback stage that
// is only reachable via on_fail and itself completes the run early.
func TestExplicitSuccessRouting(t *testing.T) {
	branchDoc := `name: branch
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: probe,   type: command, run: probe.sh, on_success: finish, on_fail: { to: repair, max: 1 } }
  - { id: repair,  type: command, run: repair.sh, on_success: done }
  - { id: finish,  type: command, run: finish.sh }
`
	// Happy path: probe succeeds -> finish runs, repair never does.
	h := newHarness(t, mustFlow(t, branchDoc))
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		return envport.ExecResult{ExitCode: 0, Output: cmd.Script}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "branch", testProject)
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
	h2.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "probe.sh") {
			return envport.ExecResult{ExitCode: 1, Output: "probe broke"}, nil
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}
	runID2, err := h2.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "branch", testProject)
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

// TestOptionPing covers PLAN4 M16 closed questions: the answer must be one
// of the declared options; the chosen option (plus note) reaches the agent
// and is recorded verbatim in ping_answered.
func TestOptionPing(t *testing.T) {
	doc := `name: ping
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`
	h := newHarness(t, mustFlow(t, doc))
	runIDCh := make(chan string, 1)
	gotAnswer := make(chan PingAnswer, 1)
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		id := <-runIDCh
		p, err := h.e.RaisePing(id, "clarification", "which db?", "", []string{"postgres", "sqlite", "postgres"}, []string{"artifacts/dbs.md"})
		if err != nil {
			return agentport.AgentResult{}, err
		}
		ans, _ := p.Await(context.Background())
		gotAnswer <- ans
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ping", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID

	p := h.waitPing(t, "clarification")
	if f := p.Form.Fields; len(f) != 1 || len(f[0].Options) != 2 || f[0].Options[0].ID != "postgres" || f[0].Options[1].ID != "sqlite" || p.AnswerKind != event.AnswerForm { // deduped
		t.Errorf("form = %+v", p.Form)
	}
	if len(p.Files) != 1 || p.Files[0] != "artifacts/dbs.md" {
		t.Errorf("ping files = %v", p.Files)
	}
	// An answer off the options is refused for a closed question.
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "accept", Content: map[string]any{AnswerField: "mysql"}}); err == nil || !strings.Contains(err.Error(), "not one of the options") {
		t.Errorf("off-option answer err = %v", err)
	}
	// Declining without the note is useless — the note is what the agent reads.
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "decline", Feedback: "  "}); err == nil || !strings.Contains(err.Error(), "note") {
		t.Errorf("bare decline err = %v", err)
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "accept", Feedback: "use v16", Content: map[string]any{AnswerField: "postgres"}}); err != nil {
		t.Fatal(err)
	}
	ans := <-gotAnswer
	if ans.Answer != "accept" || ans.Content[AnswerField] != "postgres" || ans.Feedback != "use v16" {
		t.Errorf("agent received %+v", ans)
	}
	h.e.Wait(runID)
	pa, ok := findPayload[*event.PingAnswered](h.events(t, runID))
	if !ok || pa.Answer != "accept" || pa.Content[AnswerField] != "postgres" || pa.Feedback != "use v16" {
		t.Errorf("ping_answered = %+v", pa)
	}
	pr, _ := findPayload[*event.PingRaised](h.events(t, runID))
	if pr.Form == nil || len(pr.Form.Fields[0].Options) != 2 {
		t.Errorf("ping_raised form = %+v", pr.Form)
	}

	// Invalid raises are refused up front.
	h2 := newHarness(t, mustFlow(t, doc))
	run2 := make(chan string, 1)
	h2.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		id := <-run2
		if _, err := h2.e.RaisePing(id, "clarification", "q", "", nil, []string{"/etc/passwd"}); err == nil {
			t.Error("absolute artifact accepted")
		}
		if _, err := h2.e.RaisePing(id, "clarification", "q", "", []string{""}, nil); err == nil {
			t.Error("empty option accepted")
		}
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}
	rid, err := h2.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ping", testProject)
	if err != nil {
		t.Fatal(err)
	}
	run2 <- rid
	h2.e.Wait(rid)

	// "None of these fit" end-to-end: declining carries the human's actual
	// answer to the agent in the note.
	h3 := newHarness(t, mustFlow(t, doc))
	run3 := make(chan string, 1)
	got3 := make(chan PingAnswer, 1)
	h3.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		id := <-run3
		p, err := h3.e.RaisePing(id, "clarification", "pick one", "", []string{"a", "Other", "b"}, nil)
		if err != nil {
			return agentport.AgentResult{}, err
		}
		ans, _ := p.Await(context.Background())
		got3 <- ans
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}
	rid3, err := h3.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ping", testProject)
	if err != nil {
		t.Fatal(err)
	}
	run3 <- rid3
	p3 := h3.waitPing(t, "clarification")
	if err := h3.e.AnswerPing(p3.ID, PingAnswer{Answer: "decline", Feedback: "neither — use c"}); err != nil {
		t.Fatal(err)
	}
	if ans := <-got3; ans.Answer != "decline" || ans.Feedback != "neither — use c" {
		t.Errorf("agent received %+v", ans)
	}
	h3.e.Wait(rid3)
	pa3, _ := findPayload[*event.PingAnswered](h3.events(t, rid3))
	if pa3.Answer != "decline" || pa3.Feedback != "neither — use c" {
		t.Errorf("ping_answered = %+v", pa3)
	}
}

// TestRetryRerunsFailedStage: a run that abandons at a command stage
// resumes IN THE SAME WORKSPACE, re-running only the failed stage.
func TestRetryRerunsFailedStage(t *testing.T) {
	// A single command stage with no on_fail: it abandons on failure.
	doc := `name: r
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: push, type: command, run: "push.sh" }
`
	h := newHarness(t, mustFlow(t, doc))
	fail := true
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "push.sh") && fail {
			return envport.ExecResult{ExitCode: 1, Output: "connect timeout"}, nil
		}
		return envport.ExecResult{ExitCode: 0, Output: "pushed"}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "r", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Abandoned {
		t.Fatalf("state = %s, want abandoned", v.State)
	}
	envBefore := h.view(t, runID).EnvID
	if envBefore == "" {
		t.Fatal("no env recorded")
	}

	// Fix the failing command; resume re-runs push in the SAME env.
	fail = false
	if err := h.e.RetryRun(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	v := h.view(t, runID)
	if v.State != run.Completed {
		t.Fatalf("after resume state = %s: %v", v.State, eventTypes(h.events(t, runID)))
	}
	if v.EnvID != envBefore {
		t.Errorf("env changed on resume: %s -> %s (must reuse the workspace)", envBefore, v.EnvID)
	}
	// Exactly one env was ever provisioned (resume reused it, no restart).
	if got := len(h.envp.Destroyed); got != 0 {
		t.Errorf("env destroyed on resume (%d) — should be reused", got)
	}
	if countType(h.events(t, runID), event.TypeRunRetried) != 1 {
		t.Error("no run_retried event")
	}

	// A completed run cannot be resumed.
	if err := h.e.RetryRun(context.Background(), runID); err == nil || !strings.Contains(err.Error(), "can be retried") {
		t.Errorf("resume completed err = %v", err)
	}
}

// TestEgressPolicyReachesProvisioner: the derived egress allowlist and mode are
// delivered to the provisioner via the EnvSpec at Provision time (the provisioner
// then hands them to the per-project gateway). No engine-side proxy or proxy.env.
func TestEgressPolicyReachesProvisioner(t *testing.T) {
	doc := `name: p
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`
	h := newHarness(t, mustFlow(t, doc))
	h.res.EgressDomains = []string{"models.dev", "registry-1.docker.io"}

	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "p", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	envID := h.view(t, runID).EnvID
	env := h.envp.Env(envport.EnvID(envID))
	if env == nil {
		t.Fatal("no env")
	}
	// The allowlist reached the spec (the provisioner hands it to the gateway).
	if !slices.Contains(env.Spec.EgressDomains, "models.dev") {
		t.Errorf("EgressDomains not delivered to the provisioner: %v", env.Spec.EgressDomains)
	}
	// No proxy.env is written anymore.
	if _, ok := env.Files["/apothecary/proxy.env"]; ok {
		t.Errorf("proxy.env should no longer be written")
	}
}

// --- v2 grammar mechanics ---------------------------------------------------

// TestCommandDeclaredInputPrecondition: a command's declared input is checked
// before the script runs, so a missing upstream artifact is reported as
// upstream wiring rather than as a confusing script failure.
func TestCommandDeclaredInputPrecondition(t *testing.T) {
	doc := `name: cmdio
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: commit, type: command, run: "git commit -F artifacts/commit.md",
      input: { f0: { from: repo:artifacts/commit.md, type: file } }, on_success: done,
      on_fail: { to: rescue, max: -1 } }
  - { id: rescue, type: human_gate, question: "Fix it.", input: { workspace: { from: run.workspace, type: link } },
      on_success: commit, on_reject: abort }
`
	h := newHarness(t, mustFlow(t, doc))
	var ran bool
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "git commit") {
			ran = true
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "cmdio", testProject)
	if err != nil {
		t.Fatal(err)
	}
	// The precondition fails, so the run parks at the rescue gate.
	p := h.waitPing(t, PingGate)
	if ran {
		t.Error("the script ran despite its declared input being absent")
	}
	sf, ok := findPayload[*event.StageFailed](h.events(t, runID))
	if !ok || sf.Reason != event.ReasonInputMissing {
		t.Fatalf("stage_failed = %+v, want reason %q", sf, event.ReasonInputMissing)
	}
	if !strings.Contains(sf.Error, "artifacts/commit.md") {
		t.Errorf("failure does not name the missing path: %q", sf.Error)
	}

	// Supply the artifact by hand (what a human at the gate would do) and
	// approve: the same stage re-runs and now passes its precondition.
	h.writeFile(t, "env-1", "artifacts/commit.md", "feat: x")
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	if !ran {
		t.Error("the script never ran after its input appeared")
	}
}

// TestCommandDeclaredOutputEnforced: exiting 0 without writing what it declared
// is not success — the same contract agent stages carry.
func TestCommandDeclaredOutputEnforced(t *testing.T) {
	doc := `name: cmdout
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: gen, type: command, run: "generate", output: { report: { to: artifacts/report.txt, type: file } } }
`
	h := newHarness(t, mustFlow(t, doc))
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "cmdout", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Abandoned {
		t.Fatalf("state = %s, want abandoned (no on_fail)", v.State)
	}
	sf, _ := findPayload[*event.StageFailed](h.events(t, runID))
	if sf.Reason != event.ReasonOutputContract || !strings.Contains(sf.Error, "artifacts/report.txt") {
		t.Errorf("stage_failed = %+v", sf)
	}
}

// TestVerdictOutputRebound: the engine reads the verdict from the path the
// stage bound, never a hardcoded artifacts/verdict.json — and a REJECT there
// still routes the on_reject edge with its comments.
func TestVerdictOutputRebound(t *testing.T) {
	doc := `name: vrebind
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: build,  type: agent, harness: $harness, model: $model, agent: builder, input: { rejection: { from: edge.verdict, type: verdict, optional: true } } }
  - { id: review, type: agent, harness: $harness, model: $model, agent: reviewer,
      output: { verdict: { to: artifacts/review/verdict.json, type: verdict } },
      on_reject: { to: build, max: 2 } }
`
	h := newHarness(t, mustFlow(t, doc))
	reviews := 0
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"reviewer": func(c fake.RunCall) (agentport.AgentResult, error) {
			reviews++
			body := `{"verdict":"APPROVE","comments":[]}`
			if reviews == 1 {
				body = `{"verdict":"REJECT","comments":["rebound verdict was read"]}`
			}
			h.writeFile(t, c.Env, "artifacts/review/verdict.json", body)
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "vrebind", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	// The prompt names the bound path, not the default.
	for _, c := range h.runner.RunCalls {
		if c.Spec.Agent != "reviewer" {
			continue
		}
		if !strings.Contains(c.In.Prompt, "artifacts/review/verdict.json") {
			t.Errorf("reviewer prompt does not name the bound verdict path:\n%s", c.In.Prompt)
		}
		if strings.Contains(c.In.Prompt, "you MUST write these files") &&
			strings.Contains(c.In.Prompt, "- artifacts/verdict.json") {
			t.Error("prompt still demands the default verdict path")
		}
	}
	// The rejection fed its comments back into the rebuild.
	rebuild := h.runner.RunCalls[2].In.Prompt // build1, review1, build2
	if !strings.Contains(rebuild, "rebound verdict was read") {
		t.Errorf("rebuild prompt missing the fed comments:\n%s", rebuild)
	}
}

// TestVerdictOutputMissingIsOneFailure: a missing verdict file is caught by the
// ordinary output-contract check, so the run reports it once — not once for the
// missing file and again for the unparsable verdict.
func TestVerdictOutputMissingIsOneFailure(t *testing.T) {
	doc := `name: vmiss
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: review, type: agent, harness: $harness, model: $model, agent: reviewer, output: { verdict: { to: artifacts/verdict.json, type: verdict } } }
`
	h := newHarness(t, mustFlow(t, doc))
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "vmiss", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if got := countType(evs, event.TypeStageFailed); got != 1 {
		t.Errorf("stage_failed count = %d, want 1", got)
	}
	sf, _ := findPayload[*event.StageFailed](evs)
	if sf.Reason != event.ReasonOutputContract || !strings.Contains(sf.Error, "artifacts/verdict.json") {
		t.Errorf("stage_failed = %+v", sf)
	}
}

// TestCreateMRAction: create_mr takes the title from the body's heading,
// records one mr_created event, and the run.mr.url source resolves for a
// later gate.
func TestCreateMRAction(t *testing.T) {
	doc := `name: act
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } } }
  - { id: mr,     type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }
  - { id: gate,   type: human_gate, question: "Merge?", input: { mr: { from: run.mr.url, type: link } }, on_success: done, on_reject: abort }
`
	h := newHarness(t, mustFlow(t, doc))
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"redactor": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/mr.md", "# Add the login form\n\nSummary")
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "act", testProject)
	if err != nil {
		t.Fatal(err)
	}
	p := h.waitPing(t, PingGate)
	if !strings.HasPrefix(p.Link, "https://gitlab.local/mr/") {
		t.Errorf("gate payload = %q, want the run.mr.url link", p.Link)
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	if len(h.repoHost.MRs) != 1 || h.repoHost.MRs[0].Title != "Add the login form" {
		t.Errorf("MRs = %+v", h.repoHost.MRs)
	}
	if got := countType(h.events(t, runID), event.TypeMRCreated); got != 1 {
		t.Errorf("mr_created count = %d, want 1", got)
	}
}

// TestCreateMRActionMissingBody: the action reports the fault as upstream
// wiring (its declared input is absent), not as its own output contract.
func TestCreateMRActionMissingBody(t *testing.T) {
	doc := `name: actmiss
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:artifacts/mr.md, type: file } } }
`
	h := newHarness(t, mustFlow(t, doc))
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "actmiss", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Abandoned {
		t.Fatalf("state = %s, want abandoned", v.State)
	}
	sf, _ := findPayload[*event.StageFailed](h.events(t, runID))
	if sf.Reason != event.ReasonInputMissing {
		t.Errorf("reason = %q, want %q", sf.Reason, event.ReasonInputMissing)
	}
	if len(h.repoHost.MRs) != 0 {
		t.Errorf("an MR was opened without a body: %+v", h.repoHost.MRs)
	}
}

// TestUnparseableFlowFailsRunTerminally: a stored flow that no longer parses
// can never be fixed by retrying, and leaving the run non-terminal spun it
// through recovery on every boot. It must fail with the parse error.
func TestUnparseableFlowFailsRunTerminally(t *testing.T) {
	fl := mustFlow(t, loopFlowDoc)
	fm := flow.MapSource{fl.Name: fl}
	h := newHarnessSource(t, brokenSource{name: fl.Name, inner: fm}, fl)

	// A non-terminal run whose flow the source can no longer read.
	ctx := context.Background()
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: fl.Name, ProjectID: testProject, FlowHash: fl.Hash, Branch: "b"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: "env-1"},
		&event.StageStarted{StageID: "build", Iteration: 1},
	} {
		if _, err := h.store.Append(ctx, "r-brk", p); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-brk")

	v := h.view(t, "r-brk")
	if v.State != run.Failed {
		t.Fatalf("state = %s, want failed (events: %v)", v.State, eventTypes(h.events(t, "r-brk")))
	}
	rf, ok := findPayload[*event.RunFailed](h.events(t, "r-brk"))
	if !ok || !strings.Contains(rf.Reason, "no longer parses") {
		t.Errorf("run_failed = %+v, want the parse error", rf)
	}
}

// brokenSource stands in for a stored flow whose YAML no longer parses: the
// source returns a *flow.Error, not ErrNotFound.
type brokenSource struct {
	name  string
	inner flow.MapSource
}

func (b brokenSource) Get(ctx context.Context, projectID, name string) (*flow.Flow, error) {
	if name == b.name {
		return nil, &flow.Error{File: name, Line: 4, Msg: `stage "build": "verdict" is retired`}
	}
	return b.inner.Get(ctx, projectID, name)
}

// TestWaitStageBlocksUntilSignal: a wait stage parks the run on an external
// condition and needs no workspace to do it — the same shape as a gate, minus
// the human.
func TestWaitStageBlocksUntilSignal(t *testing.T) {
	doc := `name: waitflow
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } }, on_success: mr }
  - { id: mr,     type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } }, on_success: merged }
  - { id: merged, type: wait, for: mr_merged, on_success: done }
`
	h := newHarness(t, mustFlow(t, doc))
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"redactor": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/mr.md", "# Ship it\n\nSummary")
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "waitflow", testProject)
	if err != nil {
		t.Fatal(err)
	}
	// The run reaches the wait stage and stays there: no ping, not finished.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.view(t, runID).CurrentStage == "merged" {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if v := h.view(t, runID); v.CurrentStage != "merged" || v.State.Terminal() {
		t.Fatalf("run is %s at %q, want waiting at merged", v.State, v.CurrentStage)
	}
	if len(h.e.PendingPings()) != 0 {
		t.Error("a wait stage must not ask a human anything")
	}

	// The signal fires; the stage completes and the run finishes.
	h.repoHost.SetMRState(1, repoport.MRMerged)
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
}

// TestWaitStageTimeout: a wait is unbounded unless the stage says otherwise,
// and a bounded one routes its on_timeout edge.
func TestWaitStageTimeout(t *testing.T) {
	doc := `name: waittimeout
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } }, on_success: mr }
  - { id: mr,     type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } }, on_success: merged }
  - { id: merged, type: wait, for: mr_merged, timeout: 20ms, on_success: done,
      on_timeout: { to: nudge, max: 1 } }
  - { id: nudge,  type: human_gate, question: "It never merged — chase it?", on_success: done,
      on_reject: abort }
`
	h := newHarness(t, mustFlow(t, doc))
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"redactor": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/mr.md", "# Ship it")
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "waittimeout", testProject)
	if err != nil {
		t.Fatal(err)
	}
	// The signal never fires, so the timeout routes to the gate.
	p := h.waitPing(t, PingGate)
	sf, ok := findPayload[*event.StageFailed](h.events(t, runID))
	if !ok || sf.Reason != event.ReasonTimeout || !strings.Contains(sf.Error, "mr_merged") {
		t.Errorf("stage_failed = %+v, want a timeout naming the signal", sf)
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s", v.State)
	}
}

// TestWaitForImpossibleSignalFails: waiting on something that can never happen
// in this run is a failure with a reason, not a hang.
func TestWaitForImpossibleSignalFails(t *testing.T) {
	doc := `name: waitnomr
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: merged, type: wait, for: mr_merged }
`
	h := newHarness(t, mustFlow(t, doc))
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "waitnomr", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Abandoned {
		t.Fatalf("state = %s, want abandoned (no on_fail)", v.State)
	}
	sf, _ := findPayload[*event.StageFailed](h.events(t, runID))
	if sf.Reason != event.ReasonInputMissing || !strings.Contains(sf.Error, "merge request") {
		t.Errorf("stage_failed = %+v, want a missing-prerequisite reason", sf)
	}
}

// TestEdgeSourcesAreDistinct: a rejection and a failure reach the same stage
// through different, named inputs — an agent can tell "you were turned down"
// apart from "it broke", which one undifferentiated blob could not express.
func TestEdgeSourcesAreDistinct(t *testing.T) {
	doc := `name: twofeeds
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: build,  type: agent, harness: $harness, model: $model, agent: builder, on_success: test,
      input: { rejection: { from: edge.verdict, type: verdict, optional: true }, failure: { from: edge.failure, type: failure, optional: true } } }
  - { id: test,   type: command, run_ref: verify, on_success: review,
      on_fail: { to: build, max: 3 } }
  - { id: review, type: agent, harness: $harness, model: $model, agent: reviewer, output: { verdict: { to: artifacts/verdict.json, type: verdict } }, on_success: done,
      on_reject: { to: build, max: 3 } }
`
	h := newHarness(t, mustFlow(t, doc))
	verifies, reviews := 0, 0
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "make lint") {
			verifies++
			if verifies == 1 {
				return envport.ExecResult{ExitCode: 1, Output: "TESTS BLEW UP"}, nil
			}
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"reviewer": func(c fake.RunCall) (agentport.AgentResult, error) {
			reviews++
			body := `{"verdict":"APPROVE","comments":[]}`
			if reviews == 1 {
				body = `{"verdict":"REJECT","comments":["LAYERING IS WRONG"]}`
			}
			h.writeFile(t, c.Env, "artifacts/verdict.json", body)
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "twofeeds", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
	var builds []string
	for _, c := range h.runner.RunCalls {
		if c.Spec.Agent == "builder" {
			builds = append(builds, c.In.Prompt)
		}
	}
	if len(builds) != 3 {
		t.Fatalf("builder ran %d times, want 3 (initial, after test fail, after reject)", len(builds))
	}
	// Attempt 1: neither slot renders — a first attempt has nothing fed.
	if strings.Contains(builds[0], "# INPUT: failure") || strings.Contains(builds[0], "# INPUT: rejection") {
		t.Errorf("build[0] should carry neither feed:\n%s", builds[0])
	}
	// Attempt 2 came from a test failure: only the failure slot fills.
	if !strings.Contains(builds[1], "# INPUT: failure") || !strings.Contains(builds[1], "TESTS BLEW UP") {
		t.Errorf("build[1] missing the failure:\n%s", builds[1])
	}
	if strings.Contains(builds[1], "# INPUT: rejection") {
		t.Errorf("build[1] must not render a rejection it never got:\n%s", builds[1])
	}
	// Attempt 3 came from a REJECT verdict: only the rejection slot fills.
	if !strings.Contains(builds[2], "# INPUT: rejection") || !strings.Contains(builds[2], "LAYERING IS WRONG") {
		t.Errorf("build[2] missing the rejection:\n%s", builds[2])
	}
	if strings.Contains(builds[2], "# INPUT: failure") {
		t.Errorf("build[2] must not render a stale failure:\n%s", builds[2])
	}
}

// TestUnlimitedRetryDoesNotClaimALastAttempt: on an unlimited link (max -1)
// there is no total, and saying "attempt 2 of 2" would tell an agent this is its
// last chance when the loop is bounded only by human patience.
func TestUnlimitedRetryDoesNotClaimALastAttempt(t *testing.T) {
	doc := `name: unlim
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: plan, type: agent, harness: $harness, model: $model, agent: planner, output: { plan: { to: artifacts/plan.md, type: file } }, input: { rejection: { from: edge.verdict, type: verdict, optional: true } }, on_success: gate }
  - { id: gate, type: human_gate, question: "Approve?", input: { plan: { from: artifacts/plan.md, type: file } },
      on_success: done,
      on_reject: { to: plan, max: -1 } }
`
	h := newHarness(t, mustFlow(t, doc))
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"planner": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/plan.md", "plan")
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "unlim", testProject)
	if err != nil {
		t.Fatal(err)
	}
	p1 := h.waitPing(t, PingGate)
	if err := h.e.AnswerPing(p1.ID, PingAnswer{Answer: "reject", Feedback: "narrow it down"}); err != nil {
		t.Fatal(err)
	}
	p2 := h.waitPing(t, PingGate, p1.ID)
	if err := h.e.AnswerPing(p2.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s", v.State)
	}
	replan := h.runner.RunCalls[1].In.Prompt
	if !strings.Contains(replan, "(attempt 2)") {
		t.Errorf("replan prompt should carry a bare attempt number:\n%s", replan)
	}
	if strings.Contains(replan, "attempt 2 of") {
		t.Errorf("an unlimited loop must not claim a total:\n%s", replan)
	}
}

// TestCreateMRReconcilesAfterCrashInRecordWindow covers the crash window the
// mr_created event alone cannot close (PLAN5 M20): the repository host call
// returned, the process died before the event was appended. Recovery must
// ADOPT the merge request that already exists on the run's branch, not open a
// second one — the branch is unique per run, so it is the idempotency key.
func TestCreateMRReconcilesAfterCrashInRecordWindow(t *testing.T) {
	doc := `name: rec
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:artifacts/mr.md, type: file } }, on_success: done }
`
	fl := mustFlow(t, doc)
	h := newHarness(t, fl)
	ctx := context.Background()

	// The MR the interrupted attempt already opened on the repository host.
	ref, err := h.repoHost.CreateDraftMR(ctx, repoport.RepoRef{}, "apothecary/r-rec", "", "T", "body")
	if err != nil {
		t.Fatal(err)
	}

	// History stops at the intent marker: action_started with no mr_created.
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: "rec", ProjectID: testProject, FlowHash: fl.Hash, Branch: "apothecary/r-rec"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: "env-rec"},
		&event.StageStarted{StageID: "mr", Iteration: 1},
		&event.ActionStarted{StageID: "mr", Action: "create_mr"},
	} {
		if _, err := h.store.Append(ctx, "r-rec", p); err != nil {
			t.Fatal(err)
		}
	}

	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-rec")

	// Exactly one MR on the repository host, and the run adopted it.
	if len(h.repoHost.MRs) != 1 {
		t.Errorf("the repository host holds %d merge requests, want 1 — recovery opened a duplicate", len(h.repoHost.MRs))
	}
	if got := countType(h.events(t, "r-rec"), event.TypeMRCreated); got != 1 {
		t.Errorf("mr_created count = %d, want 1", got)
	}
	if v := h.view(t, "r-rec"); v.MRURL != ref.URL {
		t.Errorf("run MR = %q, want the existing %q", v.MRURL, ref.URL)
	}
}

// A reconciliation that cannot reach the repository host must NOT fall through
// to creating one: a duplicate MR is worse than a stage a human can retry.
func TestCreateMRReconcileFailureDoesNotDuplicate(t *testing.T) {
	doc := `name: recerr
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:artifacts/mr.md, type: file } }, on_success: done }
`
	fl := mustFlow(t, doc)
	h := newHarness(t, fl)
	h.repoHost.FindErr = fmt.Errorf("repository unreachable")
	ctx := context.Background()

	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: "recerr", ProjectID: testProject, FlowHash: fl.Hash, Branch: "apothecary/r-recerr"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: "env-recerr"},
		&event.StageStarted{StageID: "mr", Iteration: 1},
		&event.ActionStarted{StageID: "mr", Action: "create_mr"},
	} {
		if _, err := h.store.Append(ctx, "r-recerr", p); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-recerr")

	if len(h.repoHost.MRs) != 0 {
		t.Errorf("created %d merge requests despite an unreadable repository host", len(h.repoHost.MRs))
	}
	if v := h.view(t, "r-recerr"); v.State != run.Failed {
		t.Errorf("state = %s, want failed", v.State)
	}
}
