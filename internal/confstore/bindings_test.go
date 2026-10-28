package confstore

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// knobsFlow declares every kind of parameter a project may bind: strings,
// a bool, an enum, and the engine-known names.
const knobsFlow = `name: knobs
version: 5
parameters:
  harness: fake
  model: fake/model
  verify: { type: string, default: verify, description: "the project command that runs the tests" }
  gate: { type: bool, default: true }
  mode: { type: string, default: confirm, enum: [auto, confirm, manual] }
agents: []
main:
  - { id: check, type: command, run_ref: $verify }
  - { id: ask, type: human_gate, run_if: $gate, question: "ok?", on_reject: abort }
`

// A binding is the project's layer over a flow's declared parameters: any
// declared name (the engine-known six included), values the parameter
// accepts, an empty value for "unbound" — and nothing of it in the document.
func TestFlowBindingRoundtrip(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	p := seedProject(t, svc)
	row, err := svc.SaveFlow(ctx, p.ID, "knobs", knobsFlow, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetFlowBinding(ctx, p.ID, "knobs"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("binding before any put: err = %v, want not found", err)
	}
	if m, err := svc.Binding(ctx, p.ID, "knobs"); m != nil || err != nil {
		t.Fatalf("Binding before any put = %v, %v, want nil, nil", m, err)
	}

	b, err := svc.PutFlowBinding(ctx, p.ID, "knobs", map[string]string{
		"verify": " test ", "gate": "false", "mode": "auto", "timeout": "1h", "harness": "  ", "model": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"verify": "test", "gate": "false", "mode": "auto", "timeout": "1h"}
	if len(b.Params) != len(want) || b.UpdatedAt.IsZero() {
		t.Fatalf("binding = %+v, want params %v", b, want)
	}
	for k, v := range want {
		if b.Params[k] != v {
			t.Errorf("params[%s] = %q, want %q", k, b.Params[k], v)
		}
	}
	got, err := svc.GetFlowBinding(ctx, p.ID, "knobs")
	if err != nil || got.FlowName != "knobs" || got.Params["mode"] != "auto" || !got.UpdatedAt.Equal(b.UpdatedAt) {
		t.Errorf("get = %+v, %v", got, err)
	}
	list, err := svc.ListFlowBindings(ctx, p.ID)
	if err != nil || len(list) != 1 || list[0].FlowName != "knobs" {
		t.Errorf("list = %+v, %v", list, err)
	}
	if m, err := svc.Binding(ctx, p.ID, "knobs"); err != nil || m["verify"] != "test" || len(m) != 4 {
		t.Errorf("Binding = %v, %v", m, err)
	}
	// The document is untouched: same text, same hash.
	if after, err := svc.GetFlow(ctx, p.ID, "knobs"); err != nil || after.Hash != row.Hash || after.YAML != knobsFlow {
		t.Errorf("the binding reached the document: %+v, %v", after, err)
	}
	// A later put replaces the whole binding, and clearing every value
	// removes it: the project is back to the flow's defaults.
	if b2, err := svc.PutFlowBinding(ctx, p.ID, "knobs", map[string]string{"mode": "manual"}); err != nil || len(b2.Params) != 1 {
		t.Fatalf("second put = %+v, %v", b2, err)
	}
	if b3, err := svc.PutFlowBinding(ctx, p.ID, "knobs", map[string]string{"verify": "", "mode": " "}); err != nil || len(b3.Params) != 0 {
		t.Fatalf("clearing put = %+v, %v", b3, err)
	}
	if _, err := svc.GetFlowBinding(ctx, p.ID, "knobs"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after clearing: err = %v, want not found", err)
	}
	if err := svc.DeleteFlowBinding(ctx, p.ID, "knobs"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete of nothing: err = %v, want not found", err)
	}
}

// What a binding may not say: a name the flow does not declare, a value
// outside an enum or of the wrong type, an engine-known value the flow's
// own rules refuse — each ErrInvalid naming the parameter — and a project
// or flow that does not exist.
func TestFlowBindingValidates(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	p := seedProject(t, svc)
	if _, err := svc.SaveFlow(ctx, p.ID, "knobs", knobsFlow, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		params map[string]string
		name   string
	}{
		{map[string]string{"nope": "x"}, "nope"},
		{map[string]string{"mode": "sometimes"}, "mode"},
		{map[string]string{"gate": "maybe"}, "gate"},
		{map[string]string{"timeout": "soon"}, "timeout"},
		{map[string]string{"model": "no-provider"}, "model"},
		{map[string]string{"verify": "make test"}, "verify"},
	} {
		_, err := svc.PutFlowBinding(ctx, p.ID, "knobs", tc.params)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.name) {
			t.Errorf("%v: err = %v, want ErrInvalid naming %q", tc.params, err, tc.name)
		}
	}
	if _, err := svc.GetFlowBinding(ctx, p.ID, "knobs"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a refused put stored something: %v", err)
	}
	if _, err := svc.PutFlowBinding(ctx, "nope", "knobs", map[string]string{"gate": "false"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: err = %v", err)
	}
	if _, err := svc.PutFlowBinding(ctx, p.ID, "nope", map[string]string{"gate": "false"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown flow: err = %v", err)
	}
}

// A built-in template is bindable without being copied: the project keeps
// running the template, configured. Resetting a copied template (deleting
// the copy) keeps the binding; deleting a flow of the project's own takes
// its binding along; deleting the project takes every binding.
func TestFlowBindingFollowsTheFlow(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	p := seedProject(t, svc)
	tmpl := "builder"
	if _, err := svc.GetFlow(ctx, p.ID, tmpl); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the project has its own %s already: %v", tmpl, err)
	}
	if _, err := svc.PutFlowBinding(ctx, p.ID, tmpl, map[string]string{"harness": "fake", "plan_gate": "false"}); err != nil {
		t.Fatalf("binding the template: %v", err)
	}
	if m, err := svc.Binding(ctx, p.ID, tmpl); err != nil || m["plan_gate"] != "false" {
		t.Fatalf("template binding = %v, %v", m, err)
	}
	if _, err := svc.SaveFlow(ctx, p.ID, "knobs", knobsFlow, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutFlowBinding(ctx, p.ID, "knobs", map[string]string{"mode": "auto"}); err != nil {
		t.Fatal(err)
	}

	// Adopt the template, then reset to it: the project points elsewhere
	// meanwhile so the copy is deletable, and the binding survives.
	if _, err := svc.CopyTemplate(ctx, tmpl, p.ID, ""); err != nil {
		t.Fatal(err)
	}
	p.DefaultFlow = "knobs"
	if _, err := svc.UpdateProject(ctx, p.ID, p); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteFlow(ctx, p.ID, tmpl); err != nil {
		t.Fatalf("reset to template: %v", err)
	}
	if m, err := svc.Binding(ctx, p.ID, tmpl); err != nil || m["plan_gate"] != "false" {
		t.Errorf("binding lost on reset to template: %v, %v", m, err)
	}

	// Deleting the project's own flow takes its binding along.
	p.DefaultFlow = tmpl
	if _, err := svc.UpdateProject(ctx, p.ID, p); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteFlow(ctx, p.ID, "knobs"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetFlowBinding(ctx, p.ID, "knobs"); !errors.Is(err, ErrNotFound) {
		t.Errorf("binding outlived its flow: %v", err)
	}
	if list, err := svc.ListFlowBindings(ctx, p.ID); err != nil || len(list) != 1 || list[0].FlowName != tmpl {
		t.Errorf("list after delete = %+v, %v", list, err)
	}

	// And the project takes the rest.
	if err := svc.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if list, err := svc.ListFlowBindings(ctx, p.ID); err != nil || len(list) != 0 {
		t.Errorf("bindings outlived their project: %+v, %v", list, err)
	}
}
