package kinds

import (
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/reviewfile"
)

// The dev fake writes, for each file the OUTPUT CONTRACT names, a document
// that holds its type's shape — found by the type the contract prints
// beside the role, wherever the stage binds the path — and a stub for the
// rest.
func TestDevFakeWritesWhatTheContractAsks(t *testing.T) {
	prompt := "# INPUT: comments — the threads (file: artifacts/threads.yaml)\n" +
		"merge_request: https://x/mr/1\nthreads:\n  - id: d1\n    resolved: false\n    ours: false\n    comments:\n      - { id: c1, author: rev, body: rename }\n  - id: d2\n    resolved: false\n    ours: false\n    comments: []\n\n" +
		"# OUTPUT CONTRACT\nBefore finishing you MUST write these files (relative to repo root):\n" +
		"- artifacts/plan.md — plan: the plan\n" +
		"- artifacts/review/verdict.yaml — verdict (verdict): APPROVE or REJECT\n" +
		"- artifacts/review.yaml — review (review_threads): the review\n" +
		"- artifacts/replies.yaml — replies (review_replies)\n" +
		"- artifacts/commit.json — message (commit_message): the commit\n" +
		"artifacts/review/verdict.yaml is your verdict: APPROVE or REJECT.\n"
	outs := declaredOutputs(prompt)
	if len(outs) != 5 || outs[0] != (declaredOutput{path: "artifacts/plan.md"}) || outs[1] != (declaredOutput{path: "artifacts/review/verdict.yaml", typ: "verdict"}) ||
		outs[2].typ != "review_threads" || outs[3].typ != "review_replies" || outs[4] != (declaredOutput{path: "artifacts/commit.json", typ: "commit_message"}) {
		t.Fatalf("declared = %+v", outs)
	}
	call := fake.RunCall{Spec: agentport.AgentSpec{Agent: "dev"}, In: agentport.AgentInput{Prompt: prompt}}
	got := map[string]string{}
	for _, out := range outs {
		b, err := stubOutput(out, call)
		if err != nil {
			t.Fatal(err)
		}
		got[out.path] = string(b)
	}
	if !strings.HasPrefix(got["artifacts/plan.md"], "# Dev stub") || !strings.Contains(got["artifacts/review/verdict.yaml"], "verdict: APPROVE") ||
		!strings.HasPrefix(got["artifacts/commit.json"], "{") {
		t.Errorf("stubs = %q", got)
	}
	if rev, err := reviewfile.ParseReview("artifacts/review.yaml", []byte(got["artifacts/review.yaml"])); err != nil || rev.Summary == "" || len(rev.Notes) != 0 {
		t.Errorf("review stub = %q (%v)", got["artifacts/review.yaml"], err)
	}
	replies, err := reviewfile.ParseReplies("artifacts/replies.yaml", []byte(got["artifacts/replies.yaml"]))
	if err != nil || len(replies) != 2 || replies[0].Thread != "d1" || replies[1].Thread != "d2" || replies[0].Did != reviewfile.DidAnswered {
		t.Errorf("replies stub = %q (%v)", got["artifacts/replies.yaml"], err)
	}
	if err := reviewfile.CheckReplies(replies, []string{"d1", "d2"}); err != nil {
		t.Errorf("the replies stub answers every thread: %v", err)
	}
}
