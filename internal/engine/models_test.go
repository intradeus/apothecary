package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports"
)

const modelFlowDoc = `name: models
version: 2
parameters: { harness: fake, model: zen/glm-5.1 }
main:
  - { id: build, type: agent, agent: builder }
  - { id: review, type: agent, agent: builder, model: zen/glm-5.3 }
`

// A model the provider retired is refused when the run is created — before a
// workspace is built — naming the stage and the closest models still there.
func TestRunRefusedForAModelItsHarnessCannotReach(t *testing.T) {
	h := newHarness(t, mustFlow(t, modelFlowDoc))
	h.res.Models = func(context.Context, string) ([]string, error) {
		return []string{"zen/glm-5.2", "zen/glm-5.3", "zen/kimi-k3", "other/glm-5.1"}, nil
	}
	_, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "models", testProject)
	if err == nil {
		t.Fatal("run created with a model the harness cannot reach")
	}
	for _, want := range []string{`stage "build"`, `"zen/glm-5.1"`, "zen/glm-5.2, zen/glm-5.3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if strings.Contains(err.Error(), `stage "review"`) || strings.Contains(err.Error(), "other/") {
		t.Errorf("error blames the wrong stage or suggests another provider: %v", err)
	}
	if len(h.envp.ExecCalls) != 0 {
		t.Error("a workspace was touched for a run that could not succeed")
	}

	steps := h.e.PreflightProject(context.Background(), testProject)
	if s := stepNamed(steps, "models"); s == nil || s.OK || !strings.Contains(s.Error, "zen/glm-5.1") {
		t.Errorf("preflight models step = %+v", s)
	}
}

// An unreachable model list is not a verdict on the flow: the run proceeds,
// and preflight says the check could not be made.
func TestUnlistableModelsDoNotBlockTheRun(t *testing.T) {
	h := newHarness(t, mustFlow(t, modelFlowDoc))
	h.res.Models = func(context.Context, string) ([]string, error) {
		return nil, errors.New("provider unreachable")
	}
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "models", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if _, ok := findPayload[*event.RunCompleted](h.events(t, runID)); !ok {
		t.Errorf("run did not complete: %v", eventTypes(h.events(t, runID)))
	}
	steps := h.e.PreflightProject(context.Background(), testProject)
	if s := stepNamed(steps, "models"); s == nil || s.OK || !strings.Contains(s.Error, "could not list models") {
		t.Errorf("preflight models step = %+v", s)
	}
}

func TestPreflightModelsPass(t *testing.T) {
	h := newHarness(t, mustFlow(t, modelFlowDoc))
	h.res.Models = func(context.Context, string) ([]string, error) {
		return []string{"zen/glm-5.1", "zen/glm-5.3"}, nil
	}
	if s := stepNamed(h.e.PreflightProject(context.Background(), testProject), "models"); s == nil || !s.OK {
		t.Errorf("models step = %+v", s)
	}
}

func stepNamed(steps []PreflightStep, id string) *PreflightStep {
	for i := range steps {
		if steps[i].ID == id {
			return &steps[i]
		}
	}
	return nil
}

// A harness that takes any model (nil list) is never refused.
func TestHarnessTakingAnyModelIsNotChecked(t *testing.T) {
	h := newHarness(t, mustFlow(t, modelFlowDoc))
	h.res.Models = func(context.Context, string) ([]string, error) { return nil, nil }
	if mc := checkModels(context.Background(), h.res, h.flowOf(t, "models")); len(mc.missing)+len(mc.unverified) != 0 {
		t.Errorf("check = %+v", mc)
	}
}

// listingRunner is a harness whose agent can be asked for its models.
type listingRunner struct {
	*fake.AgentRunner
	models []string
	err    error
	asked  int
}

func (r *listingRunner) AgentModels(context.Context, ports.EnvID) ([]string, error) {
	r.asked++
	return r.models, r.err
}

// The provider may still serve a model the INSTALLED agent does not know
// (version skew): the run fails before its first stage, not three stages in.
func TestRunFailsBeforeItsFirstStageOnAModelTheAgentDoesNotOffer(t *testing.T) {
	h := newHarness(t, mustFlow(t, modelFlowDoc))
	lr := &listingRunner{AgentRunner: h.runner, models: []string{"zen/glm-5.2", "zen/glm-5.3"}}
	h.res.Runner = lr
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "models", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	rf, ok := findPayload[*event.RunFailed](evs)
	if !ok || !strings.Contains(rf.Reason, `stage "build"`) || !strings.Contains(rf.Reason, "zen/glm-5.2") {
		t.Fatalf("run_failed = %+v; events %v", rf, eventTypes(evs))
	}
	if _, started := findPayload[*event.StageStarted](evs); started {
		t.Error("a stage started on a flow its agent cannot run")
	}
	if n := len(h.runner.SnapshotRunCalls()); n != 0 {
		t.Errorf("agent invoked %d times", n)
	}
}

func TestAgentModelCheckPassesAndToleratesAnUnaskableAgent(t *testing.T) {
	for _, lr := range []*listingRunner{
		{models: []string{"zen/glm-5.1", "zen/glm-5.3"}}, // offered
		{err: errors.New("agent would not start")},       // cannot ask: the stage will say
		{models: nil}, // says nothing
	} {
		h := newHarness(t, mustFlow(t, modelFlowDoc))
		lr.AgentRunner = h.runner
		h.res.Runner = lr
		runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "models", testProject)
		if err != nil {
			t.Fatal(err)
		}
		h.e.Wait(runID)
		if _, ok := findPayload[*event.RunCompleted](h.events(t, runID)); !ok {
			t.Errorf("runner %+v: run did not complete: %v", lr, eventTypes(h.events(t, runID)))
		}
		if lr.asked != 1 {
			t.Errorf("agent asked %d times, want once per harness", lr.asked)
		}
	}
}

func TestEnvCheckAgentModelsStep(t *testing.T) {
	h := newHarness(t, mustFlow(t, modelFlowDoc))
	h.envp.ExecFunc = firewallAwareExec()
	h.res.Runner = &listingRunner{AgentRunner: h.runner, models: []string{"zen/glm-5.3"}}
	var step CheckStep
	RunEnvCheck(context.Background(), h.res, "envck-m",
		EnvCheckOptions{Stages: h.flowOf(t, "models").Stages},
		func(s CheckStep) {
			if s.ID == "agent_models" {
				step = s
			}
		})
	if step.Status != "fail" || !strings.Contains(step.Detail, "zen/glm-5.1") || strings.Contains(step.Detail, `"review"`) {
		t.Fatalf("agent_models = %+v", step)
	}
}

func TestAgentAccepts(t *testing.T) {
	for _, c := range []struct {
		advertised []string
		model      string
		want       bool
	}{
		{[]string{"opencode-go/glm-5.2"}, "opencode-go/glm-5.2", true},
		{[]string{"gpt-5-codex/high", "gpt-5-codex/low"}, "openai/gpt-5-codex", true}, // codex: bare + effort
		{[]string{"gpt-5"}, "openai/gpt-5", true},
		{[]string{"opencode-go/glm-5.2"}, "opencode-go/glm-5.1", false},
		{[]string{"gpt-5-codex/high"}, "openai/gpt-5", false}, // a prefix is not a match
	} {
		if got := agentAccepts(c.advertised, c.model); got != c.want {
			t.Errorf("agentAccepts(%v, %q) = %v", c.advertised, c.model, got)
		}
	}
}

// A provider refusing the harness credential refuses the run before a
// workspace is built; preflight names it too. Each harness is asked about
// exactly the models its stages route through.
func TestRunRefusedWhenTheProviderRejectsTheCredential(t *testing.T) {
	h := newHarness(t, mustFlow(t, modelFlowDoc))
	var asked []string
	h.res.CheckAuth = func(_ context.Context, kind string, models []string) ([]string, []string) {
		asked = append(asked, kind+":"+strings.Join(models, ","))
		return []string{"opencode model zen/glm-5.1: credential rejected (401 Invalid credential)"}, nil
	}
	_, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "models", testProject)
	if err == nil || !strings.Contains(err.Error(), "Invalid credential") {
		t.Fatalf("err = %v, want the provider's refusal", err)
	}
	if len(asked) != 1 || asked[0] != "fake:zen/glm-5.1,zen/glm-5.3" {
		t.Errorf("asked %v", asked)
	}
	if len(h.envp.ExecCalls) != 0 {
		t.Error("a workspace was touched for a run whose credentials were refused")
	}
	if s := stepNamed(h.e.PreflightProject(context.Background(), testProject), "harness_auth"); s == nil || s.OK || !strings.Contains(s.Error, "Invalid credential") {
		t.Errorf("preflight harness_auth step = %+v", s)
	}
}

// A credential that cannot be probed (a subscription login, a provider down)
// does not block the run, nor fail preflight.
func TestUnverifiableCredentialsDoNotBlockTheRun(t *testing.T) {
	h := newHarness(t, mustFlow(t, modelFlowDoc))
	h.res.CheckAuth = func(context.Context, string, []string) ([]string, []string) {
		return nil, []string{"opencode provider zen: not an API key (subscription login)"}
	}
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "models", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if s := stepNamed(h.e.PreflightProject(context.Background(), testProject), "harness_auth"); s == nil || !s.OK {
		t.Errorf("preflight harness_auth step = %+v", s)
	}
}
