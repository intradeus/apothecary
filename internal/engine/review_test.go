package engine

// Reviews: a gate shows several files — a folder's each — and its answer
// carries the person's comments, file by file, as the decision: to
// on_reject, and to on_success when the approval said something.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/filestore"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

const reviewFlowDoc = `name: rv
version: 5
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
agents:
  - name: writer
    prompt: write the plan and take the shots
    inputs:
      - { name: review, type: verdict, optional: true }
    outputs:
      - { name: plan, type: file }
      - { name: shots, type: files }
  - name: builder
    prompt: build it
    inputs:
      - { name: review, type: verdict, optional: true }
main:
  - { id: write, type: agent, agent: writer, input: { review: { from: edge.verdict, type: verdict, optional: true } },
      output: { plan: { to: artifacts/plan.md, type: file }, shots: { to: artifacts/shots/, type: files } } }
  - { id: gate, type: human_gate, question: "ok?",
      input: { plan: { from: artifacts/plan.md, type: file }, shots: { from: artifacts/shots/, type: files } },
      on_reject: { to: write, max: 1 } }
  - { id: build, type: agent, agent: builder, input: { review: { from: edge.verdict, type: verdict, optional: true } } }
`

type reviewRun struct {
	h       *harness
	att     *filestore.Attachments
	snaps   *filestore.Snapshots
	mu      sync.Mutex
	prompts map[string][]string
}

func newReviewRun(t *testing.T) *reviewRun {
	rr := &reviewRun{h: newHarness(t, mustFlow(t, reviewFlowDoc)), prompts: map[string][]string{}}
	rr.att, rr.snaps, _ = newFiles(t)
	rr.h.e.SetFiles(rr.att, rr.snaps)
	record := func(agent string, c fake.RunCall) {
		rr.mu.Lock()
		rr.prompts[agent] = append(rr.prompts[agent], c.In.Prompt)
		rr.mu.Unlock()
	}
	rr.h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"writer": func(c fake.RunCall) (ports.AgentResult, error) {
			record("writer", c)
			rr.h.writeFile(t, c.Env, "artifacts/plan.md", "# Plan\n\nStep one.\nStep two.\n")
			rr.h.writeFile(t, c.Env, "artifacts/shots/home.png", png)
			rr.h.writeFile(t, c.Env, "artifacts/shots/login.png", png)
			return ports.AgentResult{}, nil
		},
		"builder": func(c fake.RunCall) (ports.AgentResult, error) {
			record("builder", c)
			return ports.AgentResult{}, nil
		},
	})
	return rr
}

func (rr *reviewRun) promptsOf(agent string) []string {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return append([]string(nil), rr.prompts[agent]...)
}

// attach uploads a file and claims it for the ping's answer, as the API does.
func (rr *reviewRun) attach(t *testing.T, runID, pingID, name string) string {
	t.Helper()
	up, err := rr.att.Save(context.Background(), name, "text/plain", strings.NewReader("note"))
	if err != nil {
		t.Fatal(err)
	}
	if err := rr.att.ClaimForRun(context.Background(), []string{up.ID}, runID, pingID); err != nil {
		t.Fatal(err)
	}
	return up.ID
}

func TestGateReviewsAFolderAndCarriesTheReview(t *testing.T) {
	rr := newReviewRun(t)
	h := rr.h
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "rv", testProject)
	if err != nil {
		t.Fatal(err)
	}
	g := h.waitPing(t, event.PingGate)
	want := []string{"artifacts/plan.md", "artifacts/shots/home.png", "artifacts/shots/login.png"}
	if strings.Join(g.Files, ",") != strings.Join(want, ",") {
		t.Fatalf("the gate shows %v", g.Files)
	}
	var shown []string
	for _, sn := range snapshotsFor(t, rr.snaps, runID) {
		if sn.Kind == filestore.KindPingFile && sn.PingID == g.ID {
			shown = append(shown, sn.Path)
		}
	}
	if len(shown) != 3 {
		t.Errorf("snapshots of what the gate shows: %v", shown)
	}

	refused := []struct {
		name string
		c    event.ReviewComment
		want string
	}{
		{"a file not shown", event.ReviewComment{Path: "artifacts/other.md", Body: "x"}, "is not one of the files this question shows"},
		{"lines on an image", event.ReviewComment{Path: "artifacts/shots/home.png", StartLine: 1, EndLine: 1, Body: "x"}, "is an image"},
		{"an empty comment", event.ReviewComment{Path: "artifacts/plan.md", Body: "  "}, "the comment is empty"},
		{"a backwards range", event.ReviewComment{Path: "artifacts/plan.md", StartLine: 4, EndLine: 3, Body: "x"}, "are not a range"},
		{"an attachment not on the answer", event.ReviewComment{Path: "artifacts/plan.md", Body: "x", AttachmentIDs: []string{"nope"}}, "is not attached to the answer"},
	}
	for _, c := range refused {
		err := h.e.AnswerPing(g.ID, PingAnswer{Answer: "reject", Review: []event.ReviewComment{c.c}})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}

	onStep := rr.attach(t, runID, g.ID, "step.txt")
	general := rr.attach(t, runID, g.ID, "general.txt")
	if err := h.e.AnswerPing(g.ID, PingAnswer{
		Answer: "reject", Feedback: "Close, two things.", AttachmentIDs: []string{onStep, general},
		Review: []event.ReviewComment{
			{Path: "artifacts/shots/home.png", Body: "the header is cut off"},
			{Path: "artifacts/plan.md", StartLine: 3, EndLine: 4, Heading: "Plan", Quote: "Step one.\nStep two.", Body: "say what each step changes", AttachmentIDs: []string{onStep}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Approved the second time, with a word for the builder.
	g2 := h.waitPing(t, event.PingGate, g.ID)
	if err := h.e.AnswerPing(g2.ID, PingAnswer{Answer: "approve", Review: []event.ReviewComment{
		{Path: "artifacts/plan.md", Body: "keep step two small"},
	}}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events %v", v.State, eventTypes(h.events(t, runID)))
	}

	writes := rr.promptsOf("writer")
	if len(writes) != 2 {
		t.Fatalf("writer ran %d times", len(writes))
	}
	attDir := ".apothecary/attachments/ping-" + g.ID + "/"
	for _, part := range []string{
		"REJECTED at gate gate.\n\nClose, two things.\n\n2 comments, on 2 files:\n",
		// In the order the gate showed the files, whatever order they were made in.
		"\nartifacts/plan.md\n1. [§ Plan, lines 3–4]\n   > Step one.\n   > Step two.\n   say what each step changes\n   attached: " + attDir + onStep[:8] + "/step.txt\n",
		"\nartifacts/shots/home.png\n2. [the whole image]\n   the header is cut off\n",
		// What no comment names is listed after them.
		"read them with your tools:\n- " + attDir + general[:8] + "/general.txt\n",
	} {
		if !strings.Contains(writes[1], part) {
			t.Errorf("the rejection the writer reads lacks %q:\n%s", part, writes[1])
		}
	}
	if strings.Contains(writes[0], "# INPUT: review") {
		t.Errorf("the first attempt has no decision to read:\n%s", writes[0])
	}
	builds := rr.promptsOf("builder")
	if len(builds) != 1 || !strings.Contains(builds[0], "APPROVED at gate gate.\n\n1 comment, on 1 file:\n\nartifacts/plan.md\n1. [the whole file]\n   keep step two small") {
		t.Errorf("the approval the builder reads:\n%s", builds)
	}

	// The review is on the record, comment by comment.
	for _, p := range run.ReducePings(h.events(t, runID)) {
		if p.PingID == g.ID && (len(p.Review) != 2 || p.Review[1].StartLine != 3 || p.Feedback != "Close, two things.") {
			t.Errorf("recorded = %+v", p)
		}
	}
}

// An approval that says nothing carries nothing: the next stage reads no
// decision.
func TestABareApprovalCarriesNoDecision(t *testing.T) {
	rr := newReviewRun(t)
	h := rr.h
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "rv", testProject)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.e.AnswerPing(h.waitPing(t, event.PingGate).ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if b := rr.promptsOf("builder"); len(b) != 1 || strings.Contains(b[0], "# INPUT: review") {
		t.Errorf("builder:\n%s", b)
	}
}

// What an approval carries is in its stage_completed, so a run recovered
// between the gate and the next stage still hands it on — and a feed the
// gate was entered with does not outlive the gate.
func TestAnApprovalsDecisionSurvivesRecovery(t *testing.T) {
	fl := mustFlow(t, reviewFlowDoc)
	evs := []event.Event{
		{Payload: &event.RunCreated{TicketTitle: "T"}},
		{Payload: &event.StageStarted{StageID: "write", Iteration: 1}},
		{Payload: &event.StageCompleted{StageID: "write"}},
		{Payload: &event.StageStarted{StageID: "gate", Iteration: 1}},
		{Payload: &event.PingRaised{PingID: "g1", Type: event.PingGate, StageID: "gate"}},
		{Payload: &event.PingAnswered{PingID: "g1", Answer: "reject", Feedback: "no"}},
		{Payload: &event.EdgeTaken{From: "gate", To: "write", Edge: flow.EdgeReject, Count: 1, Max: 1, Feed: "REJECTED at gate gate.\n"}},
		{Payload: &event.StageStarted{StageID: "write", Iteration: 2}},
		{Payload: &event.StageCompleted{StageID: "write"}},
	}
	if rs := replay(evs, fl); rs.feed != "" {
		t.Errorf("the rejection outlived the stage it entered: %q", rs.feed)
	}
	evs = append(evs,
		event.Event{Payload: &event.StageStarted{StageID: "gate", Iteration: 2}},
		event.Event{Payload: &event.PingRaised{PingID: "g2", Type: event.PingGate, StageID: "gate"}},
		event.Event{Payload: &event.PingAnswered{PingID: "g2", Answer: "approve", Feedback: "ship it"}},
		event.Event{Payload: &event.StageCompleted{StageID: "gate", Feed: "APPROVED at gate gate.\n\nship it\n"}},
	)
	rs := replay(evs, fl)
	if rs.ph != phAfterComplete || rs.completedFeed != "APPROVED at gate gate.\n\nship it\n" {
		t.Fatalf("ph = %v, completedFeed = %q", rs.ph, rs.completedFeed)
	}
	evs = append(evs, event.Event{Payload: &event.StageStarted{StageID: "build", Iteration: 1}})
	if rs := replay(evs, fl); rs.feedSentinel != flow.SourceVerdict || !strings.Contains(rs.feed, "ship it") {
		t.Errorf("build re-entered with feed %q (%s)", rs.feed, rs.feedSentinel)
	}
}

const verdictApproveDoc = `name: va
version: 5
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
agents:
  - name: reviewer
    prompt: review it
    outputs:
      - { name: verdict, type: verdict }
  - name: builder
    prompt: build it
    inputs:
      - { name: notes, type: verdict, optional: true }
main:
  - { id: review, type: agent, agent: reviewer, output: { verdict: { to: artifacts/verdict.json, type: verdict } } }
  - { id: build, type: agent, agent: builder, input: { notes: { from: edge.verdict, type: verdict, optional: true } } }
`

// A verdict that approves with comments hands them to the next stage.
func TestAVerdictsApprovalCarriesItsComments(t *testing.T) {
	h := newHarness(t, mustFlow(t, verdictApproveDoc))
	var mu sync.Mutex
	var built string
	h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"reviewer": func(c fake.RunCall) (ports.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/verdict.json", `{"verdict":"APPROVE","comments":["rename the flag","add a test"]}`)
			return ports.AgentResult{}, nil
		},
		"builder": func(c fake.RunCall) (ports.AgentResult, error) {
			mu.Lock()
			built = c.In.Prompt
			mu.Unlock()
			return ports.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "va", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if !strings.Contains(built, "APPROVED by the review at stage review.\n\n2 comments:\n1. rename the flag\n2. add a test") {
		t.Errorf("builder:\n%s", built)
	}
}

// A question an agent asked about its files is answered with a review the
// same way: the answer, then the comments where they were made.
func TestAnAnswerCarriesItsReview(t *testing.T) {
	got := composeAnswerText(event.AnswerText, "text", "Mostly right.", []string{"docs/a.md"},
		[]event.ReviewComment{{Path: "docs/a.md", StartLine: 2, EndLine: 2, Body: "this is wrong", AttachmentIDs: []string{"a1"}}},
		[]answerFile{{id: "a1", path: ".apothecary/attachments/ping-p/a1/fix.txt"}, {id: "a2", path: ".apothecary/attachments/ping-p/a2/more.txt"}})
	want := "Mostly right.\n\n1 comment, on 1 file:\n\ndocs/a.md\n1. [line 2]\n   this is wrong\n   attached: .apothecary/attachments/ping-p/a1/fix.txt" +
		"\n\nThe human attached these files — read them with your tools:\n- .apothecary/attachments/ping-p/a2/more.txt\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// A verdict's comments are remarks or comments on a file — the shape a
// person's review has — and render the same way.
func TestARichVerdict(t *testing.T) {
	v, err := parseVerdict([]byte(`{"verdict":"REJECT","summary":"Two problems.","comments":[
		"the change has no test",
		{"path":"internal/flow/parse.go","line":412,"body":"this error loses the line number"},
		{"path":"docs/a.md","body":"out of date"},
		{"path":"internal/flow/parse.go","line":20,"end_line":24,"quote":"if err != nil","body":"wrap it"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := verdictText("review", v)
	want := "REJECTED by the review at stage review.\n\nTwo problems.\n\n4 comments, on 2 files:\n" +
		"1. the change has no test\n" +
		"\ninternal/flow/parse.go\n2. [lines 20–24]\n   > if err != nil\n   wrap it\n3. [line 412]\n   this error loses the line number\n" +
		"\ndocs/a.md\n4. [the whole file]\n   out of date\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	for _, bad := range []struct{ doc, want string }{
		{`{"verdict":"REJECT"}`, "REJECT requires comments, or a summary"},
		{`{"verdict":"REJECT","comments":[{"path":"../etc/passwd","body":"x"}]}`, "must be a file of the repository"},
		{`{"verdict":"REJECT","comments":[{"path":"a.go","line":5,"end_line":3,"body":"x"}]}`, "are not a range"},
		{`{"verdict":"REJECT","comments":[{"path":"a.go","body":" "}]}`, "comment 1 is empty"},
		{`{"verdict":"MAYBE"}`, "must be APPROVE or REJECT"},
	} {
		if _, err := parseVerdict([]byte(bad.doc)); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: err = %v, want %q", bad.doc, err, bad.want)
		}
	}
	if v, err := parseVerdict([]byte(`{"verdict":"REJECT","summary":"not this way"}`)); err != nil || v.Summary != "not this way" {
		t.Errorf("a summary alone says why: %+v, %v", v, err)
	}
}
