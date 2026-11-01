package engine

// A workspace that is not what the log remembers, a service that will not
// come up, a rebuild that fails halfway: the stages' worst days.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
	"apothecary/internal/run/runinput"
)

// replayFor is the state of a run standing in env.
func replayFor(env envport.EnvID) *runcore.State {
	return &runcore.State{RunID: "r1", EnvID: env, Pushed: map[string]string{}}
}

// wobbleEnvp reports the workspace through a scripted sequence of statuses.
type wobbleEnvp struct {
	*fake.EnvProvisioner
	statuses []envport.EnvStatus
	err      error
	seen     int
	reattach error
}

func (w *wobbleEnvp) Status(ctx context.Context, id envport.EnvID) (envport.EnvStatus, error) {
	if w.err != nil {
		return "", w.err
	}
	if w.seen < len(w.statuses) {
		w.seen++
		return w.statuses[w.seen-1], nil
	}
	return w.EnvProvisioner.Status(ctx, id)
}

func (w *wobbleEnvp) Reattach(context.Context, envport.EnvID) error { return w.reattach }

func withFastReprobe(t *testing.T) {
	t.Helper()
	old := recoveryReprobeDelay
	recoveryReprobeDelay = time.Millisecond
	t.Cleanup(func() { recoveryReprobeDelay = old })
}

// A container caught mid-restart is probed again, not re-provisioned: that
// would throw away what is in it.
func TestRecoveryRidesOutAContainerRestarting(t *testing.T) {
	withFastReprobe(t)
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.seedLive(t, "r1", "loop", &event.StageStarted{StageID: "build", Iteration: 1, Harness: "fake", Model: "fake/model"})
	h.res.Envp = &wobbleEnvp{EnvProvisioner: h.envp, statuses: []envport.EnvStatus{envport.EnvStarting}}
	v := recoverAndSettle(t, h, "r1")
	if v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(h.events(t, "r1")))
	}
	if countType(h.events(t, "r1"), event.TypeEnvRequested) != 0 || countType(h.events(t, "r1"), event.TypeEnvReady) != 1 {
		t.Errorf("re-provisioned a workspace that came back: %v", eventTypes(h.events(t, "r1")))
	}
}

// A provisioner that cannot say how the workspace is: the run waits for the
// next boot, whatever the workspace holds is left alone.
func TestRecoveryStopsWhenItCannotProbeTheWorkspace(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.seedLive(t, "r1", "loop", &event.StageStarted{StageID: "build", Iteration: 1, Harness: "fake", Model: "fake/model"})
	h.res.Envp = &wobbleEnvp{EnvProvisioner: h.envp, err: errors.New("daemon down")}
	v := recoverAndSettle(t, h, "r1")
	if v.State.Terminal() {
		t.Errorf("state = %s", v.State)
	}
	if len(h.runner.SnapshotRunCalls()) != 0 {
		t.Error("an agent ran in a workspace nobody could probe")
	}
}

func TestRecoveryStopsWhenTheTunnelWillNotComeBack(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.seedLive(t, "r1", "loop", &event.StageStarted{StageID: "build", Iteration: 1, Harness: "fake", Model: "fake/model"})
	h.res.Envp = &wobbleEnvp{EnvProvisioner: h.envp, reattach: errors.New("no route")}
	v := recoverAndSettle(t, h, "r1")
	if v.State.Terminal() || len(h.runner.SnapshotRunCalls()) != 0 {
		t.Errorf("state = %s, %d agents ran", v.State, len(h.runner.SnapshotRunCalls()))
	}
}

// Credentials that cannot be put back: the run does not go on without them.
func TestRecoveryStopsWhenCredentialsCannotBeReseeded(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.seedLive(t, "r1", "loop", &event.StageStarted{StageID: "build", Iteration: 1, Harness: "fake", Model: "fake/model"})
	h.res.SeedAuth = func(context.Context, envport.EnvID, func(string)) error { return errors.New("keychain locked") }
	v := recoverAndSettle(t, h, "r1")
	if v.State.Terminal() || len(h.runner.SnapshotRunCalls()) != 0 {
		t.Errorf("state = %s, %d agents ran", v.State, len(h.runner.SnapshotRunCalls()))
	}
}

// A run aborted while its workspace builds stops there.
func TestAbortDuringProvisioning(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	entered := make(chan struct{})
	h.res.Envp = blockProvision{h.envp, entered}
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := h.e.Abort(id); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	if v := h.view(t, id); v.State != run.Abandoned {
		t.Errorf("state = %s", v.State)
	}
	if len(h.runner.SnapshotRunCalls()) != 0 {
		t.Error("an agent ran")
	}
}

type blockProvision struct {
	*fake.EnvProvisioner
	entered chan struct{}
}

func (b blockProvision) Provision(ctx context.Context, _ envport.EnvSpec) (envport.EnvID, error) {
	close(b.entered)
	<-ctx.Done()
	return "", ctx.Err()
}

// Bootstrap failing leaves the workspace on record, as evidence, and its
// failure on the run.
func TestBootstrapFailureRecordsTheWorkspace(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "git clone") || strings.Contains(cmd.Script, "git switch") {
			return envport.ExecResult{ExitCode: 128, Output: "fatal: could not read"}, nil
		}
		return envport.ExecResult{}, nil
	}
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	f, ok := findPayload[*event.EnvFailed](h.events(t, id))
	if !ok || f.EnvID == "" || !strings.Contains(f.Error, "could not read") {
		t.Fatalf("env_failed = %+v", f)
	}
	if s := h.view(t, id).State; s != run.Failed {
		t.Errorf("state = %s", s)
	}
}

// --- services ----------------------------------------------------------------

const svcDoc = `name: loop
version: 2
parameters: { harness: fake, model: fake/model, app: { type: string, default: default } }
main:
  - { id: up, type: service, service: $app, do: DO, on_fail: abort }
`

func TestServiceStageFailures(t *testing.T) {
	boom := errors.New("sidecar down")
	failing := func(verb string) func(*fake.Env, envport.Command) (envport.ExecResult, error) {
		return func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
			if strings.HasPrefix(cmd.Script, "apothecary-shim service "+verb) {
				return envport.ExecResult{}, boom
			}
			return envport.ExecResult{}, nil
		}
	}
	t.Run("a start that cannot reach the supervisor", func(t *testing.T) {
		h, _ := serviceHarness(t, strings.Replace(svcDoc, "DO", "start", 1))
		h.envp.ExecFunc = failing("start")
		id, sf := runOut(t, h, runinput.Ticket{}, "loop", runinput.Input{})
		wantFailure(t, sf, event.ReasonInfra, "sidecar down")
		if s := h.view(t, id).State; s != run.Failed {
			t.Errorf("state = %s", s)
		}
	})
	t.Run("a stop that cannot reach the supervisor", func(t *testing.T) {
		h, _ := serviceHarness(t, strings.Replace(svcDoc, "DO", "stop", 1))
		h.envp.ExecFunc = failing("stop")
		_, sf := runOut(t, h, runinput.Ticket{}, "loop", runinput.Input{})
		wantFailure(t, sf, event.ReasonInfra, "sidecar down")
	})
	t.Run("a restart restarts", func(t *testing.T) {
		h, svcs := serviceHarness(t, strings.Replace(svcDoc, "DO", "restart", 1))
		id, sf := runOut(t, h, runinput.Ticket{}, "loop", runinput.Input{})
		if sf != nil {
			t.Fatalf("failed: %+v", sf)
		}
		started, ok := findPayload[*event.ServiceStarted](h.events(t, id))
		if !ok || !started.Restart {
			t.Errorf("service_started = %+v", started)
		}
		found := false
		for _, c := range svcs.Snapshot() {
			found = found || strings.Contains(c, "--restart")
		}
		if !found {
			t.Errorf("calls = %v", svcs.Snapshot())
		}
	})
	t.Run("a service gone from the project", func(t *testing.T) {
		h, _ := serviceHarness(t, `name: loop
version: 2
parameters: { harness: fake, model: fake/model, app: { type: string, default: default } }
main:
  - { id: first, type: command, run: "./first.sh" }
  - { id: up, type: service, service: $app }
`)
		inner := h.envp.ExecFunc
		h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
			if strings.Contains(cmd.Script, "./first.sh") {
				h.res.Services = nil
			}
			return inner(env, cmd)
		}
		id, sf := runOut(t, h, runinput.Ticket{}, "loop", runinput.Input{})
		wantFailure(t, sf, event.ReasonInfra, `service "default" no longer exists`)
		if s := h.view(t, id).State; s != run.Failed {
			t.Errorf("state = %s", s)
		}
	})
	t.Run("its log cannot be read", func(t *testing.T) {
		h, svcs := serviceHarness(t, strings.Replace(svcDoc, "DO", "start", 1))
		svcs.Outcomes["default"] = []string{fake.OutcomeExits}
		inner := h.envp.ExecFunc
		h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
			if strings.HasPrefix(cmd.Script, "apothecary-shim service logs") {
				return envport.ExecResult{}, boom
			}
			return inner(env, cmd)
		}
		_, sf := runOut(t, h, runinput.Ticket{}, "loop", runinput.Input{})
		wantFailure(t, sf, event.ReasonExitNonzero, "(its log could not be read:")
	})
}

// What an agent reads for run.service.<name>: the address while it is up,
// plainly why not otherwise.
func TestServiceInputSaysWhatItKnows(t *testing.T) {
	const doc = `name: loop
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: first, type: command, run: "./first.sh" }
`
	h, svcs := serviceHarness(t, doc)
	h.res.Services = append(h.res.Services, resolution.Service{Name: "worker", Start: "w", Health: resolution.ServiceHealth{Command: "true"}})
	ctx := context.Background()
	env, err := h.envp.Provision(ctx, envport.EnvSpec{})
	if err != nil {
		t.Fatal(err)
	}
	x := &exec{e: h.e, ctx: ctx, res: h.res, rs: replayFor(env)}

	if got := x.serviceInput("gone"); !strings.Contains(got, `the project has no service "gone"`) {
		t.Errorf("no service: %q", got)
	}
	if got := x.serviceInput("default"); !strings.Contains(got, "is not running") {
		t.Errorf("not started: %q", got)
	}
	if got := x.serviceLink("default"); got != "" {
		t.Errorf("a link to a service that is down: %q", got)
	}
	if got := x.serviceLink("gone"); got != "" {
		t.Errorf("a link to no service: %q", got)
	}
	start := func(name string) {
		t.Helper()
		if _, err := h.envp.Exec(ctx, env, envport.Command{Script: "apothecary-shim service start --wait <<'EOF'\n{\"name\":\"" + name + "\",\"timeout\":\"1m\"}\nEOF"}); err != nil {
			t.Fatal(err)
		}
	}
	start("default")
	start("worker")
	if got := x.serviceInput("default"); !strings.Contains(got, "is running and healthy") {
		t.Errorf("up: %q (%v)", got, svcs.Snapshot())
	}
	if got := x.serviceInput("worker"); !strings.Contains(got, "it declares no port") {
		t.Errorf("no port: %q", got)
	}
	inner := h.envp.ExecFunc
	h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.HasPrefix(cmd.Script, "apothecary-shim service status") {
			return envport.ExecResult{}, errors.New("sidecar down")
		}
		return inner(env, cmd)
	}
	if got := x.serviceInput("default"); !strings.Contains(got, "could not be checked: ") {
		t.Errorf("unreachable: %q", got)
	}
}

// A run that ended stops what its stages started, whatever ended it; a
// supervisor that cannot answer is logged.
func TestEndingARunStopsItsStageServices(t *testing.T) {
	const doc = `name: loop
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: up, type: service, service: db }
  - { id: bad, type: command, run: "./bad.sh" }
`
	t.Run("stopped", func(t *testing.T) {
		h, svcs := serviceHarness(t, doc)
		inner := h.envp.ExecFunc
		h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
			if strings.Contains(cmd.Script, "./bad.sh") {
				return envport.ExecResult{ExitCode: 1}, nil
			}
			return inner(env, cmd)
		}
		id, _ := runOut(t, h, runinput.Ticket{}, "loop", runinput.Input{})
		if s := h.view(t, id).State; s != run.Abandoned {
			t.Fatalf("state = %s", s)
		}
		stopped := false
		for _, c := range svcs.Snapshot() {
			stopped = stopped || c == "stop db"
		}
		if !stopped {
			t.Errorf("calls = %v", svcs.Snapshot())
		}
		if ss, ok := findPayload[*event.ServiceStopped](h.events(t, id)); !ok || ss.By != event.ServiceByEngine {
			t.Errorf("service_stopped = %+v", ss)
		}
	})
	t.Run("the supervisor is gone", func(t *testing.T) {
		h, _ := serviceHarness(t, doc)
		inner := h.envp.ExecFunc
		h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
			switch {
			case strings.Contains(cmd.Script, "./bad.sh"):
				return envport.ExecResult{ExitCode: 1}, nil
			case strings.HasPrefix(cmd.Script, "apothecary-shim service status"), strings.HasPrefix(cmd.Script, "apothecary-shim service stop"):
				return envport.ExecResult{}, errors.New("sidecar down")
			}
			return inner(env, cmd)
		}
		id, _ := runOut(t, h, runinput.Ticket{}, "loop", runinput.Input{})
		if s := h.view(t, id).State; s != run.Abandoned {
			t.Errorf("state = %s", s)
		}
		if countType(h.events(t, id), event.TypeServiceStopped) != 0 {
			t.Error("a stop nobody confirmed was recorded")
		}
	})
}

// --- rebuild -----------------------------------------------------------------

func TestRebuildRefusals(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	wantErrText(t, h.e.RebuildEnv(ctx, "ghost", false), "run ghost not found")
	h.seed(t, "noenv", created(testProject), &event.RunCompleted{})
	wantErrText(t, h.e.RebuildEnv(ctx, "noenv", false), "has no workspace to rebuild")
	h.seed(t, "noproject", created(""), &event.EnvReady{EnvID: "e"}, &event.RunCompleted{})
	wantErrText(t, h.e.RebuildEnv(ctx, "noproject", false), "has no project (pre-v2)")
}

func TestRebuildFailures(t *testing.T) {
	ctx := context.Background()
	rebuildHarness := func(t *testing.T) (*harness, *rebuilderEnvp, string) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		r := &rebuilderEnvp{EnvProvisioner: h.envp}
		h.res.Envp = r
		return h, r, completedWithEnv(t, h)
	}
	finished := func(t *testing.T, h *harness, id, want string) {
		t.Helper()
		f, ok := findPayload[*event.EnvRebuildFailed](h.events(t, id))
		if !ok || !strings.Contains(f.Error, want) {
			t.Fatalf("env_rebuild_failed = %+v", f)
		}
		if h.view(t, id).Rebuilding {
			t.Error("the run still reads as rebuilding")
		}
	}
	t.Run("the provisioner", func(t *testing.T) {
		h, r, id := rebuildHarness(t)
		r.err = errors.New("image pull failed")
		wantErrText(t, h.e.RebuildEnv(ctx, id, false), "image pull failed")
		finished(t, h, id, "image pull failed")
	})
	t.Run("the bootstrap", func(t *testing.T) {
		h, _, id := rebuildHarness(t)
		h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
			if strings.Contains(cmd.Script, "git switch") {
				return envport.ExecResult{ExitCode: 128, Output: "no branch"}, nil
			}
			return envport.ExecResult{}, nil
		}
		wantErrText(t, h.e.RebuildEnv(ctx, id, false), "bootstrap")
		finished(t, h, id, "bootstrap")
	})
	t.Run("the flow is gone", func(t *testing.T) {
		h, _, id := rebuildHarness(t)
		h.e.flows = funcSource(func(string) (*flow.Flow, error) { return nil, errors.New("db down") })
		wantErrText(t, h.e.RebuildEnv(ctx, id, false), "load flow: db down")
		if countType(h.events(t, id), event.TypeEnvRebuildStarted) != 0 {
			t.Error("a refused rebuild started")
		}
	})
	t.Run("the project will not resolve", func(t *testing.T) {
		h, _, id := rebuildHarness(t)
		h.e.resolver = failingResolver{errors.New("vault sealed")}
		wantErrText(t, h.e.RebuildEnv(ctx, id, false), "vault sealed")
	})
	t.Run("the failure cannot be recorded", func(t *testing.T) {
		store := &flakyStore{typ: event.TypeEnvRebuildFailed}
		store.left.Store(1)
		h := newHarnessOn(t, func(m *event.MemStore) event.Store { store.Store = m; return store }, mustFlow(t, loopFlowDoc))
		r := &rebuilderEnvp{EnvProvisioner: h.envp, err: errors.New("image pull failed")}
		h.res.Envp = r
		id := completedWithEnv(t, h)
		wantErrText(t, h.e.RebuildEnv(ctx, id, false), "image pull failed")
	})
}

// --- secrets ------------------------------------------------------------------

type stoppingRunner struct {
	*fake.AgentRunner
	stopped int
	err     error
}

func (s *stoppingRunner) StopAgent(context.Context, envport.EnvID) error {
	s.stopped++
	return s.err
}

// A finished run stops the agent holding credentials in memory, once per
// workspace, and removes what it can, whatever else failed.
func TestScrubStopsTheAgentAndSurvivesFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("stops the agent even when the stop fails", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		r := &stoppingRunner{AgentRunner: h.runner, err: errors.New("no such process")}
		h.res.Runner = r
		h.res.HarnessKinds = []string{"fake", "other"}
		id := completedWithEnv(t, h)
		if r.stopped != 1 {
			t.Errorf("stopped %d times, want once for the workspace", r.stopped)
		}
		if !hasScript(h.execScripts(), "rm -f") {
			t.Errorf("a failed stop skipped the scrub: %v", id)
		}
	})
	t.Run("a project that will not resolve is logged", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.e.resolver = failingResolver{errors.New("vault sealed")}
		before := len(h.execScripts())
		h.e.ScrubWorkspaceSecrets(ctx, run.View{RunID: "r1", EnvID: "e", ProjectID: testProject})
		if len(h.execScripts()) != before {
			t.Error("a scrub ran with nothing resolved")
		}
	})
	t.Run("a failed remove is logged, not raised", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		env, err := h.envp.Provision(ctx, envport.EnvSpec{})
		if err != nil {
			t.Fatal(err)
		}
		h.res.Envp = refuseRm{h.envp, envport.ExecResult{ExitCode: 1, Output: "read-only"}, nil}
		h.e.ScrubWorkspaceSecrets(ctx, run.View{RunID: "r1", EnvID: string(env), ProjectID: testProject})
		h.res.Envp = refuseRm{h.envp, envport.ExecResult{}, errors.New("gone")}
		h.e.ScrubWorkspaceSecrets(ctx, run.View{RunID: "r1", EnvID: string(env), ProjectID: testProject})
	})
}

type refuseRm struct {
	*fake.EnvProvisioner
	res envport.ExecResult
	err error
}

func (r refuseRm) Exec(ctx context.Context, id envport.EnvID, cmd envport.Command) (envport.ExecResult, error) {
	if strings.HasPrefix(cmd.Script, "rm -f") {
		return r.res, r.err
	}
	return r.EnvProvisioner.Exec(ctx, id, cmd)
}

// --- CI ------------------------------------------------------------------------

func TestCIFailureTextSaysWhatIsKnown(t *testing.T) {
	cases := []struct {
		rep  *event.CIReport
		want []string
	}{
		{nil, []string{"(CI was not read for this chain)"}},
		{&event.CIReport{Head: "abcdefabcdefabcdef", Error: "reading its CI: 500"}, []string{"could not be read (reading its CI: 500)"}},
		{&event.CIReport{Head: "abcdef"}, []string{"No CI check is failing on abcdef."}},
		{&event.CIReport{Head: "abcdefabcdefabcdef", Checks: []event.CICheck{{Name: "unit", File: ".ci.yml", Job: "unit-tests", Stage: "test",
			Steps: []string{"run tests"}, Summary: "2 failed", URL: "https://ci/1"}}},
			[]string{"CI failed on abcdefabcdef:", ".ci.yml", `job "unit-tests"`, "stage test", `failed step "run tests"`, ": 2 failed", "(https://ci/1)", "The logs are not included"}},
	}
	for _, c := range cases {
		got := ciFailureText(c.rep)
		for _, want := range c.want {
			if !strings.Contains(got, want) {
				t.Errorf("text = %q, want %q", got, want)
			}
		}
	}
}

func TestReadCISaysWhatItCouldNotRead(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ref := h.repoHost.AddOpenMR("", "t", "feat", "main", "head-1")
	co := event.Checkout{Name: event.PrimaryCheckoutName, Branch: "feat", Primary: true}
	x := &exec{e: h.e, ctx: context.Background(), res: h.res, rs: replayFor("env-1")}
	x.rs.Checkouts = []event.Checkout{co}

	if rep := x.readCI(event.PrimaryCheckoutName, ""); !strings.Contains(rep.Error, "no merge request to read CI from") {
		t.Errorf("no MR: %+v", rep)
	}
	x.rs.AddMR(&event.MRCreated{Repo: event.PrimaryCheckoutName, Branch: "feat", MRURL: ref.URL, MRIID: ref.IID})
	h.repoHost.SetCIFailures(ref.IID, repoport.CIFailure{Name: "unit"})
	rep := x.readCI(event.PrimaryCheckoutName, "")
	if rep.Head != "head-1" || len(rep.Checks) != 1 || rep.Error != "" {
		t.Errorf("read = %+v", rep)
	}
	h.repoHost.SetMRStateErr(errors.New("host down"))
	if rep := x.readCI(event.PrimaryCheckoutName, ""); !strings.Contains(rep.Error, "reading the merge request: host down") {
		t.Errorf("no status: %+v", rep)
	}
	if rep := x.readCI(event.PrimaryCheckoutName, "head-2"); !strings.Contains(rep.Error, "reading its CI: host down") || rep.Head != "head-2" {
		t.Errorf("no CI: %+v", rep)
	}
}
