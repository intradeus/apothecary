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
	"apothecary/internal/runinput"
)

func skipped(evs []event.Event, stage string) (bool, string) {
	for _, e := range evs {
		if c, ok := e.Payload.(*event.StageCompleted); ok && c.StageID == stage && c.Skipped {
			return true, c.Note
		}
	}
	return false, ""
}

// run_if: run.ticket.tracker on a ticket written by hand: the agent never runs and no
// workspace is provisioned for it; the command after it gets the workspace.
func TestWhenTrackerSkipsWithoutASource(t *testing.T) {
	doc := `name: cond
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - { name: b, prompt: build it }
main:
  - { id: note, type: agent, harness: $harness, model: $model, agent: b, run_if: run.ticket.tracker }
  - { id: test, type: command, run: "true" }
`
	h := newHarness(t, mustFlow(t, doc))
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "by hand"}, "cond", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	evs := h.events(t, id)
	if ok, note := skipped(evs, "note"); !ok || !strings.Contains(note, "run_if run.ticket.tracker") {
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
	id2, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "imported", SourceID: "int-1", SourceKind: "fake", SourceKey: "DEV-1"}, "cond", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id2)
	if ok, _ := skipped(h.events(t, id2), "note"); ok || len(h.runner.SnapshotRunCalls()) != 1 {
		t.Errorf("with a tracker the agent must run (skipped %v, runs %d)", ok, len(h.runner.SnapshotRunCalls()))
	}
}

// run_if: run.ticket.change_type == fix runs only on a ticket that is a fix; a ticket of no stated
// type is a feature. The agent's TASK says which.
func TestWhenChangeType(t *testing.T) {
	doc := `name: cond
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - { name: b, prompt: build it, inputs: [ { name: ticket, type: ticket } ] }
main:
  - { id: repro, type: agent, harness: $harness, model: $model, agent: b, run_if: run.ticket.change_type == fix, input: { ticket: { from: run.ticket, type: ticket } } }
  - { id: test, type: command, run: "true" }
`
	h := newHarness(t, mustFlow(t, doc))
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "no type"}, "cond", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	if ok, note := skipped(h.events(t, id), "repro"); !ok || !strings.Contains(note, "run_if run.ticket.change_type == fix") {
		t.Fatalf("repro ran on a feature: %v", eventTypes(h.events(t, id)))
	}
	if v := h.view(t, id); v.TicketChangeType != "feat" {
		t.Errorf("change type = %q, want feat by default", v.TicketChangeType)
	}
	id2, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "crash", ChangeType: "fix"}, "cond", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id2)
	calls := h.runner.SnapshotRunCalls()
	if ok, _ := skipped(h.events(t, id2), "repro"); ok || len(calls) != 1 {
		t.Fatalf("on a fix the agent must run (skipped %v, runs %d)", ok, len(calls))
	}
	if !strings.Contains(calls[0].In.Prompt, "Change type: fix") {
		t.Errorf("the TASK does not name the change type:\n%s", calls[0].In.Prompt)
	}
}

// run_if: run.subject in a phase started by hand with no merge request skips;
// a subject action without the guard fails routably instead.
func TestWhenSubjectSkipsInASubjectlessPhaseRun(t *testing.T) {
	doc := `name: subj
version: 5
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: command, run: "true" }
watchers:
  - id: polish
    on: manual
    stages:
      - { id: sync, type: action, action: sync_target, run_if: run.subject }
      - { id: load, type: action, action: load_review_comments, output: { threads: { to: artifacts/threads.yaml, type: mr_threads } }, on_fail: end }
      - { id: shine, type: command, run: "./shine.sh" }
outcomes:
  - { id: shipped, on: mr_merged, then: done }
`
	ph := newPhasedHarness(t, doc)
	runID, err := ph.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "subj", testProject)
	if err != nil {
		t.Fatal(err)
	}
	ph.waitState(t, runID, run.Completed) // nothing armed: the main chain ends, the run completes
	if err := ph.e.StartPhase(runID, "polish", ""); err == nil {
		t.Fatal("a completed run must not start a phase")
	}
	// A ticket's main chain has no subject either, and the run stays between
	// chains only while something is armed on a merge request: that is the
	// chain to look at.
	doc2 := `name: subj
version: 5
parameters: { harness: fake, model: fake/model }
main:
  - { id: sync, type: action, action: sync_target, run_if: run.subject }
  - { id: load, type: action, action: load_review_comments, output: { threads: { to: artifacts/threads.yaml, type: mr_threads } }, on_fail: end }
  - { id: shine, type: command, run: "./shine.sh" }
outcomes:
  - { id: shipped, on: mr_merged, then: done }
`
	ph2 := newPhasedHarness(t, doc2)
	runID2, err := ph2.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "subj", testProject)
	if err != nil {
		t.Fatal(err)
	}
	ph2.waitState(t, runID2, run.Completed)
	evs := ph2.events(t, runID2)
	if ok, note := skipped(evs, "sync"); !ok || !strings.Contains(note, "run_if run.subject") {
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
}

// A bool parameter bound false folds into a skip: the gate never asks.
func TestWhenParamFalseSkips(t *testing.T) {
	doc := `name: gated
version: 5
parameters: { harness: fake, model: fake/model, gate: { type: bool, default: true } }
main:
  - { id: approve, type: human_gate, question: "Go?", run_if: $gate }
  - { id: build, type: command, run: "true" }
`
	h := newHarness(t, mustFlow(t, doc))
	id, err := h.e.CreateRunWith(context.Background(), runinput.Ticket{Title: "T"}, "gated", testProject, runinput.Input{Params: map[string]string{"gate": "false"}})
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
