package engine

// FLOWLANG §1 outcomes: an outcome fires once, when the merge request's fate
// is known, and ends the run — after its chain, if it has one, the way its
// `then:` says (done by default). An outcome is what keeps a run alive while
// its merge request is open, and what ends it after.

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/run"
	"apothecary/internal/runinput"
)

// outcomeFlowDoc: the builder's shape in small. The main chain ends with the
// merge request open; nothing reacts on its own (both watchers start by
// hand), so the outcomes alone keep the run waiting: merged runs a last stage
// and the run is done, closed abandons it on the spot.
var outcomeFlowDoc = strings.NewReplacer(
	"on: { signal: mr_conflicts, start: confirm }", "on: { signal: mr_conflicts, start: manual }",
	"on: { signal: mr_review_settled, quiet_for: 1h, max: 2 }", "on: manual",
	"  - id: shipped\n    on: mr_merged\n    then: done\n",
	"  - id: merged\n    on: mr_merged\n    stages:\n      - { id: ticket_done, type: command, run: \"./ticket_done.sh\", on_fail: done }\n"+
		"  - id: closed\n    on: mr_closed\n    then: abort\n",
).Replace(phasedFlowDoc)

// An open merge request keeps an outcome armed — the run waits in review
// with nothing else to do — and the merge opens it: its stage runs, on no
// subject, and the run completes.
func TestMergedOutcomeRunsItsStagesAndEndsTheRun(t *testing.T) {
	ph := newPhasedHarness(t, outcomeFlowDoc)
	runID := ph.start(t) // in review: only the outcomes are armed
	infos, err := ph.e.RunPhases(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]PhaseInfo{}
	for _, p := range infos {
		byID[p.ID] = p
	}
	if m, c := byID["merged"], byID["closed"]; m.Kind != "outcome" || m.Then != "done" || m.Max != -1 || c.Kind != "outcome" || c.Then != "abort" {
		t.Errorf("phases: merged %+v closed %+v", m, c)
	}
	ph.repoHost.SetMRState(1, repoport.MRMerged)
	ph.waitState(t, runID, run.Completed)
	if n := len(ph.execsOf("./ticket_done.sh")); n != 1 {
		t.Errorf("ticket_done ran %d times, want once after the merge", n)
	}
	evs := ph.events(t, runID)
	starts := phaseStarts(evs)
	if len(starts) != 2 || starts[1].Phase != "merged" || starts[1].Subject != "" || starts[1].Key != "mr_merged" || starts[1].By != "signal" {
		t.Errorf("phase starts = %+v, want main, then merged on no subject", starts)
	}
	if c := lastCompletion(evs, "merged"); c == nil || c.Reason != "" {
		t.Errorf("merged's completion = %+v", c)
	}
}

// A merge request closed unmerged opens the closed outcome on that merge
// request; `then: abort` abandons the run there and then, no stage run.
func TestClosedOutcomeAbandonsTheRunOnTheSpot(t *testing.T) {
	ph := newPhasedHarness(t, outcomeFlowDoc)
	runID := ph.start(t)
	ph.repoHost.SetMRState(1, repoport.MRClosed)
	ph.waitState(t, runID, run.Abandoned)
	evs := ph.events(t, runID)
	ab, _ := findPayload[*event.RunAbandoned](evs)
	if ab == nil || ab.StalledAt != "" || !strings.Contains(ab.Reason, "outcome closed") {
		t.Errorf("abandon = %+v", ab)
	}
	starts := phaseStarts(evs)
	if len(starts) != 2 || starts[1].Phase != "closed" || starts[1].Subject != "repo" || starts[1].Key != "closed" {
		t.Errorf("phase starts = %+v, want closed on the closed merge request", starts)
	}
	if c := lastCompletion(evs, "closed"); c == nil {
		t.Error("the closed outcome never completed")
	}
	for _, e := range evs {
		if s, ok := e.Payload.(*event.StageStarted); ok && s.StageID == "ticket_done" {
			t.Error("the merged outcome ran on a closed merge request")
		}
	}
}

// Under start: confirm the outcome asks first — the question says it ends
// the run — and approve runs it.
func TestOutcomeAsksFirstUnderConfirm(t *testing.T) {
	doc := strings.Replace(outcomeFlowDoc, "on: mr_merged", "on: { signal: mr_merged, start: confirm }", 1)
	ph := newPhasedHarness(t, doc)
	runID := ph.start(t)
	ph.repoHost.SetMRState(1, repoport.MRMerged)
	ask := ph.waitPing(t, event.PingPhaseConfirm)
	if ask.Phase != "merged" || !strings.Contains(ask.Question, "end the run?") || !strings.Contains(ask.Question, "The merge request https://gitlab.local/mr/1 merged.") {
		t.Errorf("confirm ping = %+v", ask)
	}
	if err := ph.e.AnswerPing(ask.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	ph.waitState(t, runID, run.Completed)
	if n := len(ph.execsOf("./ticket_done.sh")); n != 1 {
		t.Errorf("ticket_done ran %d times", n)
	}
}

// Declined, an outcome never fires again for that fact — and with the merge
// request merged nothing is armed, so the run completes without it.
func TestDecliningAnOutcomeLeavesTheRunToEnd(t *testing.T) {
	doc := strings.Replace(outcomeFlowDoc, "on: mr_merged", "on: { signal: mr_merged, start: confirm }", 1)
	ph := newPhasedHarness(t, doc)
	runID := ph.start(t)
	ph.repoHost.SetMRState(1, repoport.MRMerged)
	ask := ph.waitPing(t, event.PingPhaseConfirm)
	if err := ph.e.AnswerPing(ask.ID, PingAnswer{Answer: "reject", Feedback: "the ticket stays open"}); err != nil {
		t.Fatal(err)
	}
	ph.waitState(t, runID, run.Completed)
	if _, declined := findPayload[*event.PhaseDeclined](ph.events(t, runID)); !declined {
		t.Error("no phase_declined")
	}
	if n := len(ph.execsOf("./ticket_done.sh")); n != 0 {
		t.Errorf("a declined outcome ran %d times", n)
	}
}

// A dead end inside an outcome ends the run, not a round: the run's end
// itself failed, and someone has to know.
func TestADeadEndInAnOutcomeAbandonsTheRun(t *testing.T) {
	doc := strings.Replace(outcomeFlowDoc, `run: "./ticket_done.sh", on_fail: done }`, `run: "./ticket_done.sh" }`, 1)
	ph := newPhasedHarness(t, doc)
	ph.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "./ticket_done.sh") {
			return envport.ExecResult{ExitCode: 1, Output: "tracker refused the move"}, nil
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}
	runID := ph.start(t)
	ph.repoHost.SetMRState(1, repoport.MRMerged)
	ph.waitState(t, runID, run.Abandoned)
	ab, _ := findPayload[*event.RunAbandoned](ph.events(t, runID))
	if ab == nil || ab.StalledAt != "ticket_done" {
		t.Errorf("abandon = %+v, want the outcome's dead end to end the run", ab)
	}
}

// `on_success: abort` is the flow abandoning the run from a success — the
// main chain here, with its required route to done on the edge.
func TestOnSuccessAbortAbandonsTheRun(t *testing.T) {
	doc := "name: short\nversion: 5\nparameters: { harness: fake, model: fake/model }\nmain:\n" +
		"  - { id: s, type: command, run: \"true\", on_success: abort, on_fail: done }\n"
	ph := newPhasedHarness(t, doc)
	runID, err := ph.e.CreateRun(context.Background(), runinput.Ticket{Title: "Short"}, "short", testProject)
	if err != nil {
		t.Fatal(err)
	}
	ph.waitState(t, runID, run.Abandoned)
	ab, _ := findPayload[*event.RunAbandoned](ph.events(t, runID))
	if ab == nil || ab.StalledAt != "s" || !strings.Contains(ab.Reason, "aborted by flow") || !strings.Contains(ab.Reason, "on_success") {
		t.Errorf("abandon = %+v", ab)
	}
}
