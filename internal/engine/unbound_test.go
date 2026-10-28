package engine

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/event"
)

// A flow that leaves its model to the project: the document is fine, the
// run is refused until the project binds a value or the run brings one.
const unboundFlowDoc = `name: unbound
version: 5
parameters:
  harness: fake
  review_model: { type: string, default: $model, description: "the reviewer's model" }
agents:
  - name: builder
    prompt: build
main:
  - { id: build, type: agent, agent: builder, harness: $harness, model: $model }
  - { id: review, type: agent, agent: builder, harness: $harness, model: $review_model, on_success: done }
`

func TestUnboundModelRefusesTheRunUntilBound(t *testing.T) {
	h := newHarness(t, mustFlow(t, unboundFlowDoc))
	ctx := context.Background()
	_, err := h.e.CreateRun(ctx, Ticket{Title: "T"}, "unbound", testProject)
	if err == nil || !strings.Contains(err.Error(), "preflight: parameters") || !strings.Contains(err.Error(), `"model" has no value (read by build, review)`) || !strings.Contains(err.Error(), "Configure") {
		t.Fatalf("err = %v, want the unbound refusal naming the parameter, its stages and where a value comes from", err)
	}
	if len(h.envp.ExecCalls) != 0 {
		t.Error("a workspace was touched for a run that could not say what it runs on")
	}
	if s := stepNamed(h.e.PreflightProject(ctx, testProject), "parameters"); s == nil || s.OK || !strings.Contains(s.Error, `"model" has no value`) {
		t.Errorf("preflight parameters step = %+v", s)
	}

	// The run may bring the value itself.
	id, err := h.e.CreateRunWith(ctx, Ticket{Title: "T"}, "unbound", testProject, RunInput{Params: map[string]string{"model": "fake/model"}})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	v := h.view(t, id)
	if v.Params["model"] != "fake/model" || v.Params["review_model"] != "fake/model" {
		t.Errorf("params = %v: review_model follows model through its default", v.Params)
	}
	if _, ok := findPayload[*event.RunCompleted](h.events(t, id)); !ok {
		t.Errorf("run did not complete: %v", eventTypes(h.events(t, id)))
	}

	// Or the project binds it, and a run override still wins.
	h.e.SetFlowBindings(staticBindings{"model": "fake/model"})
	if s := stepNamed(h.e.PreflightProject(ctx, testProject), "parameters"); s == nil || !s.OK {
		t.Errorf("preflight parameters step once bound = %+v", s)
	}
	id, err = h.e.CreateRunWith(ctx, Ticket{Title: "T"}, "unbound", testProject, RunInput{Params: map[string]string{"review_model": "fake/other"}})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	v = h.view(t, id)
	if v.Params["model"] != "fake/model" || v.Params["review_model"] != "fake/other" {
		t.Errorf("params = %v: the bound model, the overridden review model", v.Params)
	}
	// The last two agent calls are this run's build and review.
	calls := h.runner.SnapshotRunCalls()
	if len(calls) >= 2 {
		calls = calls[len(calls)-2:]
	}
	if len(calls) != 2 || calls[0].Spec.Model != "fake/model" || calls[1].Spec.Model != "fake/other" {
		models := make([]string, len(calls))
		for i, c := range calls {
			models[i] = c.Spec.Model
		}
		t.Errorf("stages ran on %v, want [fake/model fake/other]", models)
	}
}
