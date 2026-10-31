package engine

// The repository host observations behind ci_failed, mr_mergeable,
// mr_head_moved and mr_approved: what each holds on, its occurrence key, and
// that the idle loop asks the repository host only for what the flow reads.

import (
	"context"
	"strings"
	"testing"
	"time"

	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/flow/signalspec"
	"apothecary/internal/mrs"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
)

// observeFlowDoc: the main chain opens the merge request; one watcher per
// signal under test, each a script the test counts; the merge ends the run.
func observeFlowDoc(watcher string) string {
	return strings.Replace(phasedFlowDoc, "outcomes:\n", watcher+"outcomes:\n", 1)
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
	ph.repoHost.SetChecks(1, repoport.Check{Name: "lint", State: repoport.CheckSuccess}, repoport.Check{Name: "test", State: repoport.CheckPending})
	settle()
	if n := ph.runsOf("./ci_fix.sh"); n != 0 {
		t.Fatalf("ci_fix ran %d times while the checks were still running", n)
	}
	ph.repoHost.SetChecks(1, repoport.Check{Name: "lint", State: repoport.CheckSuccess}, repoport.Check{Name: "test", State: repoport.CheckFailure})
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
	ph.repoHost.SetChecks(1, repoport.Check{Name: "lint", State: repoport.CheckFailure}, repoport.Check{Name: "test", State: repoport.CheckFailure})
	ph.waitRuns(t, "./ci_fix.sh", 2)
	// A neutral (cancelled, skipped) check is no failure.
	ph.repoHost.SetHead(1, "h2")
	ph.repoHost.SetChecks(1, repoport.Check{Name: "test", State: repoport.CheckNeutral}, repoport.Check{Name: "lint", State: repoport.CheckSuccess})
	settle()
	if n := ph.runsOf("./ci_fix.sh"); n != 2 {
		t.Errorf("a cancelled check opened ci_fix (%d runs)", n)
	}
	if v := ph.view(t, runID); v.State != run.InReview {
		t.Errorf("state = %s", v.State)
	}
}

// A chain that reads run.ci.failure is told what failed — each check's CI
// file, job and failed steps, no log — read once as it starts, and recorded
// so a recovered chain is told the same.
func TestCIFixReadsWhatFailed(t *testing.T) {
	ph := newPhasedHarness(t, observeFlowDoc("  - id: ci_fix\n    on: ci_failed\n    stages:\n"+
		"      - { id: ci_patch, type: agent, harness: $harness, model: $model, agent: builder, input: { failure: { from: run.ci.failure, type: failure } } }\n"))
	runID := ph.start(t)
	ph.repoHost.SetHead(1, "3f2a91c0ffee1234")
	ph.repoHost.SetCIFailures(1, repoport.CIFailure{Name: "test", URL: "https://gh/job/11", File: ".github/workflows/ci.yml",
		Job: "test (ubuntu)", Steps: []string{"Run go test ./..."}})
	ph.repoHost.SetChecks(1, repoport.Check{Name: "test", State: repoport.CheckFailure})
	ph.waitPhaseRuns(t, runID, "ci_fix", 1)
	fix := ph.promptsOf("fix")
	want := "# INPUT: failure (what failed in CI)\nCI failed on 3f2a91c0ffee:\n" +
		"- test — .github/workflows/ci.yml, job \"test (ubuntu)\", failed step \"Run go test ./...\" (https://gh/job/11)\n" +
		"The logs are not included"
	if len(fix) != 1 || !strings.Contains(fix[0], want) {
		t.Fatalf("the fix reads:\n%s", fix)
	}
	starts := phaseStarts(ph.events(t, runID))
	if last := starts[len(starts)-1]; last.CI == nil || last.CI.Head != "3f2a91c0ffee1234" || len(last.CI.Checks) != 1 {
		t.Errorf("phase_started.ci = %+v", last.CI)
	}
	// A chain that reads none does not ask.
	for _, s := range starts[:len(starts)-1] {
		if s.CI != nil {
			t.Errorf("%s read CI: %+v", s.Phase, s.CI)
		}
	}
}

func TestCIFailureIsAWatchersOnCIFailed(t *testing.T) {
	doc := observeFlowDoc("  - id: other\n    on: mr_conflicts\n    stages:\n" +
		"      - { id: ci_patch, type: agent, harness: $harness, model: $model, agent: builder, input: { failure: { from: run.ci.failure, type: failure } } }\n")
	if _, err := flow.Parse("t.yaml", []byte(doc)); err == nil || !strings.Contains(err.Error(), "run.ci.failure is what failed when a ci_failed watcher opened") {
		t.Fatalf("err = %v", err)
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
	ph.repoHost.SetMRState(1, repoport.MRMerged)
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
	if last := starts[len(starts)-1]; last.Phase != "approved" || last.Key != signalspec.MRApproved || last.Subject != "" {
		t.Errorf("start = %+v", last)
	}
}

// mr_approved as a watcher: one merge request's approval, once per head it
// was approved at — a push after the approval, approved again, is a new
// round; the same approval seen again is not.
func TestApprovedWatcherRunsOncePerApprovedHead(t *testing.T) {
	ph := newPhasedHarness(t, observeFlowDoc("  - id: ship\n    on: mr_approved\n    stages:\n      - { id: land, type: command, run: \"./land.sh\" }\n"))
	runID := ph.start(t)
	ph.repoHost.SetHead(1, "h1")
	settle()
	if n := ph.runsOf("./land.sh"); n != 0 {
		t.Fatalf("ship ran %d times before any approval", n)
	}
	ph.repoHost.SetApprovedBy(1, "rev")
	ph.waitRuns(t, "./land.sh", 1)
	starts := phaseStarts(ph.events(t, runID))
	if last := starts[len(starts)-1]; last.Phase != "ship" || last.Key != "h1" || last.Subject != "repo" || last.Tip != "h1" {
		t.Errorf("start = %+v", last)
	}
	settle()
	if n := ph.runsOf("./land.sh"); n != 1 {
		t.Fatalf("one approval ran ship %d times", n)
	}
	ph.repoHost.SetHead(1, "h2") // still approved: a new head is a new occurrence
	ph.waitRuns(t, "./land.sh", 2)
	ph.repoHost.SetMRState(1, repoport.MRMerged)
	ph.waitState(t, runID, run.Completed)
	if n := ph.runsOf("./land.sh"); n != 2 {
		t.Errorf("a merged merge request ran ship again (%d runs)", n)
	}
}

// Any observed signal may be waited on: a watcher's chain parks on
// mr_mergeable, on its own merge request, and goes on once the repository
// host says it can be merged.
func TestWaitForMergeableInAWatcher(t *testing.T) {
	ph := newPhasedHarness(t, observeFlowDoc("  - id: ship\n    on: manual\n    stages:\n"+
		"      - { id: ready, type: wait, for: mr_mergeable, on_fail: end }\n      - { id: land, type: command, run: \"./land.sh\" }\n"))
	runID := ph.start(t)
	ph.waitIdle(t, runID)
	if err := ph.e.StartPhase(runID, "ship", ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, parked := findPayload[*event.PhaseParked](ph.events(t, runID)); parked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the wait never parked")
		}
		time.Sleep(2 * time.Millisecond)
	}
	settle()
	if n := ph.runsOf("./land.sh"); n != 0 {
		t.Fatalf("land ran before the merge request could be merged (%d)", n)
	}
	ph.repoHost.SetMergeableNow(1, true)
	ph.waitRuns(t, "./land.sh", 1)
	ph.waitPhaseCompleted(t, runID, "ship", 1)
}

// A wait may park on the approval too; a close means it never comes.
func TestWaitForApproval(t *testing.T) {
	doc := strings.Replace(waitFlowDoc, "type: wait, for: mr_merged, on_fail: abort", "type: wait, for: mr_approved, on_fail: abort", 1)
	ph := newPhasedHarness(t, doc)
	runID := ph.start(t)
	ph.repoHost.SetApprovedBy(1, "rev")
	ph.waitRuns(t, "./after.sh", 1)
	ph.repoHost.SetMRState(1, repoport.MRMerged)
	ph.waitState(t, runID, run.Completed)

	ph2 := newPhasedHarness(t, doc)
	runID2 := ph2.start(t)
	ph2.repoHost.SetMRState(1, repoport.MRClosed)
	ph2.waitState(t, runID2, run.Abandoned)
}

// The run page's live status carries the head's checks, the approval and
// the repository host's merge verdict.
func TestMRStatusesCarryChecksAndApproval(t *testing.T) {
	ph := newPhasedHarness(t, phasedFlowDoc)
	runID := ph.start(t)
	ph.repoHost.SetHead(1, "h1")
	ph.repoHost.SetChecks(1, repoport.Check{Name: "test", State: repoport.CheckFailure, URL: "https://ci/1"})
	ph.repoHost.SetApprovedBy(1, "rev")
	ph.repoHost.SetMergeableNow(1, true)
	live, err := mrs.New(resolution.StaticResolver{R: ph.res}, &run.Reader{Store: ph.store}).MRStatuses(context.Background(), runID)
	if err != nil || len(live) != 1 {
		t.Fatalf("live = %+v, %v", live, err)
	}
	l := live[0]
	if l.Checks == nil || l.Checks.State != repoport.CheckFailure || l.Checks.Head != "h1" || len(l.Checks.Items) != 1 ||
		l.Approval == nil || !l.Approval.Approved || l.Approval.By[0] != "rev" || !l.MergeableNow {
		t.Errorf("live = %+v checks %+v approval %+v", l, l.Checks, l.Approval)
	}
}
