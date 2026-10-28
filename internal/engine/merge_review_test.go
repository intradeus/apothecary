package engine

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

const automergeWatcher = "  - id: automerge\n    on: mr_mergeable\n    stages:\n" +
	"      - { id: merge, type: action, action: mr_merge, with: { method: squash, branch: delete }, on_fail: end }\n"

// The repository host says it can be merged: the watcher merges it — with
// the method asked, at the head the round began on — and the merge is the
// outcome that ends the run.
func TestMRMergeMergesAtThePhasesHead(t *testing.T) {
	ph := newPhasedHarness(t, observeFlowDoc(automergeWatcher))
	runID := ph.start(t)
	ph.repoHost.SetHead(1, "h1")
	ph.repoHost.SetMergeableNow(1, true)
	ph.waitState(t, runID, run.Completed)
	m := ph.repoHost.SnapshotMRs()[0]
	if m.State != ports.MRMerged || m.MergedWith != ports.MergeMethodSquash || !m.BranchDeleted {
		t.Errorf("mr = %+v", m)
	}
	merged, _ := findPayload[*event.MRMerged](ph.events(t, runID))
	if merged == nil || merged.Head != "h1" || merged.Method != "squash" || merged.Repo != "repo" || !merged.BranchDeleted {
		t.Errorf("mr_merged = %+v", merged)
	}
	for _, s := range phaseStarts(ph.events(t, runID)) {
		if s.Phase == "shipped" {
			return
		}
	}
	t.Error("the merge did not end the run through its merged outcome")
}

// A merge the repository host declines is the stage's failure, routed by
// on_fail — here ending the round, the run back in review; the next head the
// repository host calls mergeable is merged.
func TestMRMergeRefusedRoutesOnFail(t *testing.T) {
	ph := newPhasedHarness(t, observeFlowDoc(automergeWatcher))
	runID := ph.start(t)
	ph.repoHost.SetHead(1, "h1")
	ph.repoHost.SetMergeRefusal(1, "required status checks are expected")
	ph.repoHost.SetMergeableNow(1, true)
	ph.waitPhaseRuns(t, runID, "automerge", 1)
	evs := ph.events(t, runID)
	failed, _ := findPayload[*event.StageFailed](evs)
	if failed == nil || failed.StageID != "merge" || failed.Reason != event.ReasonInputInvalid || !strings.Contains(failed.Error, "required status checks") {
		t.Fatalf("failed = %+v", failed)
	}
	if v := ph.view(t, runID); v.State != run.InReview {
		t.Fatalf("state = %s", v.State)
	}
	ph.repoHost.SetMergeRefusal(1, "")
	ph.repoHost.SetHead(1, "h2")
	ph.waitState(t, runID, run.Completed)
}

// A reviewer agent's review_threads output is checked when its stage ends
// — a bad one is its own failure, retried with the reason — and post_review
// puts the good one on the merge request, named after the phase run.
func TestPostReviewFromAReviewOutput(t *testing.T) {
	doc := observeFlowDoc("  - id: look\n    on: manual\n    stages:\n" +
		"      - { id: read, type: agent, harness: $harness, model: $model, agent: pr_reviewer, input: { failure: { from: edge.failure, type: failure, optional: true } },\n" +
		"          output: { review: { to: artifacts/review.yaml, type: review_threads } }, on_fail: { to: read, max: 1 } }\n" +
		"      - { id: publish_review, type: action, action: post_review, input: { review: { from: artifacts/review.yaml, type: review_threads } } }\n")
	doc = strings.Replace(doc, "agents:\n",
		"agents:\n  - name: pr_reviewer\n    prompt: review it\n    inputs:\n      - { name: failure, type: failure, optional: true }\n"+
			"    outputs:\n      - { name: review, type: review_threads }\n", 1)
	ph := newPhasedHarness(t, doc)
	var reads atomic.Int32
	var mu sync.Mutex
	var prompts []string
	inner := ph.runner.RunFunc
	ph.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (ports.AgentResult, error) {
		if call.Spec.Agent == "pr_reviewer" {
			mu.Lock()
			prompts = append(prompts, call.In.Prompt)
			mu.Unlock()
			review := "summary: Fine overall.\nthreads:\n  - { path: a.go, line: 3, body: name it }\n"
			if reads.Add(1) == 1 {
				review = "threads:\n  - { path: a.go, line: 0, body: nowhere }\n"
			}
			ph.writeFile(t, call.Env, "artifacts/review.yaml", review)
		}
		return inner(ctx, call)
	}
	runID := ph.start(t)
	if err := ph.e.StartPhase(runID, "look", ""); err != nil {
		t.Fatal(err)
	}
	ph.waitPhaseRuns(t, runID, "look", 1)
	evs := ph.events(t, runID)
	failed, _ := findPayload[*event.StageFailed](evs)
	if failed == nil || failed.StageID != "read" || failed.Reason != event.ReasonOutputInvalid || !strings.Contains(failed.Error, "line must be 1 or more") {
		t.Fatalf("failed = %+v", failed)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 || !strings.Contains(prompts[1], "line must be 1 or more") {
		t.Errorf("the retry was not told why (%d prompts)", len(prompts))
	}
	reviews := ph.repoHost.SnapshotMRs()[0].Reviews
	if len(reviews) != 1 || reviews[0].Summary != "Fine overall." || len(reviews[0].Notes) != 1 || reviews[0].Notes[0].Path != "a.go" ||
		!strings.HasSuffix(reviews[0].ID, "/look-1/publish_review") {
		t.Fatalf("reviews = %+v", reviews)
	}
	posted, _ := findPayload[*event.ReviewPosted](evs)
	if posted == nil || posted.Subject != "repo" || posted.Inline != 1 {
		t.Errorf("review_posted = %+v", posted)
	}
}

// shipFlowDoc is "get approval, rebase once, merge" on someone's merge
// request: the approval opens the round, which rebases onto the target,
// pushes, waits until the repository host says it can be merged — the
// checks on the new head — and merges.
const shipFlowDoc = `name: shipper
version: 5
operates_on: merge_request
parameters: { harness: fake, model: fake/model }
main:
  - { id: look, type: command, run: "true" }
watchers:
  - id: ship
    on: mr_approved
    stages:
      - { id: rebase, type: action, action: sync_target, with: { strategy: rebase }, on_fail: end }
      - { id: push, type: action, action: push_branch, on_fail: end }
      - { id: ready, type: wait, for: mr_mergeable, timeout: 24h, on_fail: end }
      - { id: merge, type: action, action: mr_merge, on_fail: end }
outcomes:
  - { id: merged, on: mr_merged, then: done }
  - { id: closed, on: mr_closed, then: done }
`

// A merge after the round's own push is pinned to the head it pushed — not
// the head the round began on, which the push replaced — and only that.
func TestMRMergeIsPinnedToTheHeadTheRoundPushed(t *testing.T) {
	ph := newPhasedHarness(t, shipFlowDoc)
	git := newMergeGit("main")
	git.parents["a1"] = []string{"c0"}
	git.remote["feature/cache"] = "a1"
	git.head = "a1"
	git.advanceTarget("main")
	ref := ph.repoHost.AddOpenMR("alice", "Add caching", "feature/cache", "main", "a1")
	ph.envp.ExecFunc = func(env *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if cmd.Dir != "repo" {
			return ports.ExecResult{}, nil
		}
		res, err := git.exec(env, cmd)
		if strings.HasPrefix(strings.TrimSpace(cmd.Script), "git push") && res.ExitCode == 0 {
			git.mu.Lock()
			head := git.remote["feature/cache"]
			git.mu.Unlock()
			ph.repoHost.SetHead(ref.IID, head)
		}
		return res, err
	}
	runID, err := ph.e.CreateRunWith(context.Background(), Ticket{Title: "Add caching (!1 by alice)"}, "shipper", testProject, RunInput{MR: &RunMR{IID: ref.IID}})
	if err != nil {
		t.Fatal(err)
	}
	ph.waitState(t, runID, run.InReview)
	ph.repoHost.SetApprovedBy(ref.IID, "rev")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, parked := findPayload[*event.PhaseParked](ph.events(t, runID)); parked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the round never parked at ready; events %v", eventTypes(ph.events(t, runID)))
		}
		time.Sleep(2 * time.Millisecond)
	}
	_, rebases, pushes, _ := git.snapshot()
	pushed := ph.repoHost.SnapshotMRs()[0].HeadSHA
	if rebases != 1 || len(pushes) != 1 || pushes[0] != "lease:feature/cache" || pushed == "a1" {
		t.Fatalf("rebases %d pushes %v head %s: one rebase, pushed leased", rebases, pushes, pushed)
	}
	ph.repoHost.SetMergeableNow(ref.IID, true)
	ph.waitState(t, runID, run.Completed)
	evs := ph.events(t, runID)
	merged, _ := findPayload[*event.MRMerged](evs)
	if merged == nil || merged.Head != pushed {
		t.Fatalf("mr_merged = %+v, want it pinned to the pushed head %s", merged, pushed)
	}
	// In the round that pushed: a merge pinned to the head it began on is
	// refused, and only the next approval's round would land it.
	if failed, ok := findPayload[*event.StageFailed](evs); ok {
		t.Errorf("stage_failed = %+v", failed)
	}
	rounds := 0
	for _, s := range phaseStarts(evs) {
		if s.Phase == "ship" {
			rounds++
		}
	}
	if rounds != 1 {
		t.Errorf("ship ran %d rounds, want the one that pushed to merge", rounds)
	}
	if m := ph.repoHost.SnapshotMRs()[0]; m.State != ports.MRMerged {
		t.Errorf("mr = %+v", m)
	}
}
