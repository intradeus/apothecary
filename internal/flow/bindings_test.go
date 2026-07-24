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
	plannerOut := &agentspec.Spec{Name: "planner", Outputs: []agentspec.OutputDecl{{Name: "plan", Path: "artifacts/plan.md"}}}
	noDecls := &agentspec.Spec{Name: "builder"}
	twoOut := &agentspec.Spec{Name: "red", Outputs: []agentspec.OutputDecl{{Name: "a", Path: "a.md"}, {Name: "b", Path: "b.md"}}}

	tests := []struct {
		name       string
		stages     string
		specs      map[string]*agentspec.Spec
		wantErr    string // substring; "" = none
		wantWarn   string // substring; "" = none
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
			name:     "map input zero-decl agent warns",
			stages:   "  - { id: b, type: agent, agent: builder, input: { x: a.md }, on_success: done }\n",
			specs:    specMap(noDecls),
			wantWarn: "declares no inputs",
		},
		{
			name:     "legacy list + required decl warns only",
			stages:   "  - { id: b, type: agent, agent: builder, input: [ticket], on_success: done }\n",
			specs:    specMap(builderPlan),
			wantWarn: "no named bindings",
		},
		{
			name:    "legacy single output but >1 decls",
			stages:  "  - { id: r, type: agent, agent: red, output: only.md, on_success: done }\n",
			specs:   specMap(twoOut),
			wantErr: "ambiguous",
		},
		{
			name:     "map output zero-decl warns",
			stages:   "  - { id: b, type: agent, agent: builder, output: { x: a.md }, on_success: done }\n",
			specs:    specMap(noDecls),
			wantWarn: "declares no outputs",
		},
		{
			name:    "unknown output key",
			stages:  "  - { id: r, type: agent, agent: red, output: { ghost: x.md }, on_success: done }\n",
			specs:   specMap(twoOut),
			wantErr: "not declared by agent",
		},
		{
			name:     "rendezvous: unproduced artifact input warns",
			stages:   "  - { id: b, type: agent, agent: builder, input: { plan: artifacts/ghost.md }, on_success: done }\n",
			specs:    specMap(builderPlan),
			wantWarn: "no agent stage produces",
		},
		{
			name: "clean: producer + consumer",
			stages: "  - { id: plan,  type: agent, agent: planner, on_success: build }\n" +
				"  - { id: build, type: agent, agent: builder, input: { plan: artifacts/plan.md }, on_success: done }\n",
			specs:      specMap(plannerOut, builderPlan),
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
		if len(got) != 1 || got[0].Value != "artifacts/plan.md" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("legacy single rebinds sole decl", func(t *testing.T) {
		st := &Stage{Outputs: []IOBinding{{Value: "custom/plan.md"}}}
		got := EffectiveOutputs(st, oneOut)
		if len(got) != 1 || got[0].Name != "plan" || got[0].Value != "custom/plan.md" {
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
	t.Run("legacy single with >1 decls is degenerate but enforced", func(t *testing.T) {
		st := &Stage{Outputs: []IOBinding{{Value: "extra.md"}}}
		got := EffectiveOutputs(st, twoOut)
		// two decl defaults + the standalone legacy path.
		if len(got) != 3 {
			t.Fatalf("got %+v", got)
		}
		if got[2].Value != "extra.md" {
			t.Errorf("legacy path missing: %+v", got)
		}
	})
}
