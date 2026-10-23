package engine

// Parameters (FLOWLANG §1) are resolved once, at run creation: the flow's
// defaults, then the project's bindings, then this attempt's overrides — and
// recorded on run_created, with the entrypoint the run began at.

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
version: 4
parameters:
  harness: fake
  model: anthropic/claude-sonnet-4-6
  verify: verify
  gate: true
  mode: { default: confirm, enum: [auto, confirm, manual] }
phases:
  - id: deliver
    on: run_start
    stages:
      - { id: test, type: command, run_ref: $verify }
  - id: quick
    on: run_start
    stages:
      - { id: fast, type: command, run_ref: $verify }
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
	want := map[string]string{"harness": "fake", "model": "anthropic/claude-sonnet-4-6", "verify": "check", "gate": "false", "mode": "manual",
		"timeout": "30m", "max_turns": "50", "max_cost_usd": "10", "max_total_turns": "300"}
	if len(v.Params) != len(want) {
		t.Errorf("params = %v, want %v", v.Params, want)
	}
	for k, val := range want {
		if v.Params[k] != val {
			t.Errorf("params[%s] = %q, want %q", k, v.Params[k], val)
		}
	}
	if v.BeginPhase != "deliver" || v.StartedBy != "human" {
		t.Errorf("begin_phase %q started_by %q", v.BeginPhase, v.StartedBy)
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

// A run begins at the entrypoint its creation names, and only at one.
func TestRunBeginsAtTheRequestedEntrypoint(t *testing.T) {
	h := newHarness(t, mustFlow(t, paramFlowDoc))
	ctx := context.Background()
	id, err := h.e.CreateRunWith(ctx, Ticket{Title: "q"}, "knobs", testProject, RunInput{Phase: "quick"})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	if v := h.view(t, id); v.BeginPhase != "quick" {
		t.Errorf("begin_phase = %q", v.BeginPhase)
	}
	if _, err := h.e.CreateRunWith(ctx, Ticket{Title: "q"}, "knobs", testProject, RunInput{Phase: "nope"}); err == nil || !strings.Contains(err.Error(), "is not an entrypoint") {
		t.Errorf("unknown phase: err = %v", err)
	}
	fl := mustFlow(t, strings.Replace(paramFlowDoc, "  - id: quick\n    on: run_start\n", "  - id: quick\n    on: manual\n", 1))
	h2 := newHarness(t, fl)
	if _, err := h2.e.CreateRunWith(ctx, Ticket{Title: "q"}, "knobs", testProject, RunInput{Phase: "quick"}); err == nil || !strings.Contains(err.Error(), "is not an entrypoint") {
		t.Errorf("manual phase as beginning: err = %v", err)
	}
}

// A rerun begins where the original did, with its parameter values, unless
// the request says otherwise.
func TestRerunCarriesEntrypointAndParams(t *testing.T) {
	h := newHarness(t, mustFlow(t, paramFlowDoc))
	ctx := context.Background()
	id, err := h.e.CreateRunWith(ctx, Ticket{Title: "carry"}, "knobs", testProject, RunInput{Phase: "quick", Params: map[string]string{"mode": "manual"}})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	again, err := h.e.Rerun(ctx, id, RunInput{})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(again)
	if v := h.view(t, again); v.BeginPhase != "quick" || v.Params["mode"] != "manual" {
		t.Errorf("rerun begin_phase %q params %v", v.BeginPhase, v.Params)
	}
	steered, err := h.e.Rerun(ctx, id, RunInput{Phase: "deliver", Params: map[string]string{"mode": "auto"}})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(steered)
	if v := h.view(t, steered); v.BeginPhase != "deliver" || v.Params["mode"] != "auto" {
		t.Errorf("steered rerun begin_phase %q params %v", v.BeginPhase, v.Params)
	}
}
