package engine

// The repository host observations behind ci_failed, mr_mergeable,
// mr_head_moved and the mr_approved outcome: what each holds on, its
// occurrence key, and that the idle loop asks the repository host only for
// what the flow reads.

import (
	"context"
	"strings"
	"testing"
	"time"

	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// observeFlowDoc: deliver opens the merge request; one reaction per signal
// under test, each a script the test counts; the merge ends the run.
func observeFlowDoc(reaction string) string {
	return strings.Replace(phasedFlowDoc,
		"  - id: shipped\n",
		reaction+"  - id: shipped\n", 1)
}

func (ph *phasedHarness) runsOf(script string) int { return len(ph.execsOf(script)) }

// waitRuns waits until script ran n times.
func (ph *phasedHarness) waitRuns(t *testing.T, script string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ph.runsOf(script) >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s ran %d time(s), want %d", script, ph.runsOf(script), n)
}

// settle lets the idle loop poll a few times.
func settle() { time.Sleep(60 * time.Millisecond) }

func TestCIFailedOpensOncePerHeadAndFailure(t *testing.T) {
	ph := newPhasedHarness(t, observeFlowDoc("  - id: ci_fix\n    on: ci_failed\n    stages:\n      - { id: patch, type: command, run: \"./ci_fix.sh\" }\n"))
	runID := ph.start(t)
	ph.repoHost.SetHead(1, "h1")
	ph.repoHost.SetChecks(1, ports.Check{Name: "lint", State: ports.CheckSuccess}, ports.Check{Name: "test", State: ports.CheckPending})
	settle()
	if n := ph.runsOf("./ci_fix.sh"); n != 0 {
		t.Fatalf("ci_fix ran %d times while the checks were still running", n)
	}
	ph.repoHost.SetChecks(1, ports.Check{Name: "lint", State: ports.CheckSuccess}, ports.Check{Name: "test", State: ports.CheckFailure})
	ph.waitRuns(t, "./ci_fix.sh", 1)
	starts := phaseStarts(ph.events(t, runID))
	if last := starts[len(starts)-1]; last.Phase != "ci_fix" || last.Key != "h1:test" || last.Subject != "repo" {
		t.Errorf("start = %+v", last)
	}
	settle()
	if n := ph.runsOf("./ci_fix.sh"); n != 1 {
		t.Fatalf("the same failure opened ci_fix %d times", n)
	}
	// Another check failing on the same head is a new failure.
	ph.repoHost.SetChecks(1, ports.Check{Name: "lint", State: ports.CheckFailure}, ports.Check{Name: "test", State: ports.CheckFailure})
	ph.waitRuns(t, "./ci_fix.sh", 2)
	// A neutral (cancelled, skipped) check is no failure.
	ph.repoHost.SetHead(1, "h2")
	ph.repoHost.SetChecks(1, ports.Check{Name: "test", State: ports.CheckNeutral}, ports.Check{Name: "lint", State: ports.CheckSuccess})
	settle()
	if n := ph.runsOf("./ci_fix.sh"); n != 2 {
		t.Errorf("a cancelled check opened ci_fix (%d runs)", n)
	}
	if v := ph.view(t, runID); v.State != run.InReview {
		t.Errorf("state = %s", v.State)
	}
}

// A flow that reads no check never makes the repository host list them.
func TestChecksAreReadOnlyWhenAFlowNeedsThem(t *testing.T) {
	ph := newPhasedHarness(t, phasedFlowDoc)
	ph.start(t)
	settle()
	ph.repoHost.SetHead(1, "h1")
	settle()
	if n := ph.repoHost.ChecksCalls; n != 0 {
		t.Errorf("MRChecks called %d times for a flow with no ci_failed", n)
	}
}

func TestMergeableOpensOncePerHead(t *testing.T) {
	ph := newPhasedHarness(t, observeFlowDoc("  - id: automerge\n    on: { signal: mr_mergeable, start: auto }\n    stages:\n      - { id: merge, type: command, run: \"./merge.sh\" }\n"))
	runID := ph.start(t)
	ph.repoHost.SetHead(1, "h1")
	settle()
	if n := ph.runsOf("./merge.sh"); n != 0 {
		t.Fatalf("merged before the repository host allowed it (%d)", n)
	}
	ph.repoHost.SetMergeableNow(1, true)
	ph.waitRuns(t, "./merge.sh", 1)
	settle()
	if n := ph.runsOf("./merge.sh"); n != 1 {
		t.Fatalf("one head opened automerge %d times", n)
	}
	ph.repoHost.SetHead(1, "h2")
	ph.waitRuns(t, "./merge.sh", 2)
	ph.repoHost.SetMRState(1, ports.MRMerged)
	ph.waitState(t, runID, run.Completed)
}

// The run's own commits are not someone else's push: the head its merge
// request was opened at, and a head a phase run already began on, are
// known; a new head opens the phase once it has stood still for quiet_for.
func TestHeadMovedIsSomeoneElsesPush(t *testing.T) {
	ph := newPhasedHarness(t, observeFlowDoc("  - id: again\n    on: { signal: mr_head_moved, quiet_for: 10m }\n    stages:\n      - { id: rereview, type: command, run: \"./rereview.sh\" }\n"))
	ph.repoHost.NewHead = "own1"
	runID := ph.start(t)
	created, _ := findPayload[*event.MRCreated](ph.events(t, runID))
	if created == nil || created.Head != "own1" {
		t.Fatalf("mr_created = %+v, want the head the merge request opened at", created)
	}
	settle()
	if n := ph.runsOf("./rereview.sh"); n != 0 {
		t.Fatalf("the run's own head opened the phase (%d)", n)
	}
	ph.repoHost.SetHead(1, "theirs1")
	settle()
	if n := ph.runsOf("./rereview.sh"); n != 0 {
		t.Fatalf("opened before the head stood still for quiet_for (%d)", n)
	}
	ph.clock.Advance(11 * time.Minute)
	ph.waitRuns(t, "./rereview.sh", 1)
	starts := phaseStarts(ph.events(t, runID))
	if last := starts[len(starts)-1]; last.Phase != "again" || last.Key != "theirs1" || last.Tip != "theirs1" {
		t.Errorf("start = %+v", last)
	}
	ph.clock.Advance(11 * time.Minute)
	settle()
	if n := ph.runsOf("./rereview.sh"); n != 1 {
		t.Fatalf("the same push opened the phase %d times", n)
	}
	// Back to a head the run knows: nothing.
	ph.repoHost.SetHead(1, "own1")
	ph.clock.Advance(11 * time.Minute)
	settle()
	if n := ph.runsOf("./rereview.sh"); n != 1 {
		t.Errorf("a known head opened the phase (%d)", n)
	}
}

// Replay knows the same heads: pushed tips, the merge request's opening
// head, a phase run's tip — per checkout.
func TestReplayKnowsTheRunsHeads(t *testing.T) {
	fl := mustFlow(t, parkFlowDoc)
	ev := func(p event.Payload) event.Event { return event.Event{RunID: "r", Type: p.EventType(), Payload: p} }
	rs := replay([]event.Event{
		ev(&event.RunCreated{TicketTitle: "t", FlowName: "park", Phase: "deliver", By: "human"}),
		ev(&event.PhaseStarted{Phase: "deliver", By: "human"}),
		ev(&event.BranchPushed{Repo: "repo", Branch: "b", Tip: "p1"}),
		ev(&event.MRCreated{Repo: "repo", Branch: "b", MRURL: "https://x/1", MRIID: 1, Head: "m1"}),
		ev(&event.PhaseCompleted{Phase: "deliver"}),
		ev(&event.PhaseStarted{Phase: "conflicts", Subject: "repo", Key: "k", By: "signal", Tip: "t1"}),
	}, fl)
	for _, sha := range []string{"p1", "m1", "t1"} {
		if !rs.knownHead("repo", sha) {
			t.Errorf("%s unknown after replay", sha)
		}
	}
	if rs.knownHead("repo", "other") || rs.knownHead("api", "p1") {
		t.Error("a head is known per checkout, and only the run's own")
	}
}

// mr_approved as an outcome: the run is done once every merge request is
// approved — read only because the flow has a signal on it.
func TestApprovedOutcomeEndsTheRun(t *testing.T) {
	doc := strings.Replace(phasedFlowDoc, "  - id: shipped\n    on: mr_merged\n    then: done\n",
		"  - { id: approved, on: mr_approved, then: done }\n", 1)
	ph := newPhasedHarness(t, doc)
	runID := ph.start(t)
	settle()
	if v := ph.view(t, runID); v.State != run.InReview {
		t.Fatalf("state before any approval = %s", v.State)
	}
	ph.repoHost.SetApprovedBy(1, "rev")
	ph.waitState(t, runID, run.Completed)
	starts := phaseStarts(ph.events(t, runID))
	if last := starts[len(starts)-1]; last.Phase != "approved" || last.Key != "approved" || last.Subject != "" {
		t.Errorf("start = %+v", last)
	}
}

// A wait may park on the approval too; a close means it never comes.
func TestWaitForApproval(t *testing.T) {
	doc := strings.Replace(waitFlowDoc, "type: wait, for: mr_merged, on_fail: abort", "type: wait, for: mr_approved, on_fail: abort", 1)
	ph := newPhasedHarness(t, doc)
	runID := ph.start(t)
	ph.repoHost.SetApprovedBy(1, "rev")
	ph.waitRuns(t, "./after.sh", 1)
	ph.repoHost.SetMRState(1, ports.MRMerged)
	ph.waitState(t, runID, run.Completed)

	ph2 := newPhasedHarness(t, doc)
	runID2 := ph2.start(t)
	ph2.repoHost.SetMRState(1, ports.MRClosed)
	ph2.waitState(t, runID2, run.Abandoned)
}

// The run page's live status carries the head's checks, the approval and
// the repository host's merge verdict.
func TestMRStatusesCarryChecksAndApproval(t *testing.T) {
	ph := newPhasedHarness(t, phasedFlowDoc)
	runID := ph.start(t)
	ph.repoHost.SetHead(1, "h1")
	ph.repoHost.SetChecks(1, ports.Check{Name: "test", State: ports.CheckFailure, URL: "https://ci/1"})
	ph.repoHost.SetApprovedBy(1, "rev")
	ph.repoHost.SetMergeableNow(1, true)
	live, err := ph.e.MRStatuses(context.Background(), runID)
	if err != nil || len(live) != 1 {
		t.Fatalf("live = %+v, %v", live, err)
	}
	l := live[0]
	if l.Checks == nil || l.Checks.State != ports.CheckFailure || l.Checks.Head != "h1" || len(l.Checks.Items) != 1 ||
		l.Approval == nil || !l.Approval.Approved || l.Approval.By[0] != "rev" || !l.MergeableNow {
		t.Errorf("live = %+v checks %+v approval %+v", l, l.Checks, l.Approval)
	}
}
