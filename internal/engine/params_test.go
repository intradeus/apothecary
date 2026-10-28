package engine

// Parameters (FLOWLANG §1) are resolved once, at run creation: the flow's
// defaults, then the project's bindings, then this attempt's overrides — and
// recorded on run_created.

import (
	"context"
	"strings"
	"testing"
)

// staticBindings is a FlowBindings that answers the same values for every
// project and flow.
type staticBindings map[string]string

func (b staticBindings) Binding(context.Context, string, string) (map[string]string, error) {
	return b, nil
}

const paramFlowDoc = `name: knobs
version: 5
parameters:
  harness: fake
  model: fake/model
  verify: { type: string, default: verify }
  gate: { type: bool, default: true }
  mode: { type: string, default: confirm, enum: [auto, confirm, manual] }
main:
  - { id: test, type: command, run_ref: $verify, on_success: done }
`

func TestParamsResolvedAtCreation(t *testing.T) {
	h := newHarness(t, mustFlow(t, paramFlowDoc))
	h.e.SetFlowBindings(staticBindings{"verify": "check", "gate": "false"})
	h.res.Commands["check"] = "true"
	ctx := context.Background()

	id, err := h.e.CreateRunWith(ctx, Ticket{Title: "knobs"}, "knobs", testProject, RunInput{Params: map[string]string{"mode": "manual"}})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	v := h.view(t, id)
	want := map[string]string{"harness": "fake", "model": "fake/model", "verify": "check", "gate": "false", "mode": "manual",
		"timeout": "30m", "max_turns": "50", "max_cost_usd": "10", "max_total_turns": "300"}
	if len(v.Params) != len(want) {
		t.Errorf("params = %v, want %v", v.Params, want)
	}
	for k, val := range want {
		if v.Params[k] != val {
			t.Errorf("params[%s] = %q, want %q", k, v.Params[k], val)
		}
	}
	if v.StartedBy != "human" {
		t.Errorf("started_by %q", v.StartedBy)
	}
	if v.State != "completed" {
		t.Errorf("state = %s", v.State)
	}
}

// A binding for a parameter the flow no longer declares is dropped (a stale
// project setting must not block runs); an override for one, or a value
// outside a parameter's enum, refuses the run.
func TestParamsBindingAndOverrideRules(t *testing.T) {
	h := newHarness(t, mustFlow(t, paramFlowDoc))
	h.e.SetFlowBindings(staticBindings{"gone": "x"})
	ctx := context.Background()

	id, err := h.e.CreateRun(ctx, Ticket{Title: "stale"}, "knobs", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	if _, ok := h.view(t, id).Params["gone"]; ok {
		t.Errorf("a stale binding should be dropped: %v", h.view(t, id).Params)
	}
	for _, tc := range []struct {
		params map[string]string
		substr string
	}{
		{map[string]string{"nope": "1"}, `parameter "nope" is not declared`},
		{map[string]string{"mode": "later"}, "takes one of auto | confirm | manual"},
		{map[string]string{"gate": "maybe"}, "takes true or false"},
	} {
		if _, err := h.e.CreateRunWith(ctx, Ticket{Title: "bad"}, "knobs", testProject, RunInput{Params: tc.params}); err == nil || !strings.Contains(err.Error(), tc.substr) {
			t.Errorf("params %v: err = %v, want %q", tc.params, err, tc.substr)
		}
	}
}

// A bound value goes through preflight like a literal: a run_ref the project
// has no command for refuses the run.
func TestBoundRunRefIsPreflighted(t *testing.T) {
	h := newHarness(t, mustFlow(t, paramFlowDoc))
	h.e.SetFlowBindings(staticBindings{"verify": "missing"})
	_, err := h.e.CreateRun(context.Background(), Ticket{Title: "x"}, "knobs", testProject)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v, want a preflight refusal naming the bound run_ref", err)
	}
}

// A rerun runs with the original's parameter values, unless the request
// says otherwise.
func TestRerunCarriesParams(t *testing.T) {
	h := newHarness(t, mustFlow(t, paramFlowDoc))
	ctx := context.Background()
	id, err := h.e.CreateRunWith(ctx, Ticket{Title: "carry"}, "knobs", testProject, RunInput{Params: map[string]string{"mode": "manual"}})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	again, err := h.e.Rerun(ctx, id, RunInput{})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(again)
	if v := h.view(t, again); v.Params["mode"] != "manual" {
		t.Errorf("rerun params %v", v.Params)
	}
	steered, err := h.e.Rerun(ctx, id, RunInput{Params: map[string]string{"mode": "auto"}})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(steered)
	if v := h.view(t, steered); v.Params["mode"] != "auto" {
		t.Errorf("steered rerun params %v", v.Params)
	}
}

const boundAgentDoc = `name: bound
version: 5
parameters:
  harness: fake
  model: fake/model
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`

// A bound harness lands on every agent stage the way the flow's own would,
// so preflight holds it to the project's harness set — at run creation and
// in the project's preflight alike.
func TestBoundHarnessOutsideTheSetIsRefused(t *testing.T) {
	h := newHarness(t, mustFlow(t, boundAgentDoc))
	h.e.SetFlowBindings(staticBindings{"harness": "codex"})
	ctx := context.Background()
	_, err := h.e.CreateRun(ctx, Ticket{Title: "x"}, "bound", testProject)
	if err == nil || !strings.Contains(err.Error(), `"codex"`) || !strings.Contains(err.Error(), "harness set") {
		t.Fatalf("err = %v, want a preflight refusal naming the bound harness", err)
	}
	if s := stepNamed(h.e.PreflightProject(ctx, testProject), "flow_refs"); s == nil || s.OK || !strings.Contains(s.Error, `"codex"`) {
		t.Errorf("flow_refs step = %+v", s)
	}
	h.e.SetFlowBindings(staticBindings{"harness": "fake"})
	if s := stepNamed(h.e.PreflightProject(ctx, testProject), "flow_refs"); s == nil || !s.OK {
		t.Errorf("flow_refs with the set's own kind = %+v", s)
	}
}

// A bound model is checked against what its harness lists, like a stage's
// own pin: before any workspace exists, and in the project's preflight.
func TestBoundModelIsCheckedAgainstTheHarnessList(t *testing.T) {
	h := newHarness(t, mustFlow(t, boundAgentDoc))
	h.res.Models = func(context.Context, string) ([]string, error) { return []string{"fake/model"}, nil }
	ctx := context.Background()
	if s := stepNamed(h.e.PreflightProject(ctx, testProject), "models"); s == nil || !s.OK {
		t.Fatalf("the flow's own model refused: %+v", s)
	}
	h.e.SetFlowBindings(staticBindings{"model": "fake/other"})
	_, err := h.e.CreateRun(ctx, Ticket{Title: "x"}, "bound", testProject)
	if err == nil || !strings.Contains(err.Error(), "preflight") || !strings.Contains(err.Error(), "fake/other") {
		t.Fatalf("err = %v, want a preflight refusal naming the bound model", err)
	}
	if s := stepNamed(h.e.PreflightProject(ctx, testProject), "models"); s == nil || s.OK || !strings.Contains(s.Error, "fake/other") {
		t.Errorf("models step = %+v", s)
	}
}
