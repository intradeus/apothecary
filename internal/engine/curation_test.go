package engine

// TODO 45: a reviewer that reads what is on the merge request before it
// writes and never repeats itself; a gate where a person picks and rewords
// what gets posted, whose "no" sticks; replies that answer every thread, held
// back one by one at a gate.

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/flow/reviewfile"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
	"apothecary/internal/runcore"
	"apothecary/internal/runinput"
)

// curatedReviewerDoc is the reviewer template's shape, small: poll the
// threads, read, a gate when $review_gate says so, post — as the merge
// request was opened and after every push.
const curatedReviewerDoc = `name: cur
version: 5
operates_on: merge_request
parameters: { harness: fake, model: fake/model, review_gate: { type: bool, default: GATE } }
agents:
  - name: pr_reviewer
    prompt: review it
    inputs:
      - { name: changes, type: changeset }
      - { name: threads, type: mr_threads }
      - { name: rejection, type: verdict, optional: true }
      - { name: failure, type: failure, optional: true }
    outputs:
      - { name: review, type: review_threads }
sequences:
  - id: review
    stages:
      - { id: poll, type: action, action: load_review_comments, with: { scope: all }, output: { threads: { to: artifacts/threads.yaml, type: mr_threads } } }
      - { id: read, type: agent, harness: $harness, model: $model, agent: pr_reviewer,
          input: { changes: { from: run.git.diff_target, type: changeset }, threads: { from: artifacts/threads.yaml, type: mr_threads },
                   rejection: { from: edge.verdict, type: verdict, optional: true }, failure: { from: edge.failure, type: failure, optional: true } },
          output: { review: { to: artifacts/review.yaml, type: review_threads } }, on_fail: { to: read, max: 1 } }
      - { id: gate, type: human_gate, run_if: $review_gate, question: "post?",
          input: { review: { from: artifacts/review.yaml, type: review_threads }, threads: { from: artifacts/threads.yaml, type: mr_threads } },
          on_reject: { to: read, max: -1 } }
      - { id: post, type: action, action: post_review, input: { review: { from: artifacts/review.yaml, type: review_threads } } }
main:
  - { id: review, sequence: review }
watchers:
  - id: again
    on: mr_head_moved
    stages:
      - { id: rereview, sequence: review }
outcomes:
  - { id: merged, on: mr_merged, then: done }
  - { id: closed, on: mr_closed, then: done }
`

// curatedReviewer runs the doc with the reviewer scripted call by call (the
// threads file it was given in hand) and a snapshot store, so gates pin what
// they show.
func curatedReviewer(t *testing.T, gate bool, script func(call int, threads *reviewfile.Threads) string) (*phasedHarness, repoport.MRRef, string) {
	t.Helper()
	doc := strings.Replace(curatedReviewerDoc, "GATE", map[bool]string{true: "true", false: "false"}[gate], 1)
	att, snaps, _ := newFiles(t)
	ph := newPhasedHarnessWith(t, doc, func(_ *harness, d *Deps) { d.Attachments, d.Snapshots = att, snaps })
	var calls atomic.Int32
	inner := ph.runner.RunFunc
	ph.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		if call.Spec.Agent == "pr_reviewer" {
			var threads *reviewfile.Threads
			if data, err := ph.envp.ReadFile(ctx, call.Env, "artifacts/threads.yaml"); err == nil {
				threads, _ = reviewfile.ParseThreads("artifacts/threads.yaml", data)
			}
			ph.writeFile(t, call.Env, "artifacts/review.yaml", script(int(calls.Add(1)), threads))
		}
		return inner(ctx, call)
	}
	ref := ph.repoHost.AddOpenMR("alice", "Add caching", "feature/cache", "main", "a1")
	runID, err := ph.e.CreateRunWith(context.Background(), runinput.Ticket{Title: "Add caching"}, "cur", testProject, runinput.Input{MR: &runinput.MR{IID: ref.IID}})
	if err != nil {
		t.Fatal(err)
	}
	return ph, ref, runID
}

const noteA = "notes:\n  - { path: a.go, line: 3, kind: nit, body: name it, rationale: style }\n"

// ourThread is the factory's open thread on a path among the threads loaded.
func ourThread(threads *reviewfile.Threads, path string) *reviewfile.Thread {
	if threads == nil {
		return nil
	}
	for i := range threads.Threads {
		if th := &threads.Threads[i]; th.Ours && !th.Resolved && th.Path == path {
			return th
		}
	}
	return nil
}

// failuresOf lists one stage's failures, in order.
func failuresOf(evs []event.Event, stage string) []*event.StageFailed {
	var out []*event.StageFailed
	for _, e := range evs {
		if f, ok := e.Payload.(*event.StageFailed); ok && f.StageID == stage {
			out = append(out, f)
		}
	}
	return out
}

// A re-review that proposes the note it already posted is refused — the
// thread is still open on the merge request — and told to continue the
// thread; the retry does, closing it: one review, one reply, no duplicate.
func TestReReviewContinuesItsThreadInsteadOfRepeatingIt(t *testing.T) {
	ph, ref, runID := curatedReviewer(t, false, func(call int, threads *reviewfile.Threads) string {
		switch call {
		case 1, 2:
			return noteA // the second time: the same note, on the same line
		}
		th := ourThread(threads, "a.go")
		if th == nil {
			return noteA
		}
		return "replies:\n  - { thread: " + th.ID + ", body: Addressed in a2, resolve: true }\n"
	})
	ph.waitState(t, runID, run.InReview)
	if th := ph.repoHost.SnapshotThreads(ref.IID); len(th) != 1 || !repoport.IsFactoryReply(th[0].Comments[0].Body) || repoport.MarkerAttr(th[0].Comments[0].Body, "kind") != "nit" {
		t.Fatalf("threads after the first review = %+v", th)
	}
	ph.repoHost.SetHead(ref.IID, "a2")
	ph.waitPhaseRuns(t, runID, "again", 1)
	evs := ph.events(t, runID)
	fails := failuresOf(evs, "rereview.read")
	if len(fails) != 1 || fails[0].Reason != event.ReasonOutputInvalid || !strings.Contains(fails[0].Error, "already carries apothecary's thread") || !strings.Contains(fails[0].Error, "continue it under replies") {
		t.Fatalf("the repeated note was not refused as one: %+v", fails)
	}
	if rv := ph.repoHost.SnapshotMRs()[0].Reviews; len(rv) != 1 {
		t.Errorf("reviews = %+v, want the first only", rv)
	}
	th := ph.repoHost.SnapshotThreads(ref.IID)
	if len(th) != 1 || !th[0].Resolved || len(th[0].Comments) != 2 || !strings.Contains(th[0].Comments[1].Body, "Addressed in a2") {
		t.Fatalf("threads after the re-review = %+v", th)
	}
	// The retry read why, in its failure input (a subject phase's prompts
	// are filed under "fix" by the harness).
	if p := ph.promptsOf("fix"); len(p) != 3 || !strings.Contains(p[2], "continue it under replies") {
		t.Errorf("the retry was not told what to do instead (%d prompts)", len(p))
	}
}

// An open gate still curates after a restart: the ping rebuilt from the log
// knows which of its files are reviews.
func TestAGateCuratesAfterARestart(t *testing.T) {
	ph, _, runID := curatedReviewer(t, true, func(int, *reviewfile.Threads) string {
		return "summary: Two things.\nnotes:\n" +
			"  - { path: a.go, line: 3, kind: nit, body: name it, rationale: style }\n" +
			"  - { path: b.go, line: 9, kind: nit, body: rename it, rationale: style }\n"
	})
	g := ph.waitPing(t, event.PingGate)
	waitCaptured(t, ph.harness, runID, g.ID)
	snaps := ph.e.snapshots
	ph.e.Shutdown()

	h2 := newHarnessSource(t, ph.flows, ph.flowOf(t, "cur"))
	h2.store, h2.envp, h2.repoHost = ph.store, ph.envp, ph.repoHost
	h2.res.Envp, h2.res.RepoHost = ph.envp, ph.repoHost
	h2.runner.RunFunc = ph.runner.RunFunc
	h2.res.Runner = h2.runner
	h2.e = New(Deps{Config: Config{MRPollInterval: 5 * time.Millisecond}, Store: ph.store, Resolver: resolution.StaticResolver{R: h2.res},
		Flows: ph.flows, Settings: h2.settings, Snapshots: snaps})
	t.Cleanup(h2.e.Shutdown)
	if err := h2.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h2.e.AnswerPing(g.ID, PingAnswer{Answer: "approve", Curated: map[string]reviewfile.Curation{
		"artifacts/review.yaml": {Items: []reviewfile.CuratedItem{{Item: "notes/1", Keep: false}}},
	}}); err != nil {
		t.Fatal(err)
	}
}

// At the gate a person keeps two of three notes, rewords one and the
// summary: that is what is posted, and the dropped note is on the record —
// the next re-review is handed it, and refused when it proposes it anyway.
func TestGateCurationPostsWhatThePersonKept(t *testing.T) {
	three := "summary: Three things.\nnotes:\n" +
		"  - { path: a.go, line: 3, kind: blocking, body: handle the error, rationale: it is dropped, evidence: ran the tests }\n" +
		"  - { path: b.go, line: 9, kind: nit, body: name it, rationale: style }\n" +
		"  - { path: c.go, line: 1, kind: suggestion, body: use a slice, rationale: fewer allocations, example: \"xs := make([]T, 0, n)\" }\n"
	ph, ref, runID := curatedReviewer(t, true, func(call int, threads *reviewfile.Threads) string {
		switch call {
		case 1:
			return three
		case 2:
			// The re-review proposes the dropped note again.
			return "summary: Again.\nnotes:\n  - { path: b.go, line: 9, kind: nit, body: Name it, rationale: style }\n"
		}
		return "summary: Nothing new.\n"
	})
	g := ph.waitPing(t, event.PingGate)
	waitCaptured(t, ph.harness, runID, g.ID)
	if g.FileTypes["artifacts/review.yaml"] != agentspec.TypeReviewThreads || g.FileTypes["artifacts/threads.yaml"] != agentspec.TypeMRThreads {
		t.Fatalf("the gate types its files: %v", g.FileTypes)
	}
	yes := true
	for _, bad := range []struct {
		name string
		c    map[string]reviewfile.Curation
		want string
	}{
		{"a file that is not a review", map[string]reviewfile.Curation{"artifacts/threads.yaml": {}}, "is not a review this question curates"},
		{"a bad key", map[string]reviewfile.Curation{"artifacts/review.yaml": {Items: []reviewfile.CuratedItem{{Item: "x/1", Keep: true}}}}, "not notes/<i>"},
		{"an item not shown", map[string]reviewfile.Curation{"artifacts/review.yaml": {Items: []reviewfile.CuratedItem{{Item: "notes/3", Keep: true}}}}, "no such item"},
		{"resolving a note", map[string]reviewfile.Curation{"artifacts/review.yaml": {Items: []reviewfile.CuratedItem{{Item: "notes/0", Keep: true, Resolve: &yes}}}}, "only a reply resolves"},
	} {
		if err := ph.e.AnswerPing(g.ID, PingAnswer{Answer: "approve", Curated: bad.c}); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: err = %v, want %q", bad.name, err, bad.want)
		}
	}
	short := "Two things."
	if err := ph.e.AnswerPing(g.ID, PingAnswer{Answer: "approve", Curated: map[string]reviewfile.Curation{
		"artifacts/review.yaml": {Summary: &short, Items: []reviewfile.CuratedItem{
			{Item: "notes/1", Keep: false},
			{Item: "notes/2", Keep: true, Body: "Use a slice here."},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	// Not waitState(in_review): the answered gate reads in_review a moment
	// before its phase resumes.
	ph.waitPhaseCompleted(t, runID, flow.MainPhase, 1)
	rv := ph.repoHost.SnapshotMRs()[0].Reviews
	if len(rv) != 1 || rv[0].Summary != "Two things." || len(rv[0].Notes) != 2 || rv[0].Notes[0].Path != "a.go" ||
		rv[0].Notes[1].Path != "c.go" || rv[0].Notes[1].Body != "[suggestion] Use a slice here." || rv[0].Notes[0].Kind != "blocking" {
		t.Fatalf("posted = %+v", rv)
	}
	evs := ph.events(t, runID)
	cur, _ := findPayload[*event.ReviewCurated](evs)
	if cur == nil || !cur.Approved || cur.Path != "artifacts/review.yaml" || cur.Type != agentspec.TypeReviewThreads ||
		len(cur.Dropped) != 1 || cur.Dropped[0].Path != "b.go" || strings.Join(cur.Rewritten, ",") != "summary,notes/2" ||
		cur.Review == nil || len(cur.Review.Notes) != 2 {
		t.Fatalf("review_curated = %+v", cur)
	}
	// The record carries the decisions, and the file read by post was the
	// curated one.
	for _, p := range run.ReducePings(evs) {
		if p.PingID == g.ID && (p.Curated["artifacts/review.yaml"].Summary == nil || len(p.Curated["artifacts/review.yaml"].Items) != 2) {
			t.Errorf("recorded = %+v", p.Curated)
		}
	}
	ready, _ := findPayload[*event.EnvReady](evs)
	data, err := ph.envp.ReadFile(context.Background(), envport.EnvID(ready.EnvID), "artifacts/review.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if written, err := reviewfile.ParseReview("artifacts/review.yaml", data); err != nil || len(written.Notes) != 2 || written.Summary != "Two things." {
		t.Errorf("the file post read = %s (%v)", data, err)
	}

	// The author pushes: the re-review is handed the dropped note and refused
	// when it proposes it anyway; its retry posts a summary only.
	ph.repoHost.SetHead(ref.IID, "a2")
	g2 := ph.waitPing(t, event.PingGate, g.ID)
	data, _ = ph.envp.ReadFile(context.Background(), envport.EnvID(ready.EnvID), "artifacts/threads.yaml")
	if th, err := reviewfile.ParseThreads("artifacts/threads.yaml", data); err != nil || len(th.Dropped) != 1 || th.Dropped[0].Path != "b.go" || len(th.Threads) != 2 {
		t.Errorf("the re-review's threads file = %s (%v)", data, err)
	}
	fails := failuresOf(ph.events(t, runID), "rereview.read")
	if len(fails) != 1 || !strings.Contains(fails[0].Error, "a person decided not to post this note (b.go:9)") {
		t.Errorf("the dropped note was proposed again without refusal: %+v", fails)
	}
	if err := ph.e.AnswerPing(g2.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	ph.waitPhaseRuns(t, runID, "again", 1)
	if rv := ph.repoHost.SnapshotMRs()[0].Reviews; len(rv) != 2 || rv[1].Summary != "Nothing new." || len(rv[1].Notes) != 0 {
		t.Errorf("reviews after the push = %+v", rv)
	}
}

// A rejection with a curation posts nothing and leaves the file alone, but
// the redo reads what the person dropped.
func TestARejectedCurationReachesTheRedo(t *testing.T) {
	ph, _, runID := curatedReviewer(t, true, func(call int, threads *reviewfile.Threads) string {
		if call == 1 {
			return noteA + "  - { path: b.go, line: 9, kind: nit, body: and that, rationale: style }\n"
		}
		return "summary: Redone.\n"
	})
	g := ph.waitPing(t, event.PingGate)
	waitCaptured(t, ph.harness, runID, g.ID)
	if err := ph.e.AnswerPing(g.ID, PingAnswer{Answer: "reject", Feedback: "Too picky.", Curated: map[string]reviewfile.Curation{
		"artifacts/review.yaml": {Items: []reviewfile.CuratedItem{{Item: "notes/1", Keep: false}}},
	}}); err != nil {
		t.Fatal(err)
	}
	g2 := ph.waitPing(t, event.PingGate, g.ID)
	p := ph.promptsOf("fix")
	if len(p) != 2 || !strings.Contains(p[1], "REJECTED at gate review.gate.\n\nToo picky.") || !strings.Contains(p[1], "dropped, do not propose again: b.go:9 [nit] and that") {
		t.Fatalf("the redo reads:\n%s", p[len(p)-1])
	}
	cur, _ := findPayload[*event.ReviewCurated](ph.events(t, runID))
	if cur == nil || cur.Approved || len(cur.Dropped) != 1 {
		t.Errorf("review_curated = %+v", cur)
	}
	if err := ph.e.AnswerPing(g2.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	ph.waitPhaseCompleted(t, runID, flow.MainPhase, 1)
	if rv := ph.repoHost.SnapshotMRs()[0].Reviews; len(rv) != 1 || rv[0].Summary != "Redone." {
		t.Errorf("reviews = %+v", rv)
	}
}

// replyGateDoc is the watcher flow with a gate before the replies are
// posted, the replier reading the gate's rejection.
func replyGateDoc() string {
	doc := strings.Replace(watcherFlowDoc, "STRATEGY", "merge", 1)
	doc = strings.Replace(doc,
		"      - { name: comments, type: mr_threads }\n      - { name: failure, type: failure, optional: true }\n    outputs:\n      - { name: replies, type: review_replies }",
		"      - { name: comments, type: mr_threads }\n      - { name: rejection, type: verdict, optional: true }\n      - { name: failure, type: failure, optional: true }\n    outputs:\n      - { name: replies, type: review_replies }", 1)
	doc = strings.Replace(doc,
		"input: { comments: { from: artifacts/threads.yaml, type: mr_threads }, failure: { from: edge.failure, type: failure, optional: true } },",
		"input: { comments: { from: artifacts/threads.yaml, type: mr_threads }, rejection: { from: edge.verdict, type: verdict, optional: true }, failure: { from: edge.failure, type: failure, optional: true } },", 1)
	doc = strings.Replace(doc,
		"      - { id: post, type: action, action: reply_review_comments, input: { replies: { from: artifacts/replies.yaml, type: review_replies } } }",
		"      - { id: reply_gate, type: human_gate, question: \"send?\", input: { replies: { from: artifacts/replies.yaml, type: review_replies }, threads: { from: artifacts/threads.yaml, type: mr_threads } }, on_reject: { to: reply, max: -1 } }\n"+
			"      - { id: post, type: action, action: reply_review_comments, input: { replies: { from: artifacts/replies.yaml, type: review_replies } } }", 1)
	return doc
}

// At the reply gate a person holds one reply back and rewords the other:
// the thread held back is left alone until someone writes there again.
func TestReplyGateHoldsBackAReplyAndSkipsItsThread(t *testing.T) {
	att, snaps, _ := newFiles(t)
	rh := newWatcherHarnessDocWith(t, replyGateDoc(), func(_ *harness, d *Deps) { d.Attachments, d.Snapshots = att, snaps })
	rh.replies = func() string {
		return "- { thread: d1, did: fixed, reply: one }\n- { thread: d2, did: answered, reply: two }\n"
	}
	runID := rh.start(t)
	t0 := rh.clock.Now()
	rh.repoHost.AddComment(1, "d1", "a.go", "rev", "rename this", t0)
	rh.repoHost.AddComment(1, "d2", "b.go", "rev", "why this way?", t0)
	rh.clock.Advance(2 * time.Minute)
	rh.pick(t, "d1", "d2")
	g := rh.waitPing(t, event.PingGate)
	waitCaptured(t, rh.harness, runID, g.ID)
	if g.FileTypes["artifacts/replies.yaml"] != agentspec.TypeReviewReplies || g.FileTypes["artifacts/threads.yaml"] != agentspec.TypeMRThreads {
		t.Fatalf("the gate types its files: %v", g.FileTypes)
	}
	s := "x"
	if err := rh.e.AnswerPing(g.ID, PingAnswer{Answer: "approve", Curated: map[string]reviewfile.Curation{"artifacts/replies.yaml": {Summary: &s}}}); err == nil || !strings.Contains(err.Error(), "no summary") {
		t.Errorf("a summary on replies: %v", err)
	}
	if err := rh.e.AnswerPing(g.ID, PingAnswer{Answer: "approve", Curated: map[string]reviewfile.Curation{
		"artifacts/replies.yaml": {Items: []reviewfile.CuratedItem{{Item: "replies/1", Keep: false}, {Item: "replies/0", Keep: true, Body: "one, really"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	rh.waitPhaseRuns(t, runID, "review_fixes", 1)
	for _, th := range rh.repoHost.SnapshotThreads(1) {
		last := th.Comments[len(th.Comments)-1]
		switch th.ID {
		case "d1":
			if len(th.Comments) != 2 || !strings.HasPrefix(last.Body, "one, really") || repoport.MarkerAttr(last.Body, "did") != "fixed" {
				t.Errorf("d1 = %+v", th.Comments)
			}
		case "d2":
			if len(th.Comments) != 1 {
				t.Errorf("d2 got a reply that was held back: %+v", th.Comments)
			}
		}
	}
	evs := rh.events(t, runID)
	cur, _ := findPayload[*event.ReviewCurated](evs)
	if cur == nil || cur.Type != agentspec.TypeReviewReplies || strings.Join(cur.DroppedReplies, ",") != "d2" || cur.Skipped["d2"] == "" || len(cur.Replies) != 1 {
		t.Fatalf("review_curated = %+v", cur)
	}
	posted, _ := findPayload[*event.ReviewRepliesPosted](evs)
	if posted == nil || strings.Join(posted.ThreadIDs, ",") != "d1" {
		t.Errorf("review_replies_posted = %+v", posted)
	}
	// d2 is left alone: nothing new, no new round.
	rh.clock.Advance(10 * time.Minute)
	time.Sleep(60 * time.Millisecond)
	if n := len(phaseStarts(rh.events(t, runID))); n != 2 {
		t.Errorf("the held-back thread started another round (%d phase starts)", n)
	}
	// Until the reviewer writes there again.
	rh.repoHost.AddComment(1, "d2", "b.go", "rev", "still asking", rh.clock.Now())
	rh.clock.Advance(2 * time.Minute)
	if offered := rh.pick(t); len(offered) != 1 || !strings.HasPrefix(offered[0], "d2 — ") {
		t.Errorf("after a new comment the round offers %q, want d2", offered)
	}
}

// Every thread a round loaded gets a reply — the replier that leaves one
// out is retried with the thread named — and a comment judged wrong is
// answered with did: declined, which the next poll reads off the marker.
func TestRepliesAnswerEveryThreadDeclinedIncluded(t *testing.T) {
	rh := newWatcherHarness(t, "merge")
	var calls int
	rh.replies = func() string {
		calls++
		if calls == 1 {
			return "- { thread: d1, did: fixed, reply: done }\n"
		}
		return "- { thread: d1, did: fixed, reply: done }\n- { thread: d2, did: declined, reply: \"The test covers it already.\", rationale: see widgets_test.go }\n"
	}
	runID := rh.start(t)
	rh.repoHost.AddComment(1, "d1", "a.go", "rev", "rename", rh.clock.Now())
	rh.repoHost.AddComment(1, "d2", "b.go", "rev", "needs a test", rh.clock.Now())
	rh.clock.Advance(2 * time.Minute)
	rh.pick(t, "d1", "d2")
	rh.waitPhaseRuns(t, runID, "review_fixes", 1)
	evs := rh.events(t, runID)
	fails := failuresOf(evs, "reply")
	if len(fails) != 1 || fails[0].Reason != event.ReasonOutputInvalid || !strings.Contains(fails[0].Error, "no reply for thread(s) d2") {
		t.Fatalf("the incomplete replies were not refused: %+v", fails)
	}
	posted, _ := findPayload[*event.ReviewRepliesPosted](evs)
	if posted == nil || len(posted.ThreadIDs) != 2 || strings.Join(posted.Declined, ",") != "d2" {
		t.Errorf("review_replies_posted = %+v", posted)
	}
	for _, th := range rh.repoHost.SnapshotThreads(1) {
		if th.ID == "d2" {
			last := th.Comments[len(th.Comments)-1]
			if !repoport.IsFactoryReply(last.Body) || repoport.MarkerAttr(last.Body, "did") != "declined" || th.Resolved {
				t.Errorf("d2 = %+v", th)
			}
		}
	}
}

// What a gate decided is replayed: a dropped note stays dropped for the
// run, an approved document waits to be written back until a stage that
// writes its path starts.
func TestCurationReplays(t *testing.T) {
	fl := mustFlow(t, strings.Replace(curatedReviewerDoc, "GATE", "true", 1))
	kept := &reviewfile.Review{Summary: "Two.", Notes: []reviewfile.Note{{Path: "a.go", Line: 3, Kind: "nit", Body: "x", Rationale: "y"}}}
	evs := []event.Event{
		{Payload: &event.RunCreated{TicketTitle: "T"}},
		{Payload: &event.PhaseStarted{Phase: flow.MainPhase, Subject: "repo"}},
		{Payload: &event.StageStarted{StageID: "review.gate", Iteration: 1}},
		{Payload: &event.PingRaised{PingID: "g1", Type: event.PingGate, StageID: "review.gate", Files: []string{"artifacts/review.yaml"},
			FileTypes: map[string]string{"artifacts/review.yaml": agentspec.TypeReviewThreads}}},
		{Payload: &event.PingAnswered{PingID: "g1", Answer: "approve", Curated: map[string]reviewfile.Curation{
			"artifacts/review.yaml": {Items: []reviewfile.CuratedItem{{Item: "notes/1", Keep: false}}}}}},
		{Payload: &event.ReviewCurated{StageID: "review.gate", PingID: "g1", Path: "artifacts/review.yaml", Type: agentspec.TypeReviewThreads,
			Approved: true, Review: kept, Dropped: []reviewfile.Note{{Path: "b.go", Line: 9, Kind: "nit", Body: "and that", Rationale: "y"}}}},
	}
	rs := runcore.Replay(evs, fl)
	if !rs.CuratedSeen["g1"] || rs.Curated["artifacts/review.yaml"] == nil || len(rs.Dropped) != 1 || rs.Dropped[0].Path != "b.go" {
		t.Fatalf("after the curation: seen %v curated %v dropped %v", rs.CuratedSeen, rs.Curated, rs.Dropped)
	}
	// A reader starting keeps it waiting; a writer starting drops it.
	if rs := runcore.Replay(append(evs, event.Event{Payload: &event.StageStarted{StageID: "review.post", Iteration: 1}}), fl); rs.Curated["artifacts/review.yaml"] == nil {
		t.Error("the stage that reads the path forgot the curation")
	}
	if rs := runcore.Replay(append(evs, event.Event{Payload: &event.StageStarted{StageID: "review.read", Iteration: 2}}), fl); rs.Curated["artifacts/review.yaml"] != nil || len(rs.Dropped) != 1 {
		t.Error("the stage that writes the path kept the curation, or the drop was forgotten")
	}
	// A rejection's curation records the drop and nothing to write.
	rejected := append(evs[:4:4], event.Event{Payload: &event.ReviewCurated{StageID: "review.gate", PingID: "g1", Path: "artifacts/review.yaml",
		Type: agentspec.TypeReviewThreads, Dropped: []reviewfile.Note{{Path: "b.go", Line: 9, Kind: "nit", Body: "and that"}}}})
	if rs := runcore.Replay(rejected, fl); len(rs.Curated) != 0 || len(rs.Dropped) != 1 {
		t.Errorf("a rejected curation: curated %v dropped %v", rs.Curated, rs.Dropped)
	}
}

// waitCaptured waits for a gate's files to be captured: a curation is
// checked against what the gate showed, which exists only once captured.
func waitCaptured(t *testing.T, h *harness, runID, pingID string) {
	t.Helper()
	waitUntil(t, "the gate's files to be captured", func() bool {
		for _, ev := range h.events(t, runID) {
			if c, ok := ev.Payload.(*event.SnapshotCaptured); ok && c.PingID == pingID {
				return true
			}
		}
		return false
	})
}
