package builtin

import (
	"context"
	"errors"
	"testing"

	"apothecary/internal/flow"
)

// TestDefaultsParse locks the compiled-in defaults: the binary must never
// ship a default that fails to parse (load panics on the first accessor).
func TestDefaultsParse(t *testing.T) {
	if Flow(DefaultFlowName) == nil {
		t.Fatalf("no built-in flow named %q", DefaultFlowName)
	}
	if len(Agents()) == 0 {
		t.Fatal("no built-in agents")
	}
}

// TestDefaultFlowPhilosophy: every agent referenced by the default flow is
// a built-in agent (a fresh install must resolve the whole flow), every
// agent/command stage declares on_fail, every on_fail chain ends at a
// manual_debug rescue gate, and that gate resumes the stage that escalated
// to it — the success chain replays from the failure point, never skipping
// stages.
func TestDefaultFlowPhilosophy(t *testing.T) {
	f := Flow(DefaultFlowName)
	for i := range f.Stages {
		s := &f.Stages[i]
		if s.Type == flow.StageAgent && Agent(s.Agent) == nil {
			t.Errorf("stage %s references agent %q, which is not built in", s.ID, s.Agent)
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
		if tgt == nil || tgt.Ping != flow.PingManualDebug {
			t.Errorf("stage %s: on_fail chain ends at %q, want a manual_debug gate", s.ID, e.To)
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
	shadow, err := flow.Parse("default", []byte("name: default\ndefaults: { harness: fake, model: a/b }\nstages:\n  - { id: s, type: command, run: \"true\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	db[DefaultFlowName] = shadow
	f, err = src.Get(context.Background(), DefaultFlowName)
	if err != nil || len(f.Stages) != 1 {
		t.Fatalf("shadowed Get(default) = %v, %v — the DB must win", f, err)
	}
}
