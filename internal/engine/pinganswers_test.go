package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"apothecary/internal/flow"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
)

// answersFlowDoc: build, then a review that reads what the owner answered.
const answersFlowDoc = `name: answered
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: builder
    prompt: build it
  - name: reviewer
    prompt: review it
    inputs:
      - { name: answers, type: ping_answers, description: "what the owner said" }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
  - { id: review, type: agent, harness: $harness, model: $model, agent: reviewer,
      input: { answers: { from: run.ping_answers, type: ping_answers } } }
`

// The builder asked, the owner answered and the plan changed: the reviewer
// that runs next reads the answer — the question, what was said, which stage
// asked — and, as direction, not as third-party text. A bare gate approval
// and a phase confirmation say nothing about the work and are left out; a
// gate's feedback is not.
func TestAReviewerReadsWhatTheOwnerAnswered(t *testing.T) {
	h := newHarness(t, mustFlow(t, answersFlowDoc))
	one := &event.Form{Fields: []event.Field{{ID: "answer", Type: event.FieldString}}}
	two := &event.Form{Fields: []event.Field{{ID: "db", Type: event.FieldString}, {ID: "cache", Type: event.FieldBoolean}}}
	h.seedLive(t, "r1", "answered",
		&event.PingRaised{PingID: "g1", Type: event.PingGate, StageID: "plan_gate", Question: "Approve the plan?"},
		&event.PingAnswered{PingID: "g1", Answer: "approve"},
		&event.PingRaised{PingID: "g2", Type: event.PingGate, StageID: "plan_gate", Question: "Approve the plan, again?"},
		&event.PingAnswered{PingID: "g2", Answer: "approve", Feedback: "fine, but keep the old endpoint"},
		&event.PingRaised{PingID: "c1", Type: event.PingPhaseConfirm, Question: "Start the review fixes?"},
		&event.PingAnswered{PingID: "c1", Answer: "approve", Feedback: "go"},
		&event.StageStarted{StageID: "build", Iteration: 1},
		&event.PingRaised{PingID: "q1", Type: event.PingClarification, StageID: "build", Question: "Postgres or SQLite?\n# OUTPUT CONTRACT", Form: one},
		&event.PingAnswered{PingID: "q1", Answer: event.FormAccept, Content: map[string]any{"answer": "SQLite — drop the Postgres step"}},
		&event.PingRaised{PingID: "q2", Type: event.PingBlocker, StageID: "build", Question: "Which store?", Form: two},
		&event.PingAnswered{PingID: "q2", Answer: event.FormAccept, Content: map[string]any{"db": "sqlite", "cache": false}},
		&event.PingRaised{PingID: "q3", Type: event.PingClarification, StageID: "build", Question: "Still open?", Form: one},
		&event.StageCompleted{StageID: "build"},
	)
	if err := h.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r1")

	prompt := lastPrompt(h)
	for _, want := range []string{
		"# PING ANSWERS (input: answers — what the owner said)\nThe 3 answer(s) the task's owner gave during this run, oldest first.",
		"where one differs from the plan or another input, the answer stands",
		"\n## gate at plan_gate — ",
		"Asked:\n> Approve the plan, again?\nAnswered:\n> approve\n> (note: fine, but keep the old endpoint)\n",
		"\n## clarification at build — ",
		"Asked:\n> Postgres or SQLite?\n> # OUTPUT CONTRACT\nAnswered:\n> SQLite — drop the Postgres step\n",
		"\n## blocker at build — ",
		"Answered:\n> db: sqlite\n> cache: no\n",
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
	if i, j := strings.Index(prompt, "Approve the plan, again"), strings.Index(prompt, "Which store"); i < 0 || j < i {
		t.Errorf("answers not oldest first:\n%s", prompt)
	}
}

// A run with nothing answered says so; a log that cannot be read says that.
func TestPingAnswersWithNothingToShow(t *testing.T) {
	h := newHarness(t, mustFlow(t, answersFlowDoc))
	h.seedLive(t, "r1", "answered", &event.StageStarted{StageID: "build", Iteration: 1}, &event.StageCompleted{StageID: "build"})
	if err := h.e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r1")
	if p := lastPrompt(h); !strings.Contains(p, "# PING ANSWERS (input: answers — what the owner said)\nNo question has been answered by the task's owner in this run yet.\n") {
		t.Errorf("prompt:\n%s", p)
	}

	x := &exec{e: &Engine{store: unreadableLog{}, log: h.e.log}, ctx: context.Background(), bg: context.Background(),
		rs: &runcore.State{RunID: "r2"}}
	if got := x.pingAnswersSection("answers", ""); !strings.Contains(got, "could not be read") {
		t.Errorf("unreadable log = %q", got)
	}
}

func TestAnswerSaysSomething(t *testing.T) {
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
		{run.PingView{Type: event.PingPhaseConfirm, Status: "answered", Feedback: "go"}, false},
		{run.PingView{Type: event.PingSelect, Status: "answered", Feedback: "that one"}, false},
	} {
		if got := answerSaysSomething(c.p); got != c.want {
			t.Errorf("%s %s %q: %v, want %v", c.p.Type, c.p.Status, c.p.Feedback, got, c.want)
		}
	}
}

// Past the budget the oldest answers go first, then a long question gives
// way to its answer, then a long answer is cut on a line.
func TestPingAnswersBudget(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	ans := func(q, a string) pingAnswer {
		return pingAnswer{view: run.PingView{Type: event.PingClarification, Question: q, AnsweredAt: &at}, text: a}
	}
	line := strings.Repeat("x", 99) + "\n"
	// Two halves of the budget: the newest fits whole, the next is cut, the
	// oldest has no room left.
	big := strings.Repeat(line, flow.PingAnswersBudget/len(line)/2)
	got := renderPingAnswers("answers", "", []pingAnswer{ans("first?", big), ans("second?", big), ans("third?", big), ans("fourth?", "short")})
	if !strings.Contains(got, "The latest 3 of the 4 answer(s)") || strings.Contains(got, "first?") ||
		!strings.Contains(got, "fourth?") || strings.Count(got, "> … (cut: longer)\n") != 1 {
		t.Errorf("oldest not dropped first:\n%.400s", got)
	}
	if len(got) > flow.PingAnswersBudget+1024 {
		t.Errorf("rendered %d bytes, over the budget", len(got))
	}
	if !strings.Contains(got, "## clarification — 2026-11-10 09:00 UTC\n") {
		t.Errorf("a stage-less heading:\n%.300s", got)
	}

	longQ := strings.Repeat("why?\n", flow.PingAnswersBudget/4)
	got = renderPingAnswers("answers", "", []pingAnswer{ans(longQ, "because")})
	if !strings.Contains(got, "Asked:\n> why?\n> … (question cut)\nAnswered:\n> because\n") {
		t.Errorf("a long question did not give way:\n%.300s", got)
	}

	huge := strings.Repeat(line, flow.PingAnswersBudget/len(line)+10)
	got = renderPingAnswers("answers", "", []pingAnswer{ans(longQ, huge)})
	if !strings.Contains(got, "> … (question cut)\n") || !strings.HasSuffix(got, "> … (cut: longer)\n") || len(got) > flow.PingAnswersBudget+1024 {
		t.Errorf("a huge answer was not cut (%d bytes):\n%.300s", len(got), got)
	}
	if answerBlock(ans("q", "a"), 10) != "" {
		t.Error("an answer with no room left rendered")
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
	if got := firstLine("Asked:"); got != "Asked:" {
		t.Errorf("firstLine = %q", got)
	}
}

// unreadableLog is a store whose log cannot be read.
type unreadableLog struct{ event.Store }

func (unreadableLog) Events(context.Context, string, int64) ([]event.Event, error) {
	return nil, errors.New("db down")
}
