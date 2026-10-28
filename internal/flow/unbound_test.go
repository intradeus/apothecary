package flow

import (
	"strings"
	"testing"
)

const unboundHead = `name: t
version: 5
agents:
  - name: b
    prompt: do b
`

// A flow need not say which model (or harness) its stages run on: the
// parameter exists, unbound, and the project or the run gives it a value.
// The document parses and says what is missing; the bound document says
// nothing is.
func TestUnboundModelParsesAndIsReported(t *testing.T) {
	doc := unboundHead + `parameters:
  harness: fake
main:
  - { id: build, type: agent, agent: b, harness: $harness, model: $model, on_success: done }
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if st := f.Stage("build"); st.Model != "" || st.Harness != "fake" {
		t.Errorf("stage = %+v, want an empty model and the literal harness", st)
	}
	if pr := f.Param("model"); !pr.Required || !pr.Known || pr.Role != RoleModel || pr.Harness != "$harness" {
		t.Errorf("model param = %+v", pr)
	}
	if pr := f.Param("harness"); pr.Role != RoleHarness || pr.Required {
		t.Errorf("harness param = %+v", pr)
	}
	ub := f.Unbound()
	if len(ub) != 1 || ub[0].Param != "model" || strings.Join(ub[0].Sites, ",") != "build" {
		t.Fatalf("unbound = %+v", ub)
	}
	if err := UnboundError(ub); err == nil || !strings.Contains(err.Error(), `"model" has no value (read by build)`) || !strings.Contains(err.Error(), "Configure") {
		t.Errorf("message = %v", err)
	}
	bound, err := f.Resolve(map[string]string{"model": "acme/alpha-1"})
	if err != nil {
		t.Fatal(err)
	}
	if st := bound.Stage("build"); st.Model != "acme/alpha-1" {
		t.Errorf("bound stage model = %q", st.Model)
	}
	if ub := bound.Unbound(); len(ub) != 0 {
		t.Errorf("bound flow still unbound: %+v", ub)
	}
	// A bad value is still refused where it is bound.
	if _, err := f.Resolve(map[string]string{"model": "nomodel"}); err == nil || !strings.Contains(err.Error(), "provider/model") {
		t.Errorf("bad bound model: err = %v", err)
	}
	// The Ask pane's harness waits the same way.
	ask, err := Parse("t.yaml", []byte(unboundHead+"ask: { harness: $harness }\nparameters: { model: a/b }\nmain:\n  - { id: build, type: agent, agent: b, harness: $harness, model: $model, on_success: done }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if ub := ask.Unbound(); len(ub) != 1 || ub[0].Param != "harness" || strings.Join(ub[0].Sites, ",") != "ask,build" {
		t.Errorf("unbound = %+v", ub)
	}
}

// Only a model or a harness may wait for a value: anywhere else a parameter
// is read it needs one now.
func TestUnboundElsewhereIsRefused(t *testing.T) {
	doc := unboundHead + `parameters:
  harness: fake
  model: a/b
  verify: { type: string }
main:
  - { id: test, type: command, run_ref: $verify, on_success: done }
`
	if _, err := Parse("t.yaml", []byte(doc)); err == nil || !strings.Contains(err.Error(), `parameter "verify" has no value`) {
		t.Errorf("err = %v", err)
	}
}

// Every agent stage says what it runs on; the flow's parameters are what it
// says it with, never a silent fallback.
func TestAgentStageStatesHarnessAndModel(t *testing.T) {
	for _, tc := range []struct{ name, stage, want string }{
		{"no harness", "{ id: build, type: agent, agent: b, model: $model }", `stage "build": harness is required`},
		{"no model", "{ id: build, type: agent, agent: b, harness: $harness }", `stage "build": model is required`},
		{"a harness that is not one", "{ id: build, type: agent, agent: b, harness: Not-A-Kind, model: $model }", "is not a slug"},
	} {
		doc := unboundHead + "parameters: { harness: fake, model: a/b }\nmain:\n  - " + tc.stage + "\n"
		if _, err := Parse("t.yaml", []byte(doc)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

// Stages sharing a model parameter share a harness — as written: the same
// kind, or the same parameter.
func TestSharedModelParameterSharesAHarness(t *testing.T) {
	two := func(h1, h2 string) string {
		return unboundHead + "parameters: { harness: fake, model: a/b, other: { type: string, default: fake } }\nmain:\n" +
			"  - { id: build, type: agent, agent: b, harness: " + h1 + ", model: $model }\n" +
			"  - { id: review, type: agent, agent: b, harness: " + h2 + ", model: $model, on_success: done }\n"
	}
	for _, tc := range []struct {
		name   string
		h1, h2 string
		ok     bool
	}{
		{"same parameter", "$harness", "$harness", true},
		{"same kind", "fake", "fake", true},
		{"two kinds", "fake", "codex", false},
		{"a kind and a parameter", "fake", "$harness", false},
		{"two parameters", "$harness", "$other", false},
	} {
		f, err := Parse("t.yaml", []byte(two(tc.h1, tc.h2)))
		if tc.ok {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			} else if f.Param("model").Harness != tc.h1 {
				t.Errorf("%s: model's harness = %q, want %q", tc.name, f.Param("model").Harness, tc.h1)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "must share a harness") || !strings.Contains(err.Error(), `both read $model`) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
	// One parameter, one role (model unbound, so neither format check speaks first).
	doc := unboundHead + "parameters: { harness: fake }\nmain:\n  - { id: build, type: agent, agent: b, harness: $model, model: $model, on_success: done }\n"
	if _, err := Parse("t.yaml", []byte(doc)); err == nil || !strings.Contains(err.Error(), "one parameter, one role") {
		t.Errorf("role conflict: err = %v", err)
	}
}

// A default may read another parameter: the value follows that one's — bound
// or not — unless this one is bound itself.
func TestDefaultReadsAnotherParameter(t *testing.T) {
	doc := unboundHead + `parameters:
  harness: fake
  review_model: { type: string, default: $model, description: "the reviewer's model" }
main:
  - { id: build, type: agent, agent: b, harness: $harness, model: $model }
  - { id: review, type: agent, agent: b, harness: $harness, model: $review_model, on_success: done }
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if pr := f.Param("review_model"); pr.DefaultRef != "model" || pr.Default != "" || pr.Required || pr.Role != RoleModel || pr.Harness != "$harness" {
		t.Errorf("review_model = %+v", pr)
	}
	// Unbound through the chain: both stages wait on model — the parameter
	// at the end of the chain, not the default that reads it.
	if ub := f.Unbound(); len(ub) != 1 || ub[0].Param != "model" || strings.Join(ub[0].Sites, ",") != "build,review" {
		t.Errorf("unbound = %+v", ub)
	}
	bound, err := f.Resolve(map[string]string{"model": "acme/alpha-1"})
	if err != nil {
		t.Fatal(err)
	}
	if bound.Stage("review").Model != "acme/alpha-1" || len(bound.Unbound()) != 0 {
		t.Errorf("review follows model: %q, unbound %+v", bound.Stage("review").Model, bound.Unbound())
	}
	bound, err = f.Resolve(map[string]string{"model": "acme/alpha-1", "review_model": "acme/alpha-2"})
	if err != nil {
		t.Fatal(err)
	}
	if bound.Stage("review").Model != "acme/alpha-2" || bound.Stage("build").Model != "acme/alpha-1" {
		t.Errorf("own binding wins: review %q build %q", bound.Stage("review").Model, bound.Stage("build").Model)
	}
	for _, w := range f.Warnings {
		if strings.Contains(w, `parameter "model"`) {
			t.Errorf("model is read through review_model's default: %q", w)
		}
	}
	for _, tc := range []struct{ name, params, want string }{
		{"no such parameter", "{ harness: fake, model: a/b, x: { type: string, default: $nope } }", "default $nope names no parameter"},
		{"another type", "{ harness: fake, model: a/b, x: { type: int, default: $model } }", "default $model is a string, and this parameter is a"},
		{"itself", "{ harness: fake, model: a/b, x: { type: string, default: $x } }", "loops back to itself"},
		{"a cycle", "{ harness: fake, model: a/b, x: { type: string, default: $y }, y: { type: string, default: $x } }", "loops back to itself"},
		{"needs a type", "{ harness: fake, model: a/b, x: { default: $model } }", "x: type is required"},
	} {
		doc := unboundHead + "parameters: " + tc.params + "\nmain:\n  - { id: build, type: agent, agent: b, harness: $harness, model: $model, on_success: done }\n"
		if _, err := Parse("t.yaml", []byte(doc)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}
