package engine

// FLOWLANG §3's two ends, said apart: `end` finishes a phase and the run
// goes back to waiting; `done` finishes the run. And a dead end in a watcher
// — a failure with nowhere to go, a person giving up at the debug leaf —
// ends that round, never the run: a watcher may run any number of times, and
// the main chain it watches around is still waiting for its merge.

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/run"
	"apothecary/internal/runinput"
)

// failSync makes the conflicts phase's script fail, every time.
func failSync(ph *phasedHarness) {
	ph.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "./sync.sh") {
			return envport.ExecResult{ExitCode: 1, Output: "merge refused: the target rewrote this module"}, nil
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}
}

// conflictRound puts the merge request in conflict and confirms the round.
func conflictRound(t *testing.T, ph *phasedHarness) {
	t.Helper()
	ph.repoHost.SetTargetSHA(1, "t1")
	ph.repoHost.SetMergeable(1, repoport.MergeConflicts, "h1")
	ask := ph.waitPing(t, event.PingPhaseConfirm)
	if err := ph.e.AnswerPing(ask.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
}

// lastCompletion is the newest phase_completed of a phase.
func lastCompletion(evs []event.Event, phase string) *event.PhaseCompleted {
	var out *event.PhaseCompleted
	for _, e := range evs {
		if c, ok := e.Payload.(*event.PhaseCompleted); ok && c.Phase == phase {
			out = c
		}
	}
	return out
}

// A main chain that says done right after opening its merge request
// completes the run there and then — the watchers are armed on the open
// merge request, and none of them runs. (The same flow ending with end stays in
// review: TestPhasedRunWaitsInReviewAndCompletesOnMerge.)
func TestDoneEndsTheRunWhileWatchersAreArmed(t *testing.T) {
	doc := strings.Replace(phasedFlowDoc,
		"  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }\n",
		"  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } }, on_success: done }\n", 1)
	ph := newPhasedHarness(t, doc)
	runID, err := ph.e.CreateRun(context.Background(), runinput.Ticket{Title: "Phased"}, "phased", testProject)
	if err != nil {
		t.Fatal(err)
	}
	ph.waitState(t, runID, run.Completed)
	evs := ph.events(t, runID)
	for _, s := range phaseStarts(evs) {
		if s.Phase != flow.MainPhase {
			t.Errorf("phase %s started after done", s.Phase)
		}
	}
	if c := lastCompletion(evs, flow.MainPhase); c == nil || c.Reason != "" {
		t.Errorf("main's completion = %+v", c)
	}
	if len(ph.repoHost.MRs) != 1 {
		t.Errorf("merge requests = %d", len(ph.repoHost.MRs))
	}
}

// A watcher's stage that fails with nowhere to go ends that round, not the
// run: its phase_completed says why, the main chain stays parked on its
// merge request, and the merge still completes the run.
func TestADeadEndInAWatcherEndsTheRound(t *testing.T) {
	ph := newPhasedHarness(t, waitFlowDoc)
	failSync(ph)
	runID := ph.start(t)
	conflictRound(t, ph)
	ph.waitPhaseRuns(t, runID, "conflicts", 1)
	evs := ph.events(t, runID)
	c := lastCompletion(evs, "conflicts")
	if c == nil || !strings.Contains(c.Reason, "fix_conflicts") || !strings.Contains(c.Reason, "no on_fail") {
		t.Fatalf("conflicts' completion = %+v, want it to say why the round ended", c)
	}
	if _, abandoned := findPayload[*event.RunAbandoned](evs); abandoned {
		t.Fatal("a watcher's dead end abandoned the run")
	}
	if infos, err := ph.e.RunPhases(context.Background(), runID); err != nil || infos[0].Live == nil || infos[0].Live.ParkedAt != "merged" {
		t.Fatalf("main after the failed round = %+v, %v; want it still parked on the merge", infos, err)
	}
	ph.repoHost.SetMRState(1, repoport.MRMerged)
	ph.waitState(t, runID, run.Completed)
	if n := len(ph.execsOf("./after.sh")); n != 1 {
		t.Errorf("after ran %d times, want once after the merge", n)
	}
}

// Giving up at a watcher's debug leaf ends that round: the ping says so (its
// phase, and that giving up ends only the phase), the human's words become
// the round's reason, and the run goes back to waiting for the merge. In the
// main chain the same answer still abandons the run (TestDebugLeaf).
func TestGivingUpAtAWatchersDebugLeafEndsTheRound(t *testing.T) {
	doc := strings.Replace(waitFlowDoc,
		`      - { id: fix_conflicts, type: command, run: "./sync.sh" }`,
		`      - { id: fix_conflicts, type: command, run: "./sync.sh", on_fail: debug }`, 1)
	ph := newPhasedHarness(t, doc)
	failSync(ph)
	runID := ph.start(t)
	conflictRound(t, ph)
	dbg := ph.waitPing(t, PingDebug)
	if dbg.StageID != "fix_conflicts" || dbg.Phase != "conflicts" || !dbg.EndsPhase {
		t.Fatalf("debug ping = stage %q phase %q ends_phase %v; want the watcher's, ending only its round", dbg.StageID, dbg.Phase, dbg.EndsPhase)
	}
	if err := ph.e.AnswerPing(dbg.ID, PingAnswer{Answer: "reject", Feedback: "the target's refactor needs a person"}); err != nil {
		t.Fatal(err)
	}
	ph.waitPhaseRuns(t, runID, "conflicts", 1)
	evs := ph.events(t, runID)
	c := lastCompletion(evs, "conflicts")
	if c == nil || !strings.Contains(c.Reason, "debug rejected at stage fix_conflicts") || !strings.Contains(c.Reason, "needs a person") {
		t.Fatalf("conflicts' completion = %+v", c)
	}
	if _, abandoned := findPayload[*event.RunAbandoned](evs); abandoned {
		t.Fatal("giving up in a watcher abandoned the run")
	}
	ph.repoHost.SetMRState(1, repoport.MRMerged)
	ph.waitState(t, runID, run.Completed)
}
