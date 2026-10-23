package engine

// Stage conditions (FLOWLANG §2 when): a stage whose condition does not hold
// completes as skipped and the chain goes on; a subject action in a phase
// run with no merge request fails routably.

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/event"
	"apothecary/internal/run"
)

func skipped(evs []event.Event, stage string) (bool, string) {
	for _, e := range evs {
		if c, ok := e.Payload.(*event.StageCompleted); ok && c.StageID == stage && c.Skipped {
			return true, c.Note
		}
	}
	return false, ""
}

// when: tracker on a ticket written by hand: the agent never runs and no
// workspace is provisioned for it; the command after it gets the workspace.
func TestWhenTrackerSkipsWithoutASource(t *testing.T) {
	doc := `name: cond
version: 4
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
agents:
  - { name: b, prompt: build it }
stages:
  - { id: note, type: agent, agent: b, when: tracker }
  - { id: test, type: command, run: "true" }
`
	h := newHarness(t, mustFlow(t, doc))
	id, err := h.e.CreateRun(context.Background(), Ticket{Title: "by hand"}, "cond", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	evs := h.events(t, id)
	if ok, note := skipped(evs, "note"); !ok || !strings.Contains(note, "when tracker") {
		t.Fatalf("note not skipped: %v", eventTypes(evs))
	}
	if n := len(h.runner.SnapshotRunCalls()); n != 0 {
		t.Errorf("the skipped agent ran %d times", n)
	}
	types := eventTypes(evs)
	envAt, skipAt := -1, -1
	for i, ty := range types {
		switch ty {
		case "env_requested":
			envAt = i
		case "stage_completed":
			if skipAt < 0 {
				skipAt = i
			}
		}
	}
	if envAt < 0 || skipAt < 0 || envAt < skipAt {
		t.Errorf("the workspace was provisioned before the skip: %v", types)
	}
	if v := h.view(t, id); v.State != run.Completed {
		t.Errorf("state = %s", v.State)
	}
	// With a tracker issue the stage runs.
	id2, err := h.e.CreateRun(context.Background(), Ticket{Title: "imported", SourceID: "int-1", SourceKind: "fake", SourceKey: "DEV-1"}, "cond", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id2)
	if ok, _ := skipped(h.events(t, id2), "note"); ok || len(h.runner.SnapshotRunCalls()) != 1 {
		t.Errorf("with a tracker the agent must run (skipped %v, runs %d)", ok, len(h.runner.SnapshotRunCalls()))
	}
}

// when: subject in a phase started by hand with no merge request skips;
// a subject action without the guard fails routably instead.
func TestWhenSubjectSkipsInASubjectlessPhaseRun(t *testing.T) {
	doc := `name: subj
version: 4
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
phases:
  - id: deliver
    on: run_start
    stages:
      - { id: build, type: command, run: "true" }
  - id: polish
    on: manual
    stages:
      - { id: sync, type: action, action: sync_target, when: subject }
      - { id: load, type: action, action: load_review_comments, on_fail: end }
      - { id: shine, type: command, run: "./shine.sh" }
`
	ph := newPhasedHarness(t, doc)
	runID, err := ph.e.CreateRun(context.Background(), Ticket{Title: "T"}, "subj", testProject)
	if err != nil {
		t.Fatal(err)
	}
	ph.waitState(t, runID, run.Completed) // nothing armed: deliver ends, the run completes
	if err := ph.e.StartPhase(runID, "polish", ""); err == nil {
		t.Fatal("a completed run must not start a phase")
	}
	// Keep the run alive with a firing phase that needs a merge request the
	// flow never opens: a parked wait would do too, but a manual phase on an
	// idle run is the case under test.
	doc2 := strings.Replace(doc, "      - { id: build, type: command, run: \"true\" }\n",
		"      - { id: build, type: command, run: \"true\" }\n      - { id: hold, type: human_gate, question: \"Keep the run around?\" }\n", 1)
	ph2 := newPhasedHarness(t, doc2)
	runID2, err := ph2.e.CreateRun(context.Background(), Ticket{Title: "T"}, "subj", testProject)
	if err != nil {
		t.Fatal(err)
	}
	ph2.waitPing(t, event.PingGate)
	ph2.waitState(t, runID2, run.WaitingOnHuman)
	if err := ph2.e.StartPhase(runID2, "polish", ""); err != nil {
		t.Fatal(err)
	}
	ph2.waitPhaseCompleted(t, runID2, "polish", 1)
	evs := ph2.events(t, runID2)
	if ok, note := skipped(evs, "sync"); !ok || !strings.Contains(note, "when subject") {
		t.Errorf("sync not skipped: %v", eventTypes(evs))
	}
	failed := false
	for _, e := range evs {
		if f, ok := e.Payload.(*event.StageFailed); ok && f.StageID == "load" && f.Reason == event.ReasonInputMissing && strings.Contains(f.Error, "no merge request subject") {
			failed = true
		}
	}
	if !failed {
		t.Errorf("load did not fail routably: %v", eventTypes(evs))
	}
	if n := len(ph2.execsOf("./shine.sh")); n != 0 {
		t.Errorf("shine ran after on_fail: end (%d)", n)
	}
	if v := ph2.view(t, runID2); v.State != run.WaitingOnHuman {
		t.Errorf("state after the polish round = %s (the gate is still open)", v.State)
	}
}

// A bool parameter bound false folds into a skip: the gate never asks.
func TestWhenParamFalseSkips(t *testing.T) {
	doc := `name: gated
version: 4
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6, gate: true }
stages:
  - { id: approve, type: human_gate, question: "Go?", when: $gate }
  - { id: build, type: command, run: "true" }
`
	h := newHarness(t, mustFlow(t, doc))
	id, err := h.e.CreateRunWith(context.Background(), Ticket{Title: "T"}, "gated", testProject, RunInput{Params: map[string]string{"gate": "false"}})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	evs := h.events(t, id)
	if ok, _ := skipped(evs, "approve"); !ok || len(h.e.PendingPings()) != 0 {
		t.Errorf("gate not skipped: %v", eventTypes(evs))
	}
	if v := h.view(t, id); v.State != run.Completed {
		t.Errorf("state = %s", v.State)
	}
}
