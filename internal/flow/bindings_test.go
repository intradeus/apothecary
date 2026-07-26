package flow

import (
	"strings"
	"testing"

	"apothecary/internal/agentspec"
)

func mustFlow(t *testing.T, stages string) *Flow {
	t.Helper()
	f, err := Parse("t.yaml", []byte(header+"stages:\n"+stages))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return f
}

func specMap(specs ...*agentspec.Spec) map[string]*agentspec.Spec {
	m := map[string]*agentspec.Spec{}
	for _, s := range specs {
		m[s.Name] = s
	}
	return m
}

func TestValidateBindings(t *testing.T) {
	builderPlan := &agentspec.Spec{Name: "builder", Inputs: []agentspec.InputDecl{{Name: "plan"}}}
	builderTwo := &agentspec.Spec{Name: "builder", Inputs: []agentspec.InputDecl{{Name: "plan"}, {Name: "changes"}}}
	builderOpt := &agentspec.Spec{Name: "builder", Inputs: []agentspec.InputDecl{
		{Name: "plan"}, {Name: "rejection", Optional: true}, {Name: "notes", Optional: true}}}
	// A spec that declares its rejection slot REQUIRED — which cannot work,
	// since nothing fills it on the first attempt.
	builderFeed := &agentspec.Spec{Name: "builder", Inputs: []agentspec.InputDecl{{Name: "rejection"}}}
	plannerOut := &agentspec.Spec{Name: "planner", Outputs: []agentspec.OutputDecl{{Name: "plan", Path: "artifacts/plan.md"}}}
	noDecls := &agentspec.Spec{Name: "builder"}
	twoOut := &agentspec.Spec{Name: "red", Outputs: []agentspec.OutputDecl{{Name: "a", Path: "artifacts/a.md"}, {Name: "b", Path: "artifacts/b.md"}}}
	reviewerV := &agentspec.Spec{Name: "reviewer",
		Inputs:  []agentspec.InputDecl{{Name: "plan"}},
		Outputs: []agentspec.OutputDecl{{Name: "verdict", Type: agentspec.OutputTypeVerdict, Path: "artifacts/verdict.json"}}}
	reviewerNoV := &agentspec.Spec{Name: "reviewer", Inputs: []agentspec.InputDecl{{Name: "plan"}}}

	tests := []struct {
		name       string
		stages     string
		specs      map[string]*agentspec.Spec
		wantErr    string // substring; "" = expect none
		wantWarn   string // substring
		noWarnings bool   // assert zero warnings
	}{
		{
			name:    "unknown input key",
			stages:  "  - { id: b, type: agent, agent: builder, input: { ghost: a.md }, on_success: done }\n",
			specs:   specMap(builderPlan),
			wantErr: "not declared by agent",
		},
		{
			name:    "required input unbound",
			stages:  "  - { id: b, type: agent, agent: builder, input: { changes: $CHANGED }, on_success: done }\n",
			specs:   specMap(builderTwo),
			wantErr: `required input "plan"`,
		},
		{
			name:    "no input at all still misses the required one",
			stages:  "  - { id: b, type: agent, agent: builder, on_success: done }\n",
			specs:   specMap(builderPlan),
			wantErr: `required input "plan"`,
		},
		{
			// An agent with no contract cannot answer for a named binding.
			name:    "binding against a contract-less agent",
			stages:  "  - { id: b, type: agent, agent: builder, input: { x: a.md }, on_success: done }\n",
			specs:   specMap(noDecls),
			wantErr: "not declared by agent",
		},
		{
			// optional inputs may be skipped.
			name:       "optional inputs need no binding",
			stages:     "  - { id: b, type: agent, agent: builder, input: { plan: artifacts/plan.md }, on_success: done }\n",
			specs:      specMap(builderOpt, plannerOut),
			noWarnings: false, // the plan path has no producer in this one-stage flow
		},
		{
			name:    "unknown output key",
			stages:  "  - { id: r, type: agent, agent: red, output: { ghost: artifacts/x.md }, on_success: done }\n",
			specs:   specMap(twoOut),
			wantErr: "not declared by agent",
		},
		{
			name:    "output path bound twice",
			stages:  "  - { id: r, type: agent, agent: red, output: { a: artifacts/same.md, b: artifacts/same.md }, on_success: done }\n",
			specs:   specMap(twoOut),
			wantErr: "bound more than once",
		},
		{
			name:       "a command's free binding names are not checked",
			stages:     "  - { id: c, type: command, run: x, input: { whatever_i_like: docs/x.md }, on_success: done }\n",
			specs:      specMap(),
			noWarnings: true,
		},
		{
			name:     "rendezvous: unproduced agent input warns",
			stages:   "  - { id: b, type: agent, agent: builder, input: { plan: artifacts/ghost.md }, on_success: done }\n",
			specs:    specMap(builderPlan),
			wantWarn: "no stage produces",
		},
		{
			name:     "rendezvous: unproduced command input warns",
			stages:   "  - { id: c, type: command, run: x, input: { ghost: artifacts/ghost.md }, on_success: done }\n",
			specs:    specMap(),
			wantWarn: "binds \"artifacts/ghost.md\"",
		},
		{
			name:     "rendezvous: unproduced gate input warns",
			stages:   "  - { id: g, type: human_gate, input: { ghost: artifacts/ghost.md }, on_success: done }\n",
			specs:    specMap(),
			wantWarn: "binds \"artifacts/ghost.md\"",
		},
		{
			name:     "rendezvous: unproduced action input warns",
			stages:   "  - { id: m, type: action, action: create_mr, input: { body: artifacts/ghost.md }, on_success: done }\n",
			specs:    specMap(),
			wantWarn: "no stage produces",
		},
		{
			// A repo file the flow never produces is legitimate input.
			name:       "rendezvous ignores non-artifact paths",
			stages:     "  - { id: c, type: command, run: x, input: { spec: docs/spec.md }, on_success: done }\n",
			specs:      specMap(),
			noWarnings: true,
		},
		{
			// A command's declared output satisfies a downstream consumer.
			name: "command output produces for a consumer",
			stages: "  - { id: gen, type: command, run: g, output: { data: artifacts/data.json }, on_success: c }\n" +
				"  - { id: c,   type: command, run: x, input: { data: artifacts/data.json }, on_success: done }\n",
			specs:      specMap(),
			noWarnings: true,
		},
		{
			name:     "on_reject without a verdict output warns",
			stages:   "  - { id: r, type: agent, agent: reviewer, input: { plan: artifacts/plan.md }, on_reject: { to: r, max: 1 }, on_success: done }\n",
			specs:    specMap(reviewerNoV),
			wantWarn: "on_reject will never fire",
		},
		{
			// $DECISION is empty on the first attempt, so its decl must be optional.
			name:    "feed sentinel bound to a required input",
			stages:  "  - { id: b, type: agent, agent: builder, input: { rejection: $DECISION }, on_success: done }\n",
			specs:   specMap(builderFeed),
			wantErr: "empty on the first attempt",
		},
		{
			// Nothing can ever fill it: no rejecting edge targets this stage.
			name:     "feed sentinel with no edge to fill it",
			stages:   "  - { id: b, type: agent, agent: builder, input: { plan: artifacts/plan.md, rejection: $DECISION }, on_success: done }\n",
			specs:    specMap(builderOpt, plannerOut),
			wantWarn: "nothing will ever fill it",
		},
		{
			name: "clean: producer, consumer and a reachable rejection",
			stages: "  - { id: plan,   type: agent, agent: planner, on_success: build }\n" +
				"  - { id: build,  type: agent, agent: builder, input: { plan: artifacts/plan.md, rejection: $DECISION }, on_success: review }\n" +
				"  - { id: review, type: agent, agent: reviewer, input: { plan: artifacts/plan.md }, on_success: done,\n" +
				"      on_reject: { to: build, max: 2 } }\n",
			specs:      specMap(plannerOut, builderOpt, reviewerV),
			noWarnings: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := mustFlow(t, tc.stages)
			errs, warns := ValidateBindings(f, tc.specs)
			joinE := joinErrs(errs)
			if tc.wantErr == "" {
				if len(errs) != 0 {
					t.Errorf("unexpected errors: %s", joinE)
				}
			} else if !strings.Contains(joinE, tc.wantErr) {
				t.Errorf("errors = %q, want mention of %q", joinE, tc.wantErr)
			}
			joinW := strings.Join(warns, " | ")
			if tc.noWarnings && len(warns) != 0 {
				t.Errorf("unexpected warnings: %s", joinW)
			}
			if tc.wantWarn != "" && !strings.Contains(joinW, tc.wantWarn) {
				t.Errorf("warnings = %q, want mention of %q", joinW, tc.wantWarn)
			}
		})
	}
}

// TestValidateBindingsUnknownAgentSkipped: an agent the caller's view does not
// know is preflight's error, not this pass's — it must not guess.
func TestValidateBindingsUnknownAgentSkipped(t *testing.T) {
	f := mustFlow(t, "  - { id: b, type: agent, agent: ghost, input: { x: artifacts/a.md }, on_success: done }\n")
	errs, _ := ValidateBindings(f, specMap())
	if len(errs) != 0 {
		t.Errorf("errors = %s, want none for an unknown agent", joinErrs(errs))
	}
}

func joinErrs(errs []*Error) string {
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Error()
	}
	return strings.Join(parts, " | ")
}

func TestEffectiveOutputs(t *testing.T) {
	oneOut := &agentspec.Spec{Name: "planner", Outputs: []agentspec.OutputDecl{{Name: "plan", Path: "artifacts/plan.md"}}}
	twoOut := &agentspec.Spec{Name: "red", Outputs: []agentspec.OutputDecl{{Name: "a", Path: "a.md"}, {Name: "b", Path: "b.md"}}}

	t.Run("nil spec is literal", func(t *testing.T) {
		st := &Stage{Outputs: []IOBinding{{Value: "x.md"}}}
		got := EffectiveOutputs(st, nil)
		if len(got) != 1 || got[0].Value != "x.md" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("decl default when no stage output", func(t *testing.T) {
		got := EffectiveOutputs(&Stage{}, oneOut)
		if len(got) != 1 || got[0].Name != "plan" || got[0].Value != "artifacts/plan.md" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("map rebind overrides one default", func(t *testing.T) {
		st := &Stage{Outputs: []IOBinding{{Name: "b", Value: "custom/b.md"}}}
		got := EffectiveOutputs(st, twoOut)
		if len(got) != 2 || got[0].Value != "a.md" || got[1].Value != "custom/b.md" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("binding matching no decl still gets enforced", func(t *testing.T) {
		// Degenerate (ValidateBindings errors on it), but the engine must not
		// silently drop a file the document demanded.
		st := &Stage{Outputs: []IOBinding{{Name: "ghost", Value: "extra.md"}}}
		got := EffectiveOutputs(st, twoOut)
		if len(got) != 3 || got[2].Value != "extra.md" {
			t.Errorf("got %+v", got)
		}
	})
}

// TestVerdictPath: one resolution for prompt assembly, the post-run read and
// crash recovery — including when the stage rebinds the path.
func TestVerdictPath(t *testing.T) {
	spec := &agentspec.Spec{Name: "reviewer", Outputs: []agentspec.OutputDecl{
		{Name: "notes", Path: "artifacts/notes.md"},
		{Name: "verdict", Type: agentspec.OutputTypeVerdict, Path: "artifacts/verdict.json"},
	}}
	if got := VerdictPath(&Stage{}, spec); got != "artifacts/verdict.json" {
		t.Errorf("default = %q", got)
	}
	st := &Stage{Outputs: []IOBinding{{Name: "verdict", Value: "artifacts/review/v.json"}}}
	if got := VerdictPath(st, spec); got != "artifacts/review/v.json" {
		t.Errorf("rebound = %q", got)
	}
	if got := VerdictPath(&Stage{}, &agentspec.Spec{Name: "builder"}); got != "" {
		t.Errorf("no verdict output = %q, want empty", got)
	}
	if got := VerdictPath(&Stage{}, nil); got != "" {
		t.Errorf("nil spec = %q, want empty", got)
	}
}
