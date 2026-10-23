package engine

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

const automergeReaction = "  - id: automerge\n    on: mr_mergeable\n    stages:\n" +
	"      - { id: merge, type: action, action: mr_merge, input: { method: squash, branch: delete }, on_fail: end }\n"

// The forge says it can be merged: the reaction merges it — with the
// method asked, at the head the phase began on — and the merge is the
// outcome that ends the run.
func TestMRMergeMergesAtThePhasesHead(t *testing.T) {
	ph := newPhasedHarness(t, observeFlowDoc(automergeReaction))
	runID := ph.start(t)
	ph.forge.SetHead(1, "h1")
	ph.forge.SetMergeableNow(1, true)
	ph.waitState(t, runID, run.Completed)
	m := ph.forge.SnapshotMRs()[0]
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

// A merge the forge declines is the stage's failure, routed by on_fail —
// here ending the round, the run back in review; the next head the forge
// calls mergeable is merged.
func TestMRMergeRefusedRoutesOnFail(t *testing.T) {
	ph := newPhasedHarness(t, observeFlowDoc(automergeReaction))
	runID := ph.start(t)
	ph.forge.SetHead(1, "h1")
	ph.forge.SetMergeRefusal(1, "required status checks are expected")
	ph.forge.SetMergeableNow(1, true)
	ph.waitPhaseRuns(t, runID, "automerge", 1)
	evs := ph.events(t, runID)
	failed, _ := findPayload[*event.StageFailed](evs)
	if failed == nil || failed.StageID != "merge" || failed.Reason != event.ReasonInputInvalid || !strings.Contains(failed.Error, "required status checks") {
		t.Fatalf("failed = %+v", failed)
	}
	if v := ph.view(t, runID); v.State != run.InReview {
		t.Fatalf("state = %s", v.State)
	}
	ph.forge.SetMergeRefusal(1, "")
	ph.forge.SetHead(1, "h2")
	ph.waitState(t, runID, run.Completed)
}

// A reviewer agent's review_threads output is checked when its stage ends
// — a bad one is its own failure, retried with the reason — and post_review
// puts the good one on the merge request, named after the phase run.
func TestPostReviewFromAReviewOutput(t *testing.T) {
	doc := observeFlowDoc("  - id: look\n    on: manual\n    stages:\n" +
		"      - { id: read, type: agent, agent: pr_reviewer, input: { failure: $FAILURE }, on_fail: { to: read, max: 1 } }\n" +
		"      - { id: publish_review, type: action, action: post_review, input: { review: artifacts/review.yaml } }\n")
	doc = strings.Replace(doc, "agents:\n",
		"agents:\n  - name: pr_reviewer\n    prompt: review it\n    inputs:\n      - { name: failure, optional: true }\n"+
			"    outputs:\n      - { name: review, type: review_threads, path: artifacts/review.yaml }\n", 1)
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
	reviews := ph.forge.SnapshotMRs()[0].Reviews
	if len(reviews) != 1 || reviews[0].Summary != "Fine overall." || len(reviews[0].Notes) != 1 || reviews[0].Notes[0].Path != "a.go" ||
		!strings.HasSuffix(reviews[0].ID, "/look-1/publish_review") {
		t.Fatalf("reviews = %+v", reviews)
	}
	posted, _ := findPayload[*event.ReviewPosted](evs)
	if posted == nil || posted.Subject != "repo" || posted.Inline != 1 {
		t.Errorf("review_posted = %+v", posted)
	}
}
