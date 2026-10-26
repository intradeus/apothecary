package flow

import (
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"apothecary/internal/agentspec"
)

// parseWith renders specs as the document's agents block and parses it.
// Contracts are parse-time facts, so a violated one comes back as the error
// rather than a second pass's finding.
func parseWith(stages string, specs map[string]*agentspec.Spec) (*Flow, error) {
	doc := header
	if len(specs) > 0 {
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
				sp.Prompt = "do the " + n + " work"
			}
			list = append(list, sp)
		}
		y, err := yaml.Marshal(map[string]any{"agents": list})
		if err != nil {
			return nil, err
		}
		doc += string(y)
	}
	return Parse("t.yaml", []byte(doc+"main:\n"+stages))
}

func specMap(specs ...*agentspec.Spec) map[string]*agentspec.Spec {
	m := map[string]*agentspec.Spec{}
	for _, s := range specs {
		m[s.Name] = s
	}
	return m
}

func TestValidateContracts(t *testing.T) {
	file := agentspec.TypeFile
	builderPlan := &agentspec.Spec{Name: "builder", Inputs: []agentspec.InputDecl{{Name: "plan", Type: file}}}
	builderTwo := &agentspec.Spec{Name: "builder", Inputs: []agentspec.InputDecl{
		{Name: "plan", Type: file}, {Name: "changes", Type: agentspec.TypeChangeset}}}
	builderOpt := &agentspec.Spec{Name: "builder", Inputs: []agentspec.InputDecl{
		{Name: "plan", Type: file},
		{Name: "rejection", Type: agentspec.TypeVerdict, Optional: true},
		{Name: "failure", Type: agentspec.TypeFailure, Optional: true},
		{Name: "notes", Type: file, Optional: true}}}
	plannerOut := &agentspec.Spec{Name: "planner", Outputs: []agentspec.OutputDecl{{Name: "plan", Type: file}}}
	noDecls := &agentspec.Spec{Name: "builder"}
	twoOut := &agentspec.Spec{Name: "red", Outputs: []agentspec.OutputDecl{{Name: "a", Type: file}, {Name: "b", Type: file}}}
	reviewerV := &agentspec.Spec{Name: "reviewer",
		Inputs:  []agentspec.InputDecl{{Name: "plan", Type: file}},
		Outputs: []agentspec.OutputDecl{{Name: "verdict", Type: agentspec.TypeVerdict}}}
	reviewerNotes := &agentspec.Spec{Name: "reviewer", Outputs: []agentspec.OutputDecl{{Name: "notes", Type: file}}}

	plan := "  - { id: plan, type: agent, agent: planner, output: { plan: { to: artifacts/plan.md, type: file } } }\n"
	tests := []struct {
		name       string
		stages     string
		specs      map[string]*agentspec.Spec
		wantErr    string // substring; "" = expect none
		noWarnings bool   // assert zero warnings
	}{
		{
			name:    "required input not stated",
			stages:  "  - { id: b, type: agent, agent: builder, input: { changes: { from: run.git.diff_target, type: changeset } }, on_success: done }\n",
			specs:   specMap(builderTwo),
			wantErr: `agent "builder" requires input "plan"`,
		},
		{
			name:    "no input at all still misses the required one",
			stages:  "  - { id: b, type: agent, agent: builder, on_success: done }\n",
			specs:   specMap(builderPlan),
			wantErr: `requires input "plan"`,
		},
		{
			// An agent with no contract cannot answer for a named port.
			name:    "a port against a contract-less agent",
			stages:  "  - { id: b, type: agent, agent: builder, input: { x: { from: repo:a.md, type: file } }, on_success: done }\n",
			specs:   specMap(noDecls),
			wantErr: `input "x" is not an input of agent "builder" (inputs: none)`,
		},
		{
			name:       "optional inputs need no port",
			stages:     plan + "  - { id: b, type: agent, agent: builder, input: { plan: { from: artifacts/plan.md, type: file } }, on_success: done }\n",
			specs:      specMap(builderOpt, plannerOut),
			noWarnings: true,
		},
		{
			name:    "unknown output",
			stages:  "  - { id: r, type: agent, agent: red, output: { a: { to: artifacts/a.md, type: file }, b: { to: artifacts/b.md, type: file }, ghost: { to: artifacts/x.md, type: file } }, on_success: done }\n",
			specs:   specMap(twoOut),
			wantErr: `output "ghost" is not an output of agent "red" (outputs: a, b)`,
		},
		{
			// Two outputs of one stage to one file: the agent is told to
			// write both, and whichever it writes last is all there is.
			name:    "one path for two outputs",
			stages:  "  - { id: r, type: agent, agent: red, output: { a: { to: artifacts/same.md, type: file }, b: { to: artifacts/same.md, type: file } }, on_success: done }\n",
			specs:   specMap(twoOut),
			wantErr: `"artifacts/same.md"`,
		},
		{
			name:       "a command's port names are its own",
			stages:     "  - { id: c, type: command, run: x, input: { whatever_i_like: { from: repo:docs/x.md, type: file } }, on_success: done }\n",
			specs:      specMap(),
			noWarnings: true,
		},
		{
			name:    "an agent input nothing writes",
			stages:  "  - { id: b, type: agent, agent: builder, input: { plan: { from: artifacts/ghost.md, type: file } }, on_success: done }\n",
			specs:   specMap(builderPlan),
			wantErr: `input "plan": nothing in this flow writes "artifacts/ghost.md" — a file of the repository is from: repo:artifacts/ghost.md`,
		},
		{
			name:    "a command input nothing writes",
			stages:  "  - { id: c, type: command, run: x, input: { ghost: { from: artifacts/ghost.md, type: file } }, on_success: done }\n",
			specs:   specMap(),
			wantErr: `nothing in this flow writes "artifacts/ghost.md"`,
		},
		{
			name:    "a gate input nothing writes",
			stages:  "  - { id: g, type: human_gate, input: { ghost: { from: artifacts/ghost.md, type: file } }, on_success: done }\n",
			specs:   specMap(),
			wantErr: `nothing in this flow writes "artifacts/ghost.md"`,
		},
		{
			name:    "an action input nothing writes",
			stages:  "  - { id: m, type: action, action: create_mr, input: { body: { from: artifacts/ghost.md, type: file } }, on_success: done }\n",
			specs:   specMap(),
			wantErr: `nothing in this flow writes "artifacts/ghost.md"`,
		},
		{
			// Outside artifacts/ is no exception: a file of the repository
			// says so with repo:, so a typo is never taken for one.
			name:    "a repository path without repo:",
			stages:  "  - { id: c, type: command, run: x, input: { spec: { from: docs/spec.md, type: file } }, on_success: done }\n",
			specs:   specMap(),
			wantErr: "from: repo:docs/spec.md",
		},
		{
			name:       "a repository file",
			stages:     "  - { id: c, type: command, run: x, input: { spec: { from: repo:docs/spec.md, type: file } }, on_success: done }\n",
			specs:      specMap(),
			noWarnings: true,
		},
		{
			// A command's output feeds a downstream reader.
			name: "a command writes for a reader",
			stages: "  - { id: gen, type: command, run: g, output: { data: { to: artifacts/data.json, type: file } }, on_success: c }\n" +
				"  - { id: c,   type: command, run: x, input: { data: { from: artifacts/data.json, type: file } }, on_success: done }\n",
			specs:      specMap(),
			noWarnings: true,
		},
		{
			// The agent is in the document, so "this edge can never fire" is
			// decidable at parse time — an error, not a suspicion.
			name:    "on_reject without a verdict port is refused",
			stages:  "  - { id: r, type: agent, agent: reviewer, output: { notes: { to: artifacts/notes.md, type: file } }, on_reject: { to: r, max: 1 }, on_success: done }\n",
			specs:   specMap(reviewerNotes),
			wantErr: "on_reject can never fire",
		},
		{
			// What fires on_reject is the stage's port: an agent that writes
			// a file may be run by a stage that reads it as a verdict.
			name:       "a stage's verdict port makes on_reject reachable",
			stages:     "  - { id: r, type: agent, agent: reviewer, output: { notes: { to: artifacts/notes.json, type: verdict } }, on_reject: { to: r, max: 1 }, on_success: done }\n",
			specs:      specMap(reviewerNotes),
			noWarnings: true,
		},
		{
			// Nothing can ever fill it: no failing edge enters this stage.
			name:    "edge.failure with no edge to fill it",
			stages:  plan + "  - { id: b, type: agent, agent: builder, input: { plan: { from: artifacts/plan.md, type: file }, failure: { from: edge.failure, type: failure, optional: true } }, on_success: done }\n",
			specs:   specMap(builderOpt, plannerOut),
			wantErr: `no on_fail or on_timeout edge enters stage "b", so edge.failure is never filled`,
		},
		{
			name: "clean: writer, readers and a reachable rejection",
			stages: plan +
				"  - { id: build,  type: agent, agent: builder, input: { plan: { from: artifacts/plan.md, type: file }, rejection: { from: edge.verdict, type: verdict, optional: true } }, on_success: review }\n" +
				"  - { id: review, type: agent, agent: reviewer, input: { plan: { from: artifacts/plan.md, type: file } },\n" +
				"      output: { verdict: { to: artifacts/verdict.json, type: verdict } }, on_success: done,\n" +
				"      on_reject: { to: build, max: 2 } }\n",
			specs:      specMap(plannerOut, builderOpt, reviewerV),
			noWarnings: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseWith(tc.stages, tc.specs)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parsed cleanly, want error mentioning %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want mention of %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.noWarnings && len(f.Warnings) != 0 {
				t.Errorf("unexpected warnings: %s", strings.Join(f.Warnings, " | "))
			}
		})
	}
}

// TestUndeclaredAgentRefused: a stage naming an agent the flow does not
// declare is a parse error — the document is the whole contract, so there is
// nothing left to defer to a later, project-aware pass.
func TestUndeclaredAgentRefused(t *testing.T) {
	_, err := parseWith("  - { id: b, type: agent, agent: ghost, input: { x: { from: repo:a.md, type: file } }, on_success: done }\n", nil)
	if err == nil || !strings.Contains(err.Error(), `agent "ghost" is not declared in this flow`) {
		t.Errorf("err = %v, want an undeclared-agent refusal", err)
	}
}

// TestTypedPath: one resolution for prompt assembly, the post-run read and
// crash recovery — the path is the stage's port, wherever it puts it.
func TestTypedPath(t *testing.T) {
	st := &Stage{Outputs: []Port{
		{Name: "notes", Value: "artifacts/notes.md", Type: agentspec.TypeFile},
		{Name: "verdict", Value: "artifacts/review/v.json", Type: agentspec.TypeVerdict},
	}}
	if got := TypedPath(st, agentspec.TypeVerdict); got != "artifacts/review/v.json" {
		t.Errorf("verdict = %q", got)
	}
	if got := TypedPath(st, agentspec.TypeReviewReplies); got != "" {
		t.Errorf("no replies port = %q, want empty", got)
	}
	if got := TypedPath(&Stage{}, agentspec.TypeVerdict); got != "" {
		t.Errorf("no outputs = %q, want empty", got)
	}
}
