package builtin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"apothecary/internal/actionspec"
	"apothecary/internal/flow"
)

// TestDefaultFlowBindingsClean is the wiring invariant this feature exists
// for: the default flow's stage bindings satisfy its agents' declared I/O
// contract with zero warnings. Contract VIOLATIONS are parse errors since v3
// (the agents are in the document), so reaching this test at all proves the
// errors are clean; what is left to assert is that nothing merely suspicious
// survives either.
func TestDefaultFlowBindingsClean(t *testing.T) {
	for _, w := range Flow(DefaultFlowName).Warnings {
		t.Errorf("binding warning: %s", w)
	}
}

// TestShippedFlowsBindCleanly: the flows this repo ships as references parse
// and bind their own agents' contracts with zero warnings. They are what a
// human copies to start from, so a stale one teaches the wrong grammar.
// (The built-in default is covered above; these are the two on disk.)
func TestShippedFlowsBindCleanly(t *testing.T) {
	for _, path := range []string{"../../artifacts/flows/v3.yaml", "../flow/testdata/v3.yaml"} {
		t.Run(path, func(t *testing.T) {
			f, err := flow.Load(path)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			for _, w := range f.Warnings {
				t.Errorf("binding warning: %s", w)
			}
		})
	}
}

// TestBuiltinPromptsBindingNeutral guards the rewording: a prompt must not name
// a header the engine no longer renders, nor an artifact path the flow is free
// to rebind. What a stage is given is described in its INPUT sections.
func TestBuiltinPromptsBindingNeutral(t *testing.T) {
	for _, sp := range Flow(DefaultFlowName).Agents {
		for _, stale := range []string{"PREVIOUS ATTEMPT FEEDBACK", "# ARTIFACT:", "artifacts/plan.md", "artifacts/verdict.json"} {
			if strings.Contains(sp.Prompt, stale) {
				t.Errorf("agent %q prompt names %q, which the flow controls — describe the input instead", sp.Name, stale)
			}
		}
	}
}

// TestDefaultsParse locks the compiled-in defaults: the binary must never
// ship a default that fails to parse (load panics on the first accessor).
func TestDefaultsParse(t *testing.T) {
	f := Flow(DefaultFlowName)
	if f == nil {
		t.Fatalf("no built-in flow named %q", DefaultFlowName)
	}
	if len(f.Agents) == 0 {
		t.Fatal("the default flow declares no agents")
	}
}

// TestDefaultFlowPhilosophy: every agent referenced by the default flow is
// a built-in agent (a fresh install must resolve the whole flow), every
// agent/command stage declares on_fail, every on_fail chain ends at a
// workspace rescue gate, and that gate resumes the stage that escalated
// to it — the success chain replays from the failure point, never skipping
// stages.
func TestDefaultFlowPhilosophy(t *testing.T) {
	f := Flow(DefaultFlowName)
	for i := range f.Stages {
		s := &f.Stages[i]
		if s.Type == flow.StageAgent && f.Agent(s.Agent) == nil {
			t.Errorf("stage %s references agent %q, which the flow does not declare", s.ID, s.Agent)
		}
		if s.Type != flow.StageAgent && s.Type != flow.StageCommand {
			continue
		}
		e := s.Edge(flow.EdgeFail)
		if e == nil {
			t.Errorf("stage %s: no on_fail", s.ID)
			continue
		}
		for e.Then != nil {
			e = e.Then
		}
		tgt := f.Stage(e.To)
		// A rescue gate is one that hands the human the workspace: the kind of
		// gate is now what it shows, not an enum.
		if tgt == nil || tgt.Type != flow.StageHumanGate || !showsWorkspace(tgt) {
			t.Errorf("stage %s: on_fail chain ends at %q, want a gate showing %s", s.ID, e.To, flow.SentinelWorkspace)
			continue
		}
		if tgt.OnSuccess != s.ID {
			t.Errorf("stage %s: rescue gate %s resumes %q, must resume the escalating stage %q",
				s.ID, tgt.ID, tgt.OnSuccess, s.ID)
		}
	}
	if f.Stage("final_gate").Edge(flow.EdgeReject).To != flow.TerminalAbort {
		t.Error("final_gate.on_reject must be abort")
	}
	// Every human_gate says what it is asking: a gate with no question would
	// park the run on an unexplained decision.
	for i := range f.Stages {
		if s := &f.Stages[i]; s.Type == flow.StageHumanGate && s.Question == "" {
			t.Errorf("gate %s has no question", s.ID)
		}
	}
	// The MR is an action, and the reviewer's verdict is what makes the review
	// stage's on_reject reachable.
	if mr := f.Stage("mr"); mr.Type != flow.StageAction || mr.Action != actionspec.CreateMR {
		t.Errorf("mr stage = %s/%s, want action/%s", mr.Type, mr.Action, actionspec.CreateMR)
	}
	if f.Agent("reviewer").VerdictOutput() == nil {
		t.Error("the reviewer must declare a verdict output, or review.on_reject can never fire")
	}
}

// showsWorkspace reports whether a gate hands the human the workspace links.
func showsWorkspace(st *flow.Stage) bool {
	for _, in := range st.Inputs {
		if in.Value == flow.SentinelWorkspace {
			return true
		}
	}
	return false
}

// TestDefaultFlowRetryChannelDeclared: every stage an on_fail or on_reject
// edge loops back to must bind the matching sentinel, or the retry would run
// blind — the agent would repeat itself with no idea why it was sent back.
func TestDefaultFlowRetryChannelDeclared(t *testing.T) {
	f := Flow(DefaultFlowName)
	for i := range f.Stages {
		for kind, e := range f.Stages[i].Edges {
			for link := e; link != nil; link = link.Then {
				tgt := f.Stage(link.To)
				if tgt == nil || tgt.Type != flow.StageAgent {
					continue // gates and commands cannot read feedback
				}
				want := flow.FeedSentinel(kind)
				bound := false
				for _, in := range tgt.Inputs {
					if in.Value == want {
						bound = true
					}
				}
				if !bound {
					t.Errorf("stage %s: %s loops back to agent stage %s, which binds no %s",
						f.Stages[i].ID, kind, tgt.ID, want)
				}
			}
		}
	}
}

func TestFlowSourceLayering(t *testing.T) {
	db := flow.MapSource{}
	src := FlowSource(db)

	// Empty DB: the built-in resolves.
	f, err := src.Get(context.Background(), DefaultFlowName)
	if err != nil || f.Name != DefaultFlowName {
		t.Fatalf("Get(default) = %v, %v", f, err)
	}
	// Unknown names still miss.
	if _, err := src.Get(context.Background(), "nope"); !errors.Is(err, flow.ErrNotFound) {
		t.Fatalf("Get(nope) err = %v, want ErrNotFound", err)
	}
	// A DB row with the same name shadows the built-in.
	shadow, err := flow.Parse("default", []byte("name: default\nversion: 3\ndefaults: { harness: fake, model: a/b }\nstages:\n  - { id: s, type: command, run: \"true\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	db[DefaultFlowName] = shadow
	f, err = src.Get(context.Background(), DefaultFlowName)
	if err != nil || len(f.Stages) != 1 {
		t.Fatalf("shadowed Get(default) = %v, %v — the DB must win", f, err)
	}
}
