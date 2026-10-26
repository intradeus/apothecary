package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports"
)

// The answer shape is declared on the ping and validated from one table:
// every kind accepts its own answers and nothing else.
func TestValidateAnswerByKind(t *testing.T) {
	opts := []event.Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}
	cases := []struct {
		kind, answer, feedback string
		ok                     bool
	}{
		{event.AnswerYesNo, "approve", "", true},
		{event.AnswerYesNo, "reject", "why", true},
		{event.AnswerYesNo, "a", "", false},
		{event.AnswerChoice, "a", "", true},
		{event.AnswerChoice, "other", "my own", true},
		{event.AnswerChoice, "other", "", false},
		{event.AnswerChoice, "c", "", false},
		{event.AnswerMulti, "a\nb", "", true},
		{event.AnswerMulti, "none", "", true},
		{event.AnswerMulti, "", "", false},
		{event.AnswerMulti, "a\nz", "", false},
		{event.AnswerText, "anything at all", "", true},
	}
	for _, c := range cases {
		err := validateAnswer(c.kind, opts, PingAnswer{Answer: c.answer, Feedback: c.feedback})
		if (err == nil) != c.ok {
			t.Errorf("%s %q/%q: err=%v, want ok=%v", c.kind, c.answer, c.feedback, err, c.ok)
		}
	}
}

// An agent may ask for a selection: structured options, multi: true; the
// tool return names the chosen ids.
func TestAskHumanMultiSelect(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	got := make(chan string, 1)
	h.runner.RunFunc = func(ctx context.Context, _ fake.RunCall) (ports.AgentResult, error) {
		id := <-runIDCh
		if _, err := h.e.RaiseQuestion(id, Question{Type: "clarification", Question: "?", Multi: true}); err == nil {
			t.Error("multi without options accepted")
		}
		p, err := h.e.RaiseQuestion(id, Question{Type: "clarification", Question: "Which tests may I delete?", Multi: true,
			Options: []event.Option{{ID: "t1", Label: "TestOld", Detail: "flaky since March"}, {ID: "t2", Label: "TestLegacy"}, {ID: "other"}}})
		if err != nil {
			return ports.AgentResult{}, err
		}
		if p.AnswerKind != event.AnswerMulti || len(p.Options) != 2 || p.Options[0].Detail != "flaky since March" {
			t.Errorf("ping = kind %q options %+v", p.AnswerKind, p.Options)
		}
		if _, ok := p.Await(ctx); ok {
			got <- ComposeAnswer(p)
		}
		return ports.AgentResult{TurnsUsed: 1, Session: "s"}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID
	p := h.waitPing(t, "clarification")
	if p.AnswerKind != event.AnswerMulti {
		t.Fatalf("pending ping kind = %q", p.AnswerKind)
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "x"}); err == nil {
		t.Error("an unknown id was accepted")
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "t1\nt2", Feedback: "both are dead"}); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-got:
		if !strings.Contains(text, "selected: t1, t2") || !strings.Contains(text, "both are dead") {
			t.Errorf("tool return = %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the agent never got its answer")
	}
	h.e.Wait(runID)
}

// Events from before the field decode with the kind inferred, and string
// options become {id, label}.
func TestPingRaisedKindIsInferredForOldEvents(t *testing.T) {
	for _, c := range []struct {
		json string
		kind string
	}{
		{`{"ping_id":"p","type":"gate","question":"ok?"}`, event.AnswerYesNo},
		{`{"ping_id":"p","type":"clarification","question":"which?","options":["a","b"]}`, event.AnswerChoice},
		{`{"ping_id":"p","type":"clarification","question":"why?"}`, event.AnswerText},
		{`{"ping_id":"p","type":"select","question":"pick","options":["d1 — a.go"]}`, event.AnswerMulti},
		{`{"ping_id":"p","type":"clarification","question":"q","options":[{"id":"x","label":"X","detail":"d"}],"answer_kind":"multi"}`, event.AnswerMulti},
	} {
		p, err := event.UnmarshalPayload(event.TypePingRaised, []byte(c.json))
		if err != nil {
			t.Fatal(err)
		}
		r := p.(*event.PingRaised)
		if r.Kind() != c.kind {
			t.Errorf("%s: kind %q, want %q", c.json, r.Kind(), c.kind)
		}
		if len(r.Options) > 0 && (r.Options[0].ID == "" || r.Options[0].Label == "") {
			t.Errorf("%s: option %+v", c.json, r.Options[0])
		}
	}
}
