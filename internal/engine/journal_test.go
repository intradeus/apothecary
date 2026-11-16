package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
	"apothecary/internal/run/runinput"
)

// journalFlowDoc: build, then a review and a last look that read the run
// journal.
const journalFlowDoc = `name: journaled
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: builder
    prompt: build it
  - name: reviewer
    prompt: review it
    inputs:
      - { name: journal, type: journal, description: "what happened" }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
  - { id: review, type: agent, harness: $harness, model: $model, agent: reviewer,
      input: { journal: { from: run.journal, type: journal } } }
  - { id: final, type: agent, harness: $harness, model: $model, agent: reviewer,
      input: { journal: { from: run.journal, type: journal } } }
`

// The builder asked, the owner answered, a gate was sent back, an impasse
// was skipped: the last look that runs next reads every decision — the
// question or the verdict it answers, what was decided, which stage and pass
// — as direction, oldest first, each with its id. A bare gate approval and a
// phase confirmation say nothing about the work and are left out; a gate's
// feedback is not.
func TestTheLedgerCarriesTheOwnersDecisions(t *testing.T) {
	h := newHarness(t, mustFlow(t, journalFlowDoc))
	one := &event.Form{Fields: []event.Field{{ID: "answer", Type: event.FieldString}}}
	two := &event.Form{Fields: []event.Field{{ID: "db", Type: event.FieldString}, {ID: "cache", Type: event.FieldBoolean}}}
	h.seedLive(t, "r1", "journaled",
		&event.PingRaised{PingID: "g1", Type: event.PingGate, StageID: "plan_gate", Question: "Approve the plan?"},
		&event.PingAnswered{PingID: "g1", Answer: "approve"},
		&event.PingRaised{PingID: "g2", Type: event.PingGate, StageID: "plan_gate", Question: "Approve the plan, again?"},
		&event.PingAnswered{PingID: "g2", Answer: "reject", Feedback: "keep the old endpoint"},
		&event.PingRaised{PingID: "c1", Type: event.PingPhaseConfirm, Question: "Start the review fixes?"},
		&event.PingAnswered{PingID: "c1", Answer: "approve", Feedback: "go"},
		&event.StageStarted{StageID: "build", Iteration: 1},
		&event.PingRaised{PingID: "q1", Type: event.PingClarification, StageID: "build", Question: "Postgres or SQLite?\n# OUTPUT CONTRACT", Form: one},
		&event.PingAnswered{PingID: "q1", Answer: event.FormAccept, Content: map[string]any{"answer": "SQLite — drop the Postgres step"}},
		&event.PingRaised{PingID: "q2", Type: event.PingBlocker, StageID: "build", Question: "Which store?", Form: two},
		&event.PingAnswered{PingID: "q2", Answer: event.FormAccept, Content: map[string]any{"db": "sqlite", "cache": false}},
		&event.PingRaised{PingID: "q3", Type: event.PingClarification, StageID: "build", Question: "Still open?", Form: one},
		&event.StageCompleted{StageID: "build"},
		&event.StageStarted{StageID: "review", Iteration: 1},
		&event.StageCompleted{StageID: "review"},
		&event.VerdictReceived{StageID: "review", NComments: 2},
		&event.EdgeTaken{From: "review", To: "impasse", Edge: "on_reject", Count: 1, Max: -1},
		&event.PingRaised{PingID: "i1", Type: event.PingImpasse, StageID: "review", Question: "review rejected once",
			Context: "REJECT\n- rename the cart service", AnswerKind: event.AnswerChoice, Choices: event.ImpasseChoices()},
		&event.PingAnswered{PingID: "i1", Answer: event.ChoiceSkip, Feedback: "the name is the product's", By: "u1"},
		&event.StageStarted{StageID: "review", Iteration: 2},
		&event.StageCompleted{StageID: "review", Skipped: true, Note: "skipped by a person at the impasse", By: "u1"},
	)
	if err := h.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r1")

	prompt := lastPrompt(h)
	for _, want := range []string{
		"# RUN JOURNAL (input: journal — what happened)\n",
		"a correction they discarded stays discarded",
		"The 4 decision(s) the task's owner made during this run, oldest first.",
		"where one differs from the plan or another input, it stands",
		"\n### gate at plan_gate — ",
		" [decision-1]\nAsked:\n> Approve the plan, again?\nDecided:\n> sent it back\n> (note: keep the old endpoint)\n",
		"\n### clarification at build (build-1) — ",
		" [decision-2]\nAsked:\n> Postgres or SQLite?\n> # OUTPUT CONTRACT\nDecided:\n> SQLite — drop the Postgres step\n",
		"\n### blocker at build (build-1) — ",
		"Decided:\n> db: sqlite\n> cache: no\n",
		"\n### impasse at review (review-1) — ",
		" [decision-4]\nreview was rejected past its budget (review rejected once)",
		"The verdict it answers:\n> REJECT\n> - rename the cart service\n",
		"Decided:\n> skipped review: it completed with no output and the flow went on past it — its verdict was overruled\n> Their note: the name is the product's\n",
		"\n## Passes\n",
		"- build: 1 pass(es); the latest, build-1: completed\n",
		"- review: 2 pass(es); the latest, review-2: skipped by the owner\n",
		"\n## Reading more\nCall the journal_read tool (your harness may list it with a prefix, such as ask_human_journal_read).",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, not := range []string{"Approve the plan?\n", "Start the review fixes", "Still open"} {
		if strings.Contains(prompt, not) {
			t.Errorf("prompt shows %q:\n%s", not, prompt)
		}
	}
	if i, j := strings.Index(prompt, "decision-1"), strings.Index(prompt, "decision-4"); i < 0 || j < i {
		t.Errorf("decisions not oldest first:\n%s", prompt)
	}
}

// A run with nothing decided and nothing run says so; a log that cannot be
// read says that.
func TestTheLedgerWithNothingToShow(t *testing.T) {
	got := renderLedger(buildJournal(nil, nil, nil), "journal", "")
	for _, want := range []string{"None yet: the task's owner has decided nothing during this run.\n", "No stage has run yet.\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("empty ledger missing %q:\n%s", want, got)
		}
	}

	h := newHarness(t, mustFlow(t, journalFlowDoc))
	x := &exec{e: &Engine{store: unreadableLog{}, log: h.e.log}, ctx: context.Background(), bg: context.Background(),
		rs: &runcore.State{RunID: "r2"}}
	if got := x.journalSection("journal", ""); !strings.Contains(got, "could not be read") {
		t.Errorf("unreadable log = %q", got)
	}
}

func TestJournalDecides(t *testing.T) {
	for _, c := range []struct {
		p    run.PingView
		want bool
	}{
		{run.PingView{Type: event.PingClarification, Status: "answered"}, true},
		{run.PingView{Type: event.PingPermission, Status: "answered"}, true},
		{run.PingView{Type: event.PingClarification, Status: "pending"}, false},
		{run.PingView{Type: event.PingBlocker, Status: "withdrawn"}, false},
		{run.PingView{Type: event.PingGate, Status: "answered", Answer: "reject"}, false},
		{run.PingView{Type: event.PingGate, Status: "answered", Feedback: "  "}, false},
		{run.PingView{Type: event.PingGate, Status: "answered", Review: []event.ReviewComment{{Body: "x"}}}, true},
		{run.PingView{Type: event.PingDebug, Status: "answered", Feedback: "fixed the env"}, true},
		{run.PingView{Type: event.PingManual, Status: "answered", Feedback: "done by hand"}, true},
		{run.PingView{Type: event.PingImpasse, Status: "answered", Answer: event.ChoiceSkip}, true},
		{run.PingView{Type: event.PingImpasse, Status: "answered", Answer: event.ChoiceKeepGoing}, true},
		{run.PingView{Type: event.PingImpasse, Status: "pending"}, false},
		{run.PingView{Type: event.PingPhaseConfirm, Status: "answered", Feedback: "go"}, false},
		{run.PingView{Type: event.PingSelect, Status: "answered", Feedback: "that one"}, false},
	} {
		if got := journalDecides(c.p); got != c.want {
			t.Errorf("%s %s %q %q: %v, want %v", c.p.Type, c.p.Status, c.p.Answer, c.p.Feedback, got, c.want)
		}
	}
}

// No owner decision is ever dropped, however many and however long: a long
// body shows its head, cut on a line, and names the call that gives all of
// it.
func TestTheLedgerDropsNoDecision(t *testing.T) {
	line := strings.Repeat("x", 99) + "\n"
	long := strings.Repeat(line, 200) // 20 KB
	var evs []event.Event
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	for i := range 300 {
		id := fmt.Sprintf("q%d", i)
		evs = append(evs,
			event.Event{TS: at, Payload: &event.PingRaised{PingID: id, Type: event.PingClarification, Question: "question " + id + "?\n" + long}},
			event.Event{TS: at, Payload: &event.PingAnswered{PingID: id, Answer: event.FormAccept, Feedback: long}})
	}
	got := renderLedger(buildJournal(evs, nil, nil), "journal", "")
	for i := 1; i <= 300; i++ {
		if !strings.Contains(got, fmt.Sprintf(" [decision-%d]\n", i)) {
			t.Fatalf("decision-%d dropped", i)
		}
	}
	if !strings.Contains(got, "> … (cut — journal_read(id: \"decision-7\") gives all of it)\n") {
		t.Errorf("a long body does not say where the rest is:\n%.2000s", got)
	}
	if strings.Contains(got, strings.Repeat(line, 20)) {
		t.Error("a long body was not cut")
	}
	if !strings.Contains(got, "question q0?") {
		t.Error("a cut question lost its head")
	}
}

// The journal reads each pass off the log: how it ended, its verdict, where
// its edge went, what it left.
func TestBuildJournalReadsEachPass(t *testing.T) {
	approve := false
	evs := []event.Event{
		{Payload: &event.PhaseStarted{Phase: "main"}},
		{Payload: &event.StageStarted{StageID: "fix-ci", Iteration: 1}},
		{Payload: &event.StageFailed{StageID: "fix-ci", Reason: "exit_nonzero", Error: "exit 1"}},
		{Payload: &event.SnapshotCaptured{StageID: "fix-ci", Iteration: 1, Kind: "log", SnapshotID: "L1", Size: 10}},
		{Payload: &event.EdgeTaken{From: "fix-ci", To: "fix-ci", Edge: "on_fail", Count: 1, Max: 2}},
		{Payload: &event.StageStarted{StageID: "fix-ci", Iteration: 2}},
		{Payload: &event.StagePaused{StageID: "fix-ci"}},
		{Payload: &event.StageResumed{StageID: "fix-ci"}},
		{Payload: &event.StageCompleted{StageID: "fix-ci", TurnsUsed: 3}},
		{Payload: &event.SnapshotCaptured{StageID: "fix-ci", Iteration: 2, Kind: "output", Name: "verdict", Path: "artifacts/v.json", SnapshotID: "O1"}},
		{Payload: &event.SnapshotCaptured{StageID: "fix-ci", Iteration: 9, Kind: "output", SnapshotID: "lost"}},
		{Payload: &event.SnapshotCaptured{StageID: "fix-ci", Iteration: 2, Kind: "ping_file", SnapshotID: "P1"}},
		{Payload: &event.VerdictReceived{StageID: "fix-ci", Approve: approve, NComments: 4}},
		{Payload: &event.EdgeTaken{From: "fix-ci", To: "impasse", Edge: "on_reject", Count: 1, Max: -1}},
		{Payload: &event.StageStarted{StageID: "later", Iteration: 1}},
		{Payload: &event.StagePaused{StageID: "later"}},
		{Payload: &event.StageStarted{StageID: "now", Iteration: 1}},
		{Payload: &event.EdgeTaken{From: "nowhere", To: "x", Edge: "on_fail", Count: 1, Max: 1}},
		{Payload: &event.StageCompleted{StageID: "nowhere"}},
		{Payload: &event.StageFailed{StageID: "nowhere"}},
		{Payload: &event.StagePaused{StageID: "nowhere"}},
		{Payload: &event.StageResumed{StageID: "nowhere"}},
		{Payload: &event.VerdictReceived{StageID: "nowhere"}},
		{Payload: &event.EdgeTaken{From: "now", To: "b", Edge: "on_fail", Count: 3, Max: -1}},
	}
	j := buildJournal(evs, mustFlow(t, journalFlowDoc), nil)
	if len(j.passes) != 4 || j.chainRun != 1 {
		t.Fatalf("passes = %d, chain runs = %d", len(j.passes), j.chainRun)
	}
	one, two := j.byPass["fix-ci-1"], j.byPass["fix-ci-2"]
	if one.end != "failed" || one.why != "exit_nonzero" || one.log == nil || one.log.id != "L1" || one.routed != "on_fail → fix-ci (1/2)" {
		t.Errorf("fix-ci-1 = %+v", one)
	}
	if two.end != "completed" || two.turns != 3 || len(two.outputs) != 1 || two.outputs[0].name != "verdict" ||
		two.verdict == nil || *two.verdict || two.comments != 4 || two.routed != "on_reject → impasse" {
		t.Errorf("fix-ci-2 = %+v", two)
	}
	got := historyBlock(j)
	for _, want := range []string{
		"- fix-ci: 2 pass(es); the latest, fix-ci-2: completed; verdict REJECT, 4 comment(s); then on_reject → impasse\n",
		"- later: 1 pass(es); the latest, later-1: paused\n",
		"- now: 1 pass(es); the latest, now-1: running now — not in the journal until it ends; then on_fail → b (3)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("history missing %q:\n%s", want, got)
		}
	}
	if passOutcome(&jPass{end: "failed", why: "output_contract"}) != "failed (output contract)" ||
		passOutcome(&jPass{end: "skipped"}) != "skipped" {
		t.Error("outcome words")
	}
	yes := true
	if !strings.Contains(passOutcome(&jPass{end: "completed", verdict: &yes}), "verdict APPROVE") {
		t.Error("an approving verdict")
	}
}

func TestPassIDs(t *testing.T) {
	for _, c := range []struct {
		id    string
		stage string
		n     int
		ok    bool
	}{
		{"review-5", "review", 5, true},
		{"fix-ci-2", "fix-ci", 2, true},
		{"make.build-1", "make.build", 1, true},
		{"review", "", 0, false},
		{"-3", "", 0, false},
		{"review-0", "", 0, false},
		{"review-x", "", 0, false},
	} {
		stage, n, ok := splitPassID(c.id)
		if stage != c.stage || n != c.n || ok != c.ok {
			t.Errorf("splitPassID(%q) = %q %d %v", c.id, stage, n, ok)
		}
		if c.ok && passID(stage, n) != c.id {
			t.Errorf("passID round trip of %q", c.id)
		}
	}
}

// Each decision reads in words: what the gate, the debug leaf, the manual
// stage or the impasse did, and an answer the words do not know as itself.
func TestDecisionWords(t *testing.T) {
	for _, c := range []struct {
		p    run.PingView
		want string
	}{
		{run.PingView{Type: event.PingGate, Answer: "approve", Feedback: "fine"}, "approved\n(note: fine)"},
		{run.PingView{Type: event.PingDebug, Answer: "approve", Feedback: "fixed"}, "continued: the failing stage ran again\nWhat they did: fixed"},
		{run.PingView{Type: event.PingDebug, Answer: "reject", Feedback: "no"}, "gave up\n(note: no)"},
		{run.PingView{Type: event.PingManual, Answer: "approve", Feedback: "did it"}, "done by hand\n(note: did it)"},
		{run.PingView{Type: event.PingManual, Answer: "reject", Feedback: "no"}, "gave up\n(note: no)"},
		{run.PingView{Type: event.PingGate, Answer: "odd", Feedback: "?"}, "odd\n(note: ?)"},
		{run.PingView{Type: event.PingImpasse, StageID: "review", Answer: event.ChoiceKeepGoing},
			"kept going: the rejection went back along its route, review's rejection budget restored"},
		{run.PingView{Type: event.PingImpasse, Answer: event.ChoiceGiveUp, EndsPhase: true}, "gave up: that round ended"},
		{run.PingView{Type: event.PingImpasse, Answer: event.ChoiceGiveUp}, "gave up: the run was abandoned"},
		{run.PingView{Type: event.PingImpasse, Answer: "later"}, "later"},
	} {
		if got := decisionAnswer(&jDecision{ping: c.p}); got != c.want {
			t.Errorf("%s %s = %q, want %q", c.p.Type, c.p.Answer, got, c.want)
		}
	}
	d := &jDecision{id: "decision-1", ping: run.PingView{Type: event.PingImpasse, StageID: "review", Question: "review rejected once"}}
	if got := decisionBlock(d, 100); strings.Contains(got, "The verdict it answers") || !strings.Contains(got, "### impasse at review [decision-1]\n") {
		t.Errorf("an impasse with no verdict on record:\n%s", got)
	}
}

// A decision carries what it answers (the plan's 2.1): a gate, the files
// and the link it showed; a debug continue, the end of the failure's output;
// a skip at the impasse, the outputs the stage did not write — what a later
// stage will not find.
func TestDecisionsCarryWhatTheyAnswer(t *testing.T) {
	evs := []event.Event{
		{Payload: &event.PingRaised{PingID: "g", Type: event.PingGate, StageID: "plan_gate", Question: "Approve the plan?",
			Files: []string{"artifacts/plan.md"}, Link: "https://git.local/mr/1"}},
		{Payload: &event.PingAnswered{PingID: "g", Answer: "reject", Feedback: "smaller steps"}},
		{Payload: &event.PingRaised{PingID: "d", Type: event.PingDebug, StageID: "test", Question: "test failed", Context: "FAIL: TestCart\nexit status 1\n"}},
		{Payload: &event.PingAnswered{PingID: "d", Answer: "approve", Feedback: "restarted the db"}},
		{Payload: &event.PingRaised{PingID: "i", Type: event.PingImpasse, StageID: "review", Question: "review rejected 5 times",
			AnswerKind: event.AnswerChoice, Choices: event.ImpasseChoices()}},
		{Payload: &event.PingAnswered{PingID: "i", Answer: event.ChoiceSkip}},
		{Payload: &event.PingRaised{PingID: "bare", Type: event.PingGate, StageID: "plan_gate", Question: "Again?"}},
		{Payload: &event.PingAnswered{PingID: "bare", Answer: "reject", Feedback: "no"}},
	}
	got := renderLedger(buildJournal(evs, impasseFlow(t, "{ to: build, max: 2, then: impasse }"), nil), "journal", "")
	for _, want := range []string{
		"Asked:\n> Approve the plan?\nIt showed: artifacts/plan.md, https://git.local/mr/1\nDecided:\n> sent it back\n",
		"Asked:\n> test failed\nWhat failed (the end of its output):\n> FAIL: TestCart\n> exit status 1\nDecided:\n> continued",
		"\n> It would have written, and did not: verdict (artifacts/verdict.json)\n",
		"Asked:\n> Again?\nDecided:\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ledger missing %q:\n%s", want, got)
		}
	}
	// A skip of a stage the flow no longer has names no outputs.
	if got := renderLedger(buildJournal(evs, nil, nil), "journal", ""); strings.Contains(got, "would have written") {
		t.Errorf("outputs named with no flow:\n%s", got)
	}
}

// What an answer attached is found through the engine's attachments and
// named in the decision, at the path it was delivered to.
func TestJournalOfNamesAttachedFiles(t *testing.T) {
	att, snaps, _ := newFiles(t)
	h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Attachments, d.Snapshots = att, snaps }, mustFlow(t, journalFlowDoc))
	ctx := context.Background()
	up, err := att.Save(ctx, "", "logo.png", "image/png", strings.NewReader("png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := att.ClaimForRun(ctx, "", []string{up.ID}, "r1", "q1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Append(ctx, "r1",
		&event.PingRaised{PingID: "q1", Type: event.PingClarification, Question: "Which logo?"},
		&event.PingAnswered{PingID: "q1", Answer: event.FormAccept, Feedback: "this one", AttachmentIDs: []string{up.ID}},
		&event.PingRaised{PingID: "q2", Type: event.PingClarification, Question: "And the font?"},
		&event.PingAnswered{PingID: "q2", Answer: event.FormAccept, Feedback: "any"},
	); err != nil {
		t.Fatal(err)
	}
	j, err := h.e.journalOf(ctx, "r1", nil)
	if err != nil || len(j.decisions) != 2 || len(j.decisions[0].files) != 1 || len(j.decisions[1].files) != 0 {
		t.Fatalf("journal = %+v, %v", j, err)
	}
	claimed, err := att.Get(ctx, up.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := renderLedger(j, "journal", ""); !strings.Contains(got, "Decided:\n> (note: this one)\n") ||
		!strings.Contains(got, "> - "+attachmentPath(claimed)+"\n") {
		t.Errorf("the attached file is not named:\n%s", got)
	}
	if _, err := (&Engine{store: unreadableLog{}}).journalOf(ctx, "r1", nil); err == nil {
		t.Error("an unreadable log read")
	}
	// No attachments port, or one that cannot be read: the decision stands,
	// with no file named.
	for _, e := range []*Engine{{store: h.store}, {store: h.store, attachments: brokenAttachments{att}}} {
		if j, err := e.journalOf(ctx, "r1", nil); err != nil || len(j.decisions) != 2 || len(j.decisions[0].files) != 0 {
			t.Errorf("journal = %+v, %v", j, err)
		}
	}
}

// brokenAttachments cannot list a ping's files.
type brokenAttachments struct{ AttachmentStore }

func (brokenAttachments) ForPing(context.Context, string) ([]filestore.Attachment, error) {
	return nil, errors.New("db down")
}

// An output is typed by its stage's declaration in the flow; an output the
// flow does not declare, or a stage it does not have, is untyped.
func TestOutputType(t *testing.T) {
	fl := mustFlow(t, `name: typed
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: reviewer
    prompt: review it
    outputs:
      - { name: verdict, type: verdict, description: "the verdict" }
main:
  - { id: review, type: agent, harness: $harness, model: $model, agent: reviewer,
      output: { verdict: { to: artifacts/verdict.json, type: verdict } } }
`)
	for _, c := range []struct{ stage, name, want string }{
		{"review", "verdict", verdictType}, {"review", "other", ""}, {"gone", "verdict", ""},
	} {
		if got := outputType(fl, c.stage, c.name); got != c.want {
			t.Errorf("outputType(%s, %s) = %q, want %q", c.stage, c.name, got, c.want)
		}
	}
	if outputType(nil, "review", "verdict") != "" {
		t.Error("no flow, a type")
	}
}

func TestCutAtLine(t *testing.T) {
	for _, c := range []struct {
		s    string
		n    int
		want string
	}{
		{"ab\ncd\n", 10, "ab\ncd\n"},
		{"ab\ncd\n", 4, "ab\n"},
		{"abcdef", 3, "abc\n"},
		{"é\n", 1, "\n"},
	} {
		if got := cutAtLine(c.s, c.n); got != c.want {
			t.Errorf("cutAtLine(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}

// unreadableLog is a store whose log cannot be read.
type unreadableLog struct{ event.Store }

func (unreadableLog) Events(context.Context, string, int64) ([]event.Event, error) {
	return nil, errors.New("db down")
}

// readFlowDoc: a review whose verdict is a typed output.
const readFlowDoc = `name: read
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: reviewer
    prompt: review it
    outputs:
      - { name: verdict, type: verdict, description: "the verdict" }
      - { name: shot, type: file, description: "a screenshot" }
main:
  - { id: review, type: agent, harness: $harness, model: $model, agent: reviewer,
      output: { verdict: { to: artifacts/verdict.json, type: verdict }, shot: { to: artifacts/shot.png, type: file } } }
`

// journal_read's entries: a pass whole — its record, each output (text
// embedded, a binary listed, one no longer kept said so) and its full log;
// every record with each pass's verdict and no log; a decision inline, or in
// a file when long; an id the run has not, refused.
func TestJournalRead(t *testing.T) {
	att, snaps, _ := newFiles(t)
	fl := mustFlow(t, readFlowDoc)
	h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Attachments, d.Snapshots = att, snaps }, fl)
	ctx := context.Background()
	capture := func(kind filestore.SnapshotKind, name, path string, data string) string {
		s, err := snaps.Capture(ctx, filestore.Snapshot{RunID: "r1", Kind: kind, StageID: "review", Iteration: 1,
			Name: name, Path: path, Filename: filepath.Base(path)}, func(s string) string { return s }, []byte(data))
		if err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	verdict := capture(filestore.KindOutput, "verdict", "artifacts/verdict.json", "REJECT\n- rename the cart service")
	shot := capture(filestore.KindOutput, "shot", "artifacts/shot.png", "\x89PNG\r\n\x1a\n\x00\x00")
	logID := capture(filestore.KindLog, "", "", "turn 1\nturn 2\n")
	long := strings.Repeat("a long answer line\n", 600)
	h.seed(t, "r1",
		&event.RunCreated{TicketTitle: "T", FlowName: "read", FlowHash: fl.Hash, ProjectID: testProject, Branch: "feat/x"},
		&event.PhaseStarted{Phase: "main"},
		&event.StageStarted{StageID: "review", Iteration: 1},
		&event.PingRaised{PingID: "q1", Type: event.PingClarification, StageID: "review", Question: "Rename it?"},
		&event.PingAnswered{PingID: "q1", Answer: event.FormAccept, Feedback: "no"},
		&event.PingRaised{PingID: "q2", Type: event.PingClarification, StageID: "review", Question: "Sure?"},
		&event.PingAnswered{PingID: "q2", Answer: event.FormAccept, Feedback: long},
		&event.StageCompleted{StageID: "review", TurnsUsed: 2},
		&event.SnapshotCaptured{StageID: "review", Iteration: 1, Kind: "output", Name: "verdict", Path: "artifacts/verdict.json", SnapshotID: verdict, Mime: "application/json", Size: 33},
		&event.SnapshotCaptured{StageID: "review", Iteration: 1, Kind: "output", Name: "shot", Path: "artifacts/shot.png", SnapshotID: shot, Mime: "image/png", Size: 10},
		&event.SnapshotCaptured{StageID: "review", Iteration: 1, Kind: "output", Name: "notes", Path: "artifacts/notes.md", SnapshotID: "swept", Mime: "text/markdown"},
		&event.SnapshotCaptured{StageID: "review", Iteration: 1, Kind: "log", SnapshotID: logID, Size: 14},
		&event.VerdictReceived{StageID: "review", NComments: 1},
		&event.EdgeTaken{From: "review", To: "impasse", Edge: "on_reject", Count: 1, Max: -1},
		&event.StageStarted{StageID: "review", Iteration: 2},
	)

	pass, err := h.e.JournalRead(ctx, "r1", "review-1")
	if err != nil || pass.ID != "review-1" || pass.File != "review-1.md" {
		t.Fatalf("pass = %+v, %v", pass, err)
	}
	for _, want := range []string{
		"# RUN JOURNAL — pass review-1\n\n### review-1 — pass 1 of review\n",
		"Ended: completed; verdict REJECT, 1 comment(s); then on_reject → impasse\nTurns: 2\nDecisions in it: decision-1, decision-2\n",
		"\n## Output: verdict — artifacts/verdict.json (verdict), 33 bytes\n\nREJECT\n- rename the cart service\n",
		"\n## Output: shot — artifacts/shot.png (file), 10 bytes\n\n(not text — image/png: listed, not embedded)\n",
		"\n## Output: notes — artifacts/notes.md, 0 bytes\n\n(no longer kept)\n",
		"\n## Log (14 bytes)\n\nturn 1\nturn 2\n",
	} {
		if !strings.Contains(pass.Text, want) {
			t.Errorf("pass file missing %q:\n%s", want, pass.Text)
		}
	}
	running, err := h.e.JournalRead(ctx, "r1", "review-2")
	if err != nil || !strings.Contains(running.Text, "This pass is running now") {
		t.Errorf("running pass = %+v, %v", running, err)
	}

	all, err := h.e.JournalRead(ctx, "r1", "")
	if err != nil || all.ID != "records" || all.File != "records.md" {
		t.Fatalf("records = %+v, %v", all, err)
	}
	for _, want := range []string{
		"### clarification at review (review-1) — ", " [decision-1]\nAsked:\n> Rename it?\nDecided:\n> (note: no)\n",
		"Verdict:\n> REJECT\n> - rename the cart service\n",
	} {
		if !strings.Contains(all.Text, want) {
			t.Errorf("records missing %q:\n%s", want, all.Text)
		}
	}
	if strings.Contains(all.Text, "turn 1") || strings.Contains(all.Text, "## Log") {
		t.Errorf("records carry a log:\n%s", all.Text)
	}
	if strings.Count(all.Text, "a long answer line") != 600 {
		t.Error("the records cut a decision")
	}

	d, err := h.e.JournalRead(ctx, "r1", "decision-1")
	if err != nil || d.File != "" || !strings.HasPrefix(d.Text, "### clarification at review (review-1)") {
		t.Errorf("decision = %+v, %v", d, err)
	}
	if d, err := h.e.JournalRead(ctx, "r1", "decision-2"); err != nil || d.File != "decision-2.md" || strings.Count(d.Text, "a long answer line") != 600 {
		t.Errorf("long decision = %+v, %v", d.File, err)
	}
	for _, id := range []string{"review-3", "decision-9", "review", "nothing"} {
		if _, err := h.e.JournalRead(ctx, "r1", id); !errors.Is(err, ErrJournalEntryNotFound) {
			t.Errorf("%s: %v", id, err)
		}
	}
	h.e.store = unreadableLog{h.store}
	if _, err := h.e.JournalRead(ctx, "r1", ""); err == nil {
		t.Error("an unreadable log read")
	}
}

// Every id the ledger prints, journal_read resolves.
func TestEveryLedgerIDResolves(t *testing.T) {
	h := newHarness(t, mustFlow(t, journalFlowDoc))
	ctx := context.Background()
	h.seed(t, "r1",
		&event.PhaseStarted{Phase: "main"},
		&event.StageStarted{StageID: "build", Iteration: 1},
		&event.PingRaised{PingID: "q1", Type: event.PingBlocker, StageID: "build", Question: "Which?"},
		&event.PingAnswered{PingID: "q1", Answer: event.FormAccept, Feedback: "that"},
		&event.StageFailed{StageID: "build", Reason: "exit_nonzero"},
		&event.StageStarted{StageID: "build", Iteration: 2},
		&event.StageCompleted{StageID: "build"},
		&event.StageStarted{StageID: "fix-ci", Iteration: 1},
	)
	j, err := h.e.journalOf(ctx, "r1", nil)
	if err != nil {
		t.Fatal(err)
	}
	ledger := renderLedger(j, "journal", "")
	ids := regexp.MustCompile(`\[(decision-\d+)\]|the latest, ([\w.-]+-\d+):`).FindAllStringSubmatch(ledger, -1)
	if len(ids) != 3 {
		t.Fatalf("ids = %v in\n%s", ids, ledger)
	}
	for _, m := range ids {
		id := m[1] + m[2]
		if _, err := h.e.JournalRead(ctx, "r1", id); err != nil {
			t.Errorf("%s: %v", id, err)
		}
		// and every earlier pass the count names
		if stage, n, ok := splitPassID(id); ok {
			for i := 1; i < n; i++ {
				if _, err := h.e.JournalRead(ctx, "r1", passID(stage, i)); err != nil {
					t.Errorf("%s: %v", passID(stage, i), err)
				}
			}
		}
	}
}

// A snapshot store that fails says so, and is not taken for a dropped one.
func TestSnapBodyStoreDown(t *testing.T) {
	h := newHarness(t, mustFlow(t, journalFlowDoc))
	e := &Engine{snapshots: downSnapshots{}, log: h.e.log}
	if got := e.snapBody(context.Background(), jSnap{id: "x", mime: "text/plain"}); got != "(could not be read just now)\n" {
		t.Errorf("store down = %q", got)
	}
	if got := (&Engine{log: h.e.log}).snapBody(context.Background(), jSnap{id: "x"}); got != "(no longer kept)\n" {
		t.Errorf("no store = %q", got)
	}
	e.snapshots = binarySnapshots{}
	if got := e.snapBody(context.Background(), jSnap{id: "x"}); got != "(not text: listed, not embedded)\n" {
		t.Errorf("binary bytes = %q", got)
	}
	for _, mime := range []string{"application/zip", "application/pdf", "application/octet-stream", "video/mp4", "font/woff"} {
		if textual(jSnap{mime: mime}) {
			t.Errorf("%s read as text", mime)
		}
	}
}

type downSnapshots struct{ SnapshotStore }

func (downSnapshots) Bytes(context.Context, string) ([]byte, filestore.Snapshot, error) {
	return nil, filestore.Snapshot{}, errors.New("db down")
}

type binarySnapshots struct{ SnapshotStore }

func (binarySnapshots) Bytes(context.Context, string) ([]byte, filestore.Snapshot, error) {
	return []byte("a\x00b"), filestore.Snapshot{}, nil
}

// A pass's record says how a failed pass failed and why a person skipped
// one; an output names what it can; a run with nothing says so.
func TestPassRecordWords(t *testing.T) {
	j := &journal{}
	failed := passRecord(j, &jPass{id: "b-1", stage: "b", n: 1, end: "failed", why: "exit_nonzero", err: "exit 1\nmake: *** [all]"})
	if !strings.Contains(failed, "Ended: failed (exit nonzero)\nError:\n> exit 1\n> make: *** [all]\n") {
		t.Errorf("failed:\n%s", failed)
	}
	skipped := passRecord(j, &jPass{id: "r-2", stage: "r", n: 2, end: "skipped", note: "the name is the product's"})
	if !strings.Contains(skipped, "Note:\n> the name is the product's\n") {
		t.Errorf("skipped:\n%s", skipped)
	}
	if got := snapWords(jSnap{size: 9, truncated: true}); got != "(unnamed), 9 bytes, captured truncated" {
		t.Errorf("snapWords = %q", got)
	}
	if got := (&Engine{}).journalRecords(context.Background(), j); got != "# RUN JOURNAL — every record\n\n## Owner decisions\nNone.\n\n## Agent notes\n"+
		"What agents said they did differently from their direction — their claims, to check against the work.\nNone.\n\n## Passes\nNone.\n" {
		t.Errorf("empty records = %q", got)
	}
}

// notesFlowDoc: a builder that may leave notes, then a last look that reads
// the journal.
const notesFlowDoc = `name: noted
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: builder
    prompt: build it
    outputs:
      - { name: notes, type: journal_notes, description: "what you did differently" }
  - name: looker
    prompt: look at it
    inputs:
      - { name: journal, type: journal }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder,
      output: { notes: { to: artifacts/journal-notes.yaml, type: journal_notes } } }
  - { id: look, type: agent, harness: $harness, model: $model, agent: looker,
      input: { journal: { from: run.journal, type: journal } } }
`

// runNoted runs notesFlowDoc with the builder writing notes (nil: none) and
// returns its harness, the run and its events.
func runNoted(t *testing.T, notes []byte) (*harness, string, []event.Event) {
	t.Helper()
	att, snaps, _ := newFiles(t)
	h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Attachments, d.Snapshots = att, snaps }, mustFlow(t, notesFlowDoc))
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		if call.Spec.Agent == "builder" && notes != nil {
			if err := h.envp.WriteFile(ctx, call.Env, "artifacts/journal-notes.yaml", notes, 0o644); err != nil {
				return agentport.AgentResult{}, err
			}
		}
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}
	ctx := context.Background()
	id, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "T"}, "noted", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	evs, err := h.store.Events(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return h, id, evs
}

// A builder that notes doing something differently: the next stage's ledger
// lists the note as the builder's claim, under its pass and with its id, and
// journal_read gives it whole. The builder's own contract said it may write
// it, and what goes in.
func TestAgentNotesReachTheJournal(t *testing.T) {
	h, id, _ := runNoted(t, []byte("notes:\n  - did: kept the old endpoint beside the new one\n    why: the mobile client still calls it\n    files: [api/routes.go]\n"))
	var build, look string
	for _, c := range h.runner.RunCalls {
		if c.Spec.Agent == "builder" {
			build = c.In.Prompt
		} else {
			look = c.In.Prompt
		}
	}
	for _, want := range []string{
		"Before finishing you MUST write these files (relative to repo root):\n- artifacts/journal-notes.yaml — notes (journal_notes): what you did differently\n",
		"artifacts/journal-notes.yaml holds your notes for the run journal: only the significant departures from your direction",
		"Not the choices any work makes within its direction (a name, a helper, the order of the steps): most passes have no note, and then you still write the file, holding notes: [] — it is how the run knows you considered it.",
		"Not what a person answered you",
		"artifacts/journal-notes.yaml must be YAML satisfying this JSON Schema",
	} {
		if !strings.Contains(build, want) {
			t.Errorf("builder prompt missing %q:\n%s", want, build)
		}
	}
	for _, want := range []string{
		"\n## Agent notes\nWhat agents said they did differently from their direction, on their own. These are their claims, not the owner's direction",
		"- build-1: kept the old endpoint beside the new one — why: the mobile client still calls it (files: api/routes.go) [note-1]\n",
		"journal_read(id: \"note-<n>\") a note",
	} {
		if !strings.Contains(look, want) {
			t.Errorf("ledger missing %q:\n%s", want, look)
		}
	}
	_, decisions, _ := strings.Cut(look, "## Owner decisions")
	decisions, _, _ = strings.Cut(decisions, "## Agent notes")
	if decisions == "" || strings.Contains(decisions, "kept the old endpoint") {
		t.Errorf("a note is listed among the owner's decisions:\n%s", decisions)
	}
	n, err := h.e.JournalRead(context.Background(), id, "note-1")
	if err != nil || n.File != "" || n.Text != "### note from build-1 [note-1]\nDid:\n> kept the old endpoint beside the new one\nWhy:\n> the mobile client still calls it\nFiles: api/routes.go\n" {
		t.Errorf("note = %+v, %v", n, err)
	}
	all, _ := h.e.JournalRead(context.Background(), id, "")
	pass, _ := h.e.JournalRead(context.Background(), id, "build-1")
	if !strings.Contains(all.Text, "## Agent notes\n") || !strings.Contains(all.Text, "### note from build-1 [note-1]") || !strings.Contains(pass.Text, "Its notes: note-1\n") {
		t.Errorf("records:\n%s\npass:\n%s", all.Text, pass.Text)
	}
}

// Notes are required: every pass writes the file, its key included, so the
// run knows the agent considered it. None written fails the stage
// (output_contract); the key with an empty list, or with nothing after it,
// is no note; an empty file, or notes out of shape, fail it (output_invalid).
func TestAgentNotesRequiredButMayBeEmpty(t *testing.T) {
	failure := func(evs []event.Event) *event.StageFailed {
		for _, e := range evs {
			if f, ok := e.Payload.(*event.StageFailed); ok && f.StageID == "build" {
				return f
			}
		}
		return nil
	}
	if f := failure(func() []event.Event { _, _, evs := runNoted(t, nil); return evs }()); f == nil || f.Reason != event.ReasonOutputContract {
		t.Errorf("no notes file: %+v", f)
	}
	for _, empty := range []string{"notes: []\n", "notes:\n", `{"notes": []}`} {
		h, id, evs := runNoted(t, []byte(empty))
		if f := failure(evs); f != nil {
			t.Errorf("%q failed the stage: %+v", empty, f)
		}
		var look string
		for _, c := range h.runner.RunCalls {
			if c.Spec.Agent == "looker" {
				look = c.In.Prompt
			}
		}
		if !strings.Contains(look, "None: no agent has noted doing anything differently from its direction.") {
			t.Errorf("%q: notes in the ledger:\n%s", empty, look)
		}
		if pass, err := h.e.JournalRead(context.Background(), id, "build-1"); err != nil || strings.Contains(pass.Text, "Its notes") {
			t.Errorf("%q: pass record = %v\n%s", empty, err, pass.Text)
		}
	}
	for _, bad := range []string{"", "[]\n", "did: just one thing\n", "notes:\n  - did: x\n"} {
		if f := failure(func() []event.Event { _, _, evs := runNoted(t, []byte(bad)); return evs }()); f == nil || f.Reason != event.ReasonOutputInvalid {
			t.Errorf("%q: %+v", bad, f)
		}
	}
	if noNotes("artifacts/notes.json") != `{"notes": []}` || noNotes("artifacts/journal-notes.yaml") != "notes: []" {
		t.Error("how a notes file says there is none")
	}
}

// The ledger lists this round's notes, up to its bound, and counts the rest
// and the earlier rounds'; a long note is cut on one line; notes that cannot
// be read are said so in their pass's record.
func TestNotesBlock(t *testing.T) {
	old := &jPass{id: "build-1", chainRun: 1}
	now := &jPass{id: "fix-2", chainRun: 2}
	j := &journal{chainRun: 2}
	j.notes = append(j.notes, &jNote{id: "note-1", pass: old, did: "a", why: "b"})
	for i := range journalNotesListed + 2 {
		j.notes = append(j.notes, &jNote{id: fmt.Sprintf("note-%d", i+2), pass: now, did: "did\nthis", why: "why"})
	}
	got := notesBlock(j)
	// The latest are listed: this round's first two (note-2, note-3) and the
	// earlier round's are counted.
	if strings.Count(got, "\n- fix-2: did this — why: why [note-") != journalNotesListed ||
		strings.Contains(got, "[note-1]") || strings.Contains(got, "[note-2]") || strings.Contains(got, "[note-3]") ||
		!strings.Contains(got, "[note-4]\n") || !strings.Contains(got, fmt.Sprintf("[note-%d]\n(", journalNotesListed+3)) {
		t.Errorf("listed:\n%s", got)
	}
	if !strings.Contains(got, "(2 earlier from this round and 1 from earlier rounds: journal_read() writes them all.)\n") {
		t.Errorf("counted:\n%s", got)
	}
	if got := notesBlock(&journal{chainRun: 2, notes: j.notes[:1]}); !strings.Contains(got, "None this round.\n(1 from earlier rounds: journal_read() writes them all.)\n") {
		t.Errorf("only earlier:\n%s", got)
	}
	if got := notesBlock(&journal{}); got != "\n## Agent notes\nNone: no agent has noted doing anything differently from its direction.\n" {
		t.Errorf("none = %q", got)
	}
	long := noteLine(&jNote{pass: now, did: strings.Repeat("é", 400), why: "y"})
	if len(long) > journalNoteLine+len("…") || !strings.HasSuffix(long, "…") || !utf8.ValidString(long) {
		t.Errorf("a long note = %d bytes, %q…", len(long), long[:20])
	}
}

// Notes that cannot be read — swept, a store down, not notes — leave their
// pass's record saying so, and the other passes' notes stand.
func TestLoadNotesUnreadable(t *testing.T) {
	h := newHarness(t, mustFlow(t, journalFlowDoc))
	notesOf := func(id string) *jPass {
		return &jPass{id: "b-1", outputs: []jSnap{{id: id, typ: agentspec.TypeJournalNotes}}}
	}
	for _, c := range []struct {
		e    *Engine
		want string
	}{
		{&Engine{log: h.e.log}, "no longer kept"},
		{&Engine{log: h.e.log, snapshots: downSnapshots{}}, "could not be read just now"},
		{&Engine{log: h.e.log, snapshots: binarySnapshots{}}, "not a notes file"},
	} {
		ps := notesOf("x")
		j := &journal{passes: []*jPass{ps}, byNote: map[string]*jNote{}}
		c.e.loadNotes(context.Background(), j)
		if !strings.HasPrefix(ps.notesErr, c.want) || len(j.notes) != 0 {
			t.Errorf("%T: %q", c.e.snapshots, ps.notesErr)
		}
		if rec := passRecord(j, ps); !strings.Contains(rec, "Its notes could not be read: "+c.want) {
			t.Errorf("record:\n%s", rec)
		}
	}
}

// A long run (the plan's 2.7): thousands of passes and notes leave the
// ledger the same size — a line per stage, the notes listed to their bound —
// and a multi-megabyte pass is written to its file whole.
func TestALongRun(t *testing.T) {
	evs := []event.Event{{Payload: &event.PhaseStarted{Phase: "main"}}}
	for i := 1; i <= 1000; i++ {
		for _, st := range []string{"build", "review", "test"} {
			evs = append(evs,
				event.Event{Payload: &event.StageStarted{StageID: st, Iteration: i}},
				event.Event{Payload: &event.StageCompleted{StageID: st, TurnsUsed: 3}},
				event.Event{Payload: &event.SnapshotCaptured{StageID: st, Iteration: i, Kind: "log", SnapshotID: fmt.Sprintf("L%s%d", st, i), Size: 1 << 20}},
				event.Event{Payload: &event.EdgeTaken{From: st, To: "build", Edge: "on_fail", Count: 1, Max: 5}})
		}
	}
	j := buildJournal(evs, nil, nil)
	for i, ps := range j.passes {
		n := &jNote{id: fmt.Sprintf("note-%d", i+1), pass: ps, did: strings.Repeat("did ", 100), why: strings.Repeat("why ", 100)}
		j.notes = append(j.notes, n)
		j.byNote[n.id] = n
	}
	got := renderLedger(j, "journal", "")
	if len(evs) < 4000 || len(j.passes) != 3000 {
		t.Fatalf("events %d, passes %d", len(evs), len(j.passes))
	}
	_, passes, _ := strings.Cut(got, "\n## Passes\n")
	if n := strings.Count(passes, "\n- "); n != 3 || !strings.Contains(passes, "- review: 1000 pass(es); the latest, review-1000: completed") {
		t.Errorf("passes (%d lines):\n%s", n, passes)
	}
	if n := strings.Count(got, " [note-"); n > journalNotesListed {
		t.Errorf("%d notes listed, bound %d", n, journalNotesListed)
	}
	if !strings.Contains(got, "- test-1000: did did") || strings.Contains(got, "[note-1]") ||
		!strings.Contains(got, "(2970 earlier from this round: journal_read() writes them all.)") {
		t.Errorf("not the latest listed, the rest counted:\n%.3000s", got)
	}
	if len(got) > 16<<10 {
		t.Errorf("ledger is %d bytes after %d events", len(got), len(evs))
	}

	// A pass whose log is megabytes is written to its file whole.
	mem := filestore.NewMem()
	snaps := filestore.NewSnapshots(mem, filestore.NewBlobs(t.TempDir()), 8<<20)
	fl := mustFlow(t, readFlowDoc)
	h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Snapshots = snaps }, fl)
	ctx := context.Background()
	line := strings.Repeat("x", 79) + "\n"
	big := strings.Repeat(line, 5<<20/len(line)) // 5 MiB
	s, err := snaps.Capture(ctx, filestore.Snapshot{RunID: "r1", Kind: filestore.KindLog, StageID: "review", Iteration: 1, Filename: "review-1.log"},
		func(s string) string { return s }, []byte(big))
	if err != nil {
		t.Fatal(err)
	}
	h.seed(t, "r1",
		&event.RunCreated{TicketTitle: "T", FlowName: "read", FlowHash: fl.Hash, ProjectID: testProject, Branch: "feat/x"},
		&event.PhaseStarted{Phase: "main"},
		&event.StageStarted{StageID: "review", Iteration: 1},
		&event.StageCompleted{StageID: "review"},
		&event.SnapshotCaptured{StageID: "review", Iteration: 1, Kind: "log", SnapshotID: s.ID, Size: int64(len(big))},
	)
	pass, err := h.e.JournalRead(ctx, "r1", "review-1")
	if err != nil || pass.File != "review-1.md" || !strings.Contains(pass.Text, "\n\n"+big) || strings.Contains(pass.Text, "truncated") {
		t.Errorf("pass = %s, %d bytes, %v", pass.File, len(pass.Text), err)
	}
}

// An optional output is listed apart in the contract, under what the agent
// may write; a files one may be left empty.
func TestOptionalOutputsInTheContract(t *testing.T) {
	h := newHarness(t, mustFlow(t, `name: opt
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: maker
    prompt: make it
    outputs:
      - { name: plan, type: file, description: "the plan" }
      - { name: extra, type: file, description: "anything worth keeping" }
      - { name: shots, type: files, description: "screenshots" }
main:
  - { id: make, type: agent, harness: $harness, model: $model, agent: maker,
      output: { plan: { to: artifacts/plan.md, type: file },
                extra: { to: artifacts/extra.md, type: file, optional: true },
                shots: { to: artifacts/shots/, type: files, optional: true } } }
`))
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		return agentport.AgentResult{TurnsUsed: 1}, h.envp.WriteFile(ctx, call.Env, "artifacts/plan.md", []byte("p"), 0o644)
	}
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "opt", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	got := lastPrompt(h)
	for _, want := range []string{
		"Before finishing you MUST write these files (relative to repo root):\n- artifacts/plan.md — plan: the plan\n",
		"You MAY also write these, only when they apply — leaving one out is fine, but one you write must hold its shape:\n- artifacts/extra.md — extra: anything worth keeping\n- artifacts/shots/ — shots: screenshots\n",
		"artifacts/shots/ is a directory: write one or more files into it (it starts empty), or none.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("contract missing %q:\n%s", want, got)
		}
	}
	for _, e := range h.events(t, id) {
		if f, ok := e.Payload.(*event.StageFailed); ok {
			t.Errorf("optional outputs left out failed the stage: %+v", f)
		}
	}
}
