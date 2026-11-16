package flow

import (
	"strings"
	"testing"
)

// notesDoc: a builder whose notes are an optional output, and a reader of
// them.
const notesDoc = `name: t
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: builder
    prompt: build
    outputs:
      - { name: notes, type: journal_notes, description: "what you did differently" }
  - name: reader
    prompt: read
    inputs:
      - { name: notes, type: journal_notes, optional: true }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder,
      output: { notes: { to: artifacts/journal-notes.yaml, type: journal_notes, optional: true } } }
`

// An output may be optional: the stage may leave it unwritten, so a reader of
// it reads it optional too — a required read is refused, naming why.
func TestOptionalOutputs(t *testing.T) {
	f, err := Parse("t.yaml", []byte(notesDoc+
		"  - { id: read, type: agent, harness: $harness, model: $model, agent: reader, input: { notes: { from: artifacts/journal-notes.yaml, type: journal_notes, optional: true } } }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if out := f.Stage("build").Output("notes"); out == nil || !out.Optional || out.Type != "journal_notes" {
		t.Errorf("notes port = %+v", out)
	}
	_, err = Parse("t.yaml", []byte(strings.Replace(notesDoc, "optional: true }\n", "}\n", 1)+
		"  - { id: read, type: agent, harness: $harness, model: $model, agent: reader, input: { notes: { from: artifacts/journal-notes.yaml, type: journal_notes } } }\n"))
	if err == nil || !strings.Contains(err.Error(), `stage "build"'s output "notes" is optional`) {
		t.Errorf("a required read of an optional output: %v", err)
	}
	// A command's output may be optional too.
	if _, err := Parse("t.yaml", []byte(notesDoc+"  - { id: c, type: command, run: \"true\", output: { log: { to: artifacts/c.log, type: file, optional: true } } }\n")); err != nil {
		t.Errorf("a command's optional output: %v", err)
	}
}

// A journal_notes file is its notes key over a list of {did, why, files?},
// empty or null when there is none, checked by its built-in schema.
func TestJournalNotesSchema(t *testing.T) {
	s := TypeSchema("journal_notes")
	if s == nil {
		t.Fatal("no schema for journal_notes")
	}
	for _, c := range []struct {
		doc string
		ok  bool
	}{
		{"notes:\n  - { did: kept the old endpoint, why: the client still calls it, files: [api.go] }\n", true},
		{"notes: [{ did: x, why: y }]\n", true},
		{"notes: []\n", true},
		{"notes:\n", true},
		{"", false},
		{"{}\n", false},
		{"[]\n", false},
		{"- { did: x, why: y }\n", false},
		{"notes: [{ did: x }]\n", false},
		{"notes: [{ did: x, why: y, extra: z }]\n", false},
		{"notes: []\nsummary: x\n", false},
		{"notes: { did: x, why: y }\n", false},
	} {
		if err := s.Check("artifacts/journal-notes.yaml", []byte(c.doc)); (err == nil) != c.ok {
			t.Errorf("%q: %v, want ok=%v", c.doc, err, c.ok)
		}
	}
	// JSON says it the same way.
	if s.Check("artifacts/notes.json", []byte(`{"notes": []}`)) != nil || s.Check("artifacts/notes.json", []byte("[]")) == nil {
		t.Error(`a JSON notes file: {"notes": []} is no note, a bare list is not notes`)
	}
}
