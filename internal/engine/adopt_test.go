package engine

// Runs created on a merge request someone opened (an entrypoint on
// mr_opened): the run works on that merge request's branch, from the head
// the forge keeps a ref for, acts on it from its first phase, reads the
// author's later pushes, and ends with it.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/builtin"
	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

const reviewerFlowDoc = `name: rev
version: 4
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
agents:
  - name: pr_reviewer
    prompt: review it
    inputs:
      - { name: changes }
    outputs:
      - { name: review, type: review_threads, path: artifacts/review.yaml }
phases:
  - id: first_look
    on: mr_opened
    stages:
      - { id: read, type: agent, agent: pr_reviewer, input: { changes: $DIFF_TARGET } }
      - { id: post, type: action, action: post_review, input: { review: artifacts/review.yaml } }
  - id: again
    on: mr_head_moved
    stages:
      - { id: reread, type: agent, agent: pr_reviewer, input: { changes: $DIFF_TARGET } }
      - { id: repost, type: action, action: post_review, input: { review: artifacts/review.yaml } }
  - { id: merged, on: mr_merged, then: done }
  - { id: closed, on: mr_closed, then: done }
`

// newReviewerHarness: the reviewer agent writes a one-note review; every
// command the workspace is asked to run is recorded.
func newReviewerHarness(t *testing.T, doc string) (*phasedHarness, func() []string) {
	t.Helper()
	ph := newPhasedHarness(t, doc)
	inner := ph.runner.RunFunc
	ph.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (ports.AgentResult, error) {
		if call.Spec.Agent == "pr_reviewer" {
			ph.writeFile(t, call.Env, "artifacts/review.yaml", "summary: Reads well.\nthreads:\n  - { path: a.go, line: 3, body: name it }\n")
		}
		return inner(ctx, call)
	}
	var mu sync.Mutex
	var scripts []string
	ph.envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		mu.Lock()
		scripts = append(scripts, cmd.Script)
		mu.Unlock()
		return ports.ExecResult{}, nil
	}
	return ph, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), scripts...)
	}
}

func hasScript(scripts []string, substr string) bool {
	for _, s := range scripts {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func TestRunOnSomeonesMergeRequest(t *testing.T) {
	ph, scripts := newReviewerHarness(t, reviewerFlowDoc)
	ref := ph.forge.AddOpenMR("alice", "Add caching", "feature/cache", "main", "a1")
	runID, err := ph.e.CreateRunWith(context.Background(), Ticket{Title: "Review caching"}, "rev", testProject, RunInput{MR: &RunMR{IID: ref.IID}})
	if err != nil {
		t.Fatal(err)
	}
	v := ph.waitState(t, runID, run.InReview)

	created, _ := findPayload[*event.RunCreated](ph.events(t, runID))
	co := created.Checkouts[0]
	if created.MR == nil || created.MR.Author != "alice" || created.MR.Head != "a1" || created.Branch != "feature/cache" || created.SourceBranch != "main" ||
		co.Branch != "feature/cache" || co.Source != "main" || co.Fetch != "refs/merge-requests/1/head" || co.Fork {
		t.Fatalf("run_created = %+v mr %+v checkout %+v", created, created.MR, co)
	}
	if len(v.MRs) != 1 || v.MRs[0].Author != "alice" || v.MRs[0].IID != ref.IID || v.Adopted == nil {
		t.Errorf("view mrs = %+v adopted %+v", v.MRs, v.Adopted)
	}
	if !hasScript(scripts(), `git switch "feature/cache" 2>/dev/null || { git fetch origin 'refs/merge-requests/1/head' && git switch -c "feature/cache" FETCH_HEAD; }`) {
		t.Errorf("bootstrap did not check out the merge request's head:\n%s", strings.Join(scripts(), "\n"))
	}
	starts := phaseStarts(ph.events(t, runID))
	if len(starts) != 1 || starts[0].Phase != "first_look" || starts[0].Subject != "repo" || starts[0].Tip != "a1" {
		t.Fatalf("starts = %+v", starts)
	}
	if !hasScript(scripts(), "git diff --name-status origin/main...HEAD") {
		t.Error("$DIFF_TARGET was not measured against the merge request's target")
	}
	if rv := ph.forge.SnapshotMRs()[0].Reviews; len(rv) != 1 || rv[0].Summary != "Reads well." {
		t.Fatalf("reviews = %+v", rv)
	}

	// The author pushes: the run reads the new head — fast-forwarded to it
	// first — and reviews again. Its own adoption head was known: one
	// re-review only.
	ph.forge.SetHead(ref.IID, "a2")
	ph.waitPhaseRuns(t, runID, "again", 1)
	if !hasScript(scripts(), "git fetch origin 'refs/merge-requests/1/head' && git merge --ff-only FETCH_HEAD") {
		t.Error("the re-review did not bring the checkout to the new head")
	}
	if rv := ph.forge.SnapshotMRs()[0].Reviews; len(rv) != 2 || rv[0].ID == rv[1].ID {
		t.Fatalf("reviews after the push = %+v", rv)
	}
	ph.forge.SetMRState(ref.IID, ports.MRMerged)
	ph.waitState(t, runID, run.Completed)
}

// Run creation holds the two kinds of entrypoint apart, and a merge request
// is created on only while open.
func TestRunOnAMergeRequestRefusals(t *testing.T) {
	ctx := context.Background()
	ph, _ := newReviewerHarness(t, reviewerFlowDoc)
	ref := ph.forge.AddOpenMR("alice", "Add caching", "feature/cache", "main", "a1")
	if _, err := ph.e.CreateRunWith(ctx, Ticket{Title: "T"}, "rev", testProject, RunInput{}); err == nil || !strings.Contains(err.Error(), "begins runs on a merge request someone opened: name one") {
		t.Errorf("no merge request: %v", err)
	}
	ph.forge.SetMRState(ref.IID, ports.MRClosed)
	if _, err := ph.e.CreateRunWith(ctx, Ticket{Title: "T"}, "rev", testProject, RunInput{MR: &RunMR{IID: ref.IID}}); err == nil || !strings.Contains(err.Error(), "is closed") {
		t.Errorf("closed merge request: %v", err)
	}
	ph2 := newPhasedHarness(t, phasedFlowDoc)
	other := ph2.forge.AddOpenMR("bob", "Fix", "fix/x", "main", "b1")
	if _, err := ph2.e.CreateRunWith(ctx, Ticket{Title: "T"}, "phased", testProject, RunInput{MR: &RunMR{IID: other.IID}}); err == nil || !strings.Contains(err.Error(), "no entrypoint that begins a run on a merge request") {
		t.Errorf("flow with run_start only: %v", err)
	}
	if _, err := ph2.e.CreateRunWith(ctx, Ticket{Title: "T"}, "phased", testProject, RunInput{Phase: "deliver", MR: &RunMR{IID: other.IID}}); err == nil || !strings.Contains(err.Error(), "begins a fresh run") {
		t.Errorf("run_start phase with a merge request: %v", err)
	}
}

// A fork's merge request is read through the forge's ref on a local branch
// of its own; there is nothing here to push it to. A rerun stays on it.
func TestForkMergeRequestIsReadOnlyAndRerunStaysOnIt(t *testing.T) {
	doc := strings.Replace(reviewerFlowDoc,
		"      - { id: post, type: action, action: post_review, input: { review: artifacts/review.yaml } }\n",
		"      - { id: post, type: action, action: post_review, input: { review: artifacts/review.yaml } }\n      - { id: push, type: action, action: push_branch }\n", 1)
	ph, _ := newReviewerHarness(t, doc)
	ref := ph.forge.AddOpenMR("eve", "From my fork", "main", "main", "f1")
	ph.forge.MRs[0].Fork = true
	runID, err := ph.e.CreateRunWith(context.Background(), Ticket{Title: "Fork"}, "rev", testProject, RunInput{MR: &RunMR{IID: ref.IID}})
	if err != nil {
		t.Fatal(err)
	}
	ph.waitState(t, runID, run.Abandoned) // the push is a dead end of the entrypoint
	evs := ph.events(t, runID)
	created, _ := findPayload[*event.RunCreated](evs)
	if created.Branch != "mr-1" || !created.Checkouts[0].Fork {
		t.Errorf("fork checkout = %+v", created.Checkouts[0])
	}
	failed, _ := findPayload[*event.StageFailed](evs)
	if failed == nil || failed.StageID != "push" || !strings.Contains(failed.Error, "comes from a fork") {
		t.Errorf("failed = %+v", failed)
	}
	next, err := ph.e.Rerun(context.Background(), runID, RunInput{})
	if err != nil {
		t.Fatal(err)
	}
	again, _ := findPayload[*event.RunCreated](ph.events(t, next))
	if again == nil || again.MR == nil || again.MR.IID != ref.IID {
		t.Errorf("rerun = %+v", again)
	}
}

// The built-in reviewer template runs as written: created on a merge
// request, it reviews it through its block, waits, and ends with it.
func TestBuiltinReviewerTemplateRuns(t *testing.T) {
	src, ok := builtin.FlowYAML("reviewer")
	if !ok {
		t.Fatal("no built-in reviewer")
	}
	src = strings.NewReplacer("harness: opencode", "harness: fake", "model: opencode-go/glm-5.2", "model: anthropic/claude-sonnet-4-6").Replace(src)
	ph, _ := newReviewerHarness(t, src)
	ref := ph.forge.AddOpenMR("alice", "Add caching", "feature/cache", "main", "a1")
	runID, err := ph.e.CreateRunWith(context.Background(), Ticket{Title: "Add caching (!1 by alice)"}, "reviewer", testProject, RunInput{MR: &RunMR{IID: ref.IID}})
	if err != nil {
		t.Fatal(err)
	}
	ph.waitState(t, runID, run.InReview)
	evs := ph.events(t, runID)
	var stages []string
	for _, e := range evs {
		if s, ok := e.Payload.(*event.StageCompleted); ok {
			stages = append(stages, s.StageID)
		}
	}
	// The gate is off by default: skipped, the review posted.
	if got := strings.Join(stages, " "); got != "review.read review.gate review.post" {
		t.Errorf("stages completed: %s", got)
	}
	if rv := ph.forge.SnapshotMRs()[0].Reviews; len(rv) != 1 {
		t.Fatalf("reviews = %+v", rv)
	}
	ph.forge.SetMRState(ref.IID, ports.MRClosed)
	ph.waitState(t, runID, run.Completed)
}
