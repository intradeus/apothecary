package flow

import (
	"encoding/json"
	"strings"
	"testing"
)

// commentsDoc is a flow whose agent reads the ticket's comments; stage is
// the agent stage's input line for them.
func commentsDoc(port string) string {
	return `name: t
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: planner
    prompt: plan
    inputs:
      - { name: ticket, type: ticket }
      - { name: discussion, type: ticket_comments, optional: true }
main:
  - id: plan
    type: agent
    harness: $harness
    model: $model
    agent: planner
    input:
      ticket: { from: run.ticket, type: ticket }
` + port
}

// run.ticket.comments is a ticket_comments value: the latest 20 comments,
// the run's own left out, unless the port says otherwise.
func TestTicketCommentsInput(t *testing.T) {
	for _, tc := range []struct {
		port      string
		limit     int
		exclusion bool
	}{
		{"      discussion: { from: run.ticket.comments, type: ticket_comments }\n", DefaultCommentsLimit, true},
		{"      discussion: { from: run.ticket.comments, type: ticket_comments, limit: 5 }\n", 5, true},
		{"      discussion: { from: run.ticket.comments, type: ticket_comments, limit: 200, exclude_own: false }\n", 200, false},
	} {
		f, err := Parse("t.yaml", []byte(commentsDoc(tc.port)))
		if err != nil {
			t.Fatalf("%s: %v", tc.port, err)
		}
		in := f.Stage("plan").Input("discussion")
		if in == nil || in.Value != SourceTicketComments || in.Type != "ticket_comments" || in.Limit != tc.limit || in.ExcludeOwn != tc.exclusion {
			t.Errorf("%s: port = %+v", tc.port, in)
		}
		if tk := f.Stage("plan").Input("ticket"); tk.Limit != 0 || tk.ExcludeOwn {
			t.Errorf("a ticket port has no comment options: %+v", tk)
		}
	}
}

func TestTicketCommentsInputRefusals(t *testing.T) {
	cmd := "  - id: c\n    type: command\n    run: \"true\"\n"
	for _, tc := range []struct{ name, doc, want string }{
		{"the wrong type", commentsDoc("      discussion: { from: run.ticket.comments, type: ticket }\n"),
			"run.ticket.comments is a ticket_comments — type: ticket_comments"},
		{"a zero limit", commentsDoc("      discussion: { from: run.ticket.comments, type: ticket_comments, limit: 0 }\n"),
			"limit is how many of the latest comments the prompt shows: 1 to 200"},
		{"a limit past the cap", commentsDoc("      discussion: { from: run.ticket.comments, type: ticket_comments, limit: 201 }\n"),
			"1 to 200"},
		{"a limit not a number", commentsDoc("      discussion: { from: run.ticket.comments, type: ticket_comments, limit: many }\n"),
			"limit is a whole number"},
		{"exclude_own not a bool", commentsDoc("      discussion: { from: run.ticket.comments, type: ticket_comments, exclude_own: maybe }\n"),
			"exclude_own is true or false"},
		{"options on another source", strings.Replace(commentsDoc("      discussion: { from: run.ticket.comments, type: ticket_comments }\n"), "ticket: { from: run.ticket, type: ticket }", "ticket: { from: run.ticket, type: ticket, limit: 3 }", 1),
			"limit and exclude_own are a run.ticket.comments input's, not run.ticket's"},
		{"options on an output", commentsDoc("      discussion: { from: run.ticket.comments, type: ticket_comments }\n") + cmd + "    output: { o: { to: a.md, type: file, exclude_own: true } }\n",
			"limit and exclude_own are a run.ticket.comments input's"},
		{"a file of comments", commentsDoc("      discussion: { from: repo:c.yaml, type: ticket_comments }\n"),
			"a ticket_comments is a value the engine provides, not a file: from: run.ticket.comments"},
		{"a command reading them", commentsDoc("") + cmd + "    input: { d: { from: run.ticket.comments, type: ticket_comments } }\n",
			"a command reads files; run.ticket.comments is a value the engine provides"},
		{"a gate shown them", commentsDoc("") + "  - id: g\n    type: human_gate\n    input: { d: { from: run.ticket.comments, type: ticket_comments } }\n",
			"a gate shows a file or a link; run.ticket.comments is a ticket_comments"},
		{"written", commentsDoc("") + cmd + "    output: { d: { to: a.yaml, type: ticket_comments } }\n",
			"what is written is a file"},
	} {
		if _, err := Parse("t.yaml", []byte(tc.doc)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

// The options survive the editor's round trip.
func TestTicketCommentsOptionsRoundTrip(t *testing.T) {
	doc, err := ParseDoc("t.yaml", []byte(commentsDoc("      discussion: { from: run.ticket.comments, type: ticket_comments, limit: 3, exclude_own: false }\n")))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(doc.Main[0].Input)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ticket":{"from":"run.ticket","type":"ticket"},"discussion":{"from":"run.ticket.comments","type":"ticket_comments","limit":3,"exclude_own":false}}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
	var back DocPorts
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if p := back.Pairs[1].Port; p.Limit == nil || *p.Limit != 3 || p.ExcludeOwn == nil || *p.ExcludeOwn {
		t.Errorf("round trip = %+v", p)
	}
}
