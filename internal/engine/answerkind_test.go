package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports"
)

func ptr[T any](v T) *T { return &v }

// textForm is a question asked without fields: one free-text answer.
func textForm() *event.Form {
	return &event.Form{Fields: []event.Field{{ID: AnswerField, Type: event.FieldString, Required: true}}}
}

// textAnswer answers it.
func textAnswer(s string) PingAnswer {
	return PingAnswer{Answer: event.FormAccept, Content: map[string]any{AnswerField: s}}
}

// testForm has one field of every kind.
func testForm() *event.Form {
	return &event.Form{Fields: []event.Field{
		{ID: "db", Type: event.FieldString, Required: true, Options: []event.Option{{ID: "pg", Label: "Postgres"}, {ID: "sqlite", Label: "SQLite"}}},
		{ID: "tests", Type: event.FieldArray, Options: []event.Option{{ID: "t1", Label: "t1"}, {ID: "t2", Label: "t2"}}, MaxItems: ptr(1)},
		{ID: "why", Type: event.FieldString, MinLength: ptr(3), Pattern: "^[a-z ]+$"},
		{ID: "mail", Type: event.FieldString, Format: "email"},
		{ID: "n", Type: event.FieldInteger, Minimum: ptr(1.0), Maximum: ptr(5.0)},
		{ID: "ok", Type: event.FieldBoolean},
	}}
}

// content decodes as the API does: JSON numbers are float64, lists []any.
func content(t *testing.T, js string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// The answer shape is declared on the ping and validated from one table:
// a yes/no takes approve|reject; a form accept with valid fields, or
// decline with a note.
func TestValidateAnswerByKind(t *testing.T) {
	cases := []struct {
		name, kind, answer, feedback, content string
		ok                                    bool
	}{
		{"approve", event.AnswerYesNo, "approve", "", "", true},
		{"reject", event.AnswerYesNo, "reject", "why", "", true},
		{"yes_no other word", event.AnswerYesNo, "accept", "", "", false},
		{"choice", event.AnswerForm, "accept", "", `{"db":"pg"}`, true},
		{"every field", event.AnswerForm, "accept", "", `{"db":"sqlite","tests":["t2"],"why":"it is small","mail":"a@b.c","n":3,"ok":true}`, true},
		{"required missing", event.AnswerForm, "accept", "", `{"tests":[]}`, false},
		{"required blank", event.AnswerForm, "accept", "", `{"db":"  "}`, false},
		{"not an option", event.AnswerForm, "accept", "", `{"db":"mysql"}`, false},
		{"multi not an option", event.AnswerForm, "accept", "", `{"db":"pg","tests":["t9"]}`, false},
		{"multi none", event.AnswerForm, "accept", "", `{"db":"pg","tests":[]}`, true},
		{"multi too many", event.AnswerForm, "accept", "", `{"db":"pg","tests":["t1","t2"]}`, false},
		{"too short", event.AnswerForm, "accept", "", `{"db":"pg","why":"no"}`, false},
		{"pattern", event.AnswerForm, "accept", "", `{"db":"pg","why":"NOPE!"}`, false},
		{"format", event.AnswerForm, "accept", "", `{"db":"pg","mail":"not mail"}`, false},
		{"integer", event.AnswerForm, "accept", "", `{"db":"pg","n":2.5}`, false},
		{"maximum", event.AnswerForm, "accept", "", `{"db":"pg","n":6}`, false},
		{"boolean type", event.AnswerForm, "accept", "", `{"db":"pg","ok":"yes"}`, false},
		{"unknown field", event.AnswerForm, "accept", "", `{"db":"pg","zz":1}`, false},
		{"decline with note", event.AnswerForm, "decline", "none of these fit", "", true},
		{"decline without note", event.AnswerForm, "decline", " ", "", false},
		{"form other word", event.AnswerForm, "other", "x", "", false},
	}
	for _, c := range cases {
		ans := PingAnswer{Answer: c.answer, Feedback: c.feedback}
		if c.content != "" {
			ans.Content = content(t, c.content)
		}
		err := validateAnswer(c.kind, testForm(), &ans)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// Accepting tidies the content: strings trimmed, blank optional fields and
// repeated picks dropped.
func TestValidateFormTidiesContent(t *testing.T) {
	ans := PingAnswer{Answer: "accept", Content: content(t, `{"db":" pg ","why":"","tests":["t1","t1"]}`)}
	f := testForm()
	f.Fields[1].MaxItems = nil
	if err := validateAnswer(event.AnswerForm, f, &ans); err != nil {
		t.Fatal(err)
	}
	if ans.Content["db"] != "pg" {
		t.Errorf("db = %q", ans.Content["db"])
	}
	if _, ok := ans.Content["why"]; ok {
		t.Error("a blank optional field was kept")
	}
	if got := ans.Content["tests"].([]any); len(got) != 1 {
		t.Errorf("tests = %v", got)
	}
}

// An asked form is checked and tidied; what cannot be enforced is dropped
// rather than failing a harness's question.
func TestNormalizeForm(t *testing.T) {
	bad := []event.Field{
		{ID: "", Type: event.FieldString},
		{ID: "a", Type: "object"},
		{ID: "a", Type: event.FieldArray},
		{ID: "a", Type: event.FieldNumber, Options: []event.Option{{ID: "x"}}},
		{ID: "a", Type: event.FieldString, Options: []event.Option{{ID: ""}}},
	}
	for _, f := range bad {
		if err := normalizeForm(&event.Form{Fields: []event.Field{f}}); err == nil {
			t.Errorf("%+v accepted", f)
		}
	}
	if err := normalizeForm(&event.Form{Fields: []event.Field{{ID: "a", Type: "string"}, {ID: "a", Type: "string"}}}); err == nil {
		t.Error("a repeated id was accepted")
	}
	if err := normalizeForm(&event.Form{}); err == nil {
		t.Error("an empty form was accepted")
	}
	f := &event.Form{Fields: []event.Field{{ID: " x ", Type: event.FieldString, Pattern: "(?<=lookbehind)", Format: "phone", Default: 3.0,
		Options: []event.Option{{ID: "a"}, {ID: "a"}}}}}
	if err := normalizeForm(f); err != nil {
		t.Fatal(err)
	}
	fd := f.Fields[0]
	if fd.ID != "x" || fd.Pattern != "" || fd.Format != "" || fd.Default != nil || len(fd.Options) != 1 || fd.Options[0].Label != "a" {
		t.Errorf("normalized = %+v", fd)
	}
}

// What the agent reads: a lone field's value, several as id: value lines,
// a decline as the human's reason.
func TestComposeFormText(t *testing.T) {
	one := &event.Form{Fields: []event.Field{{ID: AnswerField, Type: event.FieldString}}}
	if got := composeFormText(one, "accept", "", map[string]any{AnswerField: "use sqlite"}); got != "use sqlite" {
		t.Errorf("one field = %q", got)
	}
	got := composeFormText(testForm(), "accept", "be quick", content(t, `{"db":"pg","tests":[],"n":2,"ok":false}`))
	want := "db: pg\ntests: none selected\nwhy: (no answer)\nmail: (no answer)\nn: 2\nok: no\n(note: be quick)"
	if got != want {
		t.Errorf("several fields =\n%s\nwant\n%s", got, want)
	}
	if got := composeFormText(testForm(), "decline", "neither — use mysql", nil); !strings.Contains(got, "declined") || !strings.Contains(got, "neither — use mysql") {
		t.Errorf("decline = %q", got)
	}
}

// An agent may ask for a selection: an array field; the tool return names
// the chosen ids.
func TestAskHumanMultiSelect(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	got := make(chan string, 1)
	h.runner.RunFunc = func(ctx context.Context, _ fake.RunCall) (ports.AgentResult, error) {
		id := <-runIDCh
		if _, err := h.e.RaiseQuestion(id, Question{Type: "clarification", Question: "?",
			Fields: []event.Field{{ID: "tests", Type: event.FieldArray}}}); err == nil {
			t.Error("an array field without options accepted")
		}
		p, err := h.e.RaiseQuestion(id, Question{Type: "clarification", Question: "Which tests may I delete?",
			Fields: []event.Field{{ID: "tests", Type: event.FieldArray,
				Options: []event.Option{{ID: "t1", Label: "TestOld", Detail: "flaky since March"}, {ID: "t2", Label: "TestLegacy"}}}}})
		if err != nil {
			return ports.AgentResult{}, err
		}
		if p.AnswerKind != event.AnswerForm || len(p.Form.Fields[0].Options) != 2 || p.Form.Fields[0].Options[0].Detail != "flaky since March" {
			t.Errorf("ping = kind %q form %+v", p.AnswerKind, p.Form)
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
	if p.AnswerKind != event.AnswerForm {
		t.Fatalf("pending ping kind = %q", p.AnswerKind)
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "accept", Content: content(t, `{"tests":["x"]}`)}); err == nil {
		t.Error("an unknown id was accepted")
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "accept", Feedback: "both are dead", Content: content(t, `{"tests":["t1","t2"]}`)}); err != nil {
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

// A question asked without fields is one free-text answer.
func TestAskHumanWithoutFieldsIsFreeText(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	got := make(chan string, 1)
	h.runner.RunFunc = func(ctx context.Context, _ fake.RunCall) (ports.AgentResult, error) {
		p, err := h.e.RaiseQuestion(<-runIDCh, Question{Type: "clarification", Question: "Why?"})
		if err != nil {
			return ports.AgentResult{}, err
		}
		if f := p.Form.Fields; len(f) != 1 || f[0].ID != AnswerField || f[0].Type != event.FieldString || !f[0].Required {
			t.Errorf("form = %+v", p.Form)
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
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "accept", Content: map[string]any{AnswerField: "because"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-got:
		if text != "because" {
			t.Errorf("tool return = %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the agent never got its answer")
	}
	h.e.Wait(runID)
}

// The kind is yes_no for the engine's own decisions, form for any question.
func TestPingRaisedKind(t *testing.T) {
	for _, c := range []struct {
		json string
		kind string
	}{
		{`{"ping_id":"p","type":"gate","question":"ok?"}`, event.AnswerYesNo},
		{`{"ping_id":"p","type":"debug","question":"stuck"}`, event.AnswerYesNo},
		{`{"ping_id":"p","type":"clarification","question":"why?"}`, event.AnswerForm},
		{`{"ping_id":"p","type":"select","question":"pick","answer_kind":"form","form":{"fields":[{"id":"threads","type":"array","options":["d1"]}]}}`, event.AnswerForm},
	} {
		p, err := event.UnmarshalPayload(event.TypePingRaised, []byte(c.json))
		if err != nil {
			t.Fatal(err)
		}
		r := p.(*event.PingRaised)
		if r.Kind() != c.kind {
			t.Errorf("%s: kind %q, want %q", c.json, r.Kind(), c.kind)
		}
		if r.Form != nil && (r.Form.Fields[0].Options[0].ID != "d1" || r.Form.Fields[0].Options[0].Label != "d1") {
			t.Errorf("%s: option %+v", c.json, r.Form.Fields[0].Options[0])
		}
	}
}
