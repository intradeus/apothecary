package flow

import (
	"encoding/json"
	"strings"
	"testing"
)

// schemaDoc is a document with one schema, a writer of it and a reader.
// Each case swaps one part to break a single rule: an agent's declaration
// (writerOut, readerIn), a stage's port (writePort, readPort), or the stages.
type schemaDoc struct {
	schemas, writerOut, readerIn, writePort, readPort, stages string
}

func (d schemaDoc) render() string {
	if d.schemas == "" {
		d.schemas = `schemas:
  - name: report
    description: what the tests found
    json_schema:
      type: object
      properties:
        passed: { type: integer, minimum: 0 }
        failures: { type: array, items: { type: string } }
      required: [passed]
      additionalProperties: false
`
	}
	if d.writerOut == "" {
		d.writerOut = `{ name: report, schema: report }`
	}
	if d.readerIn == "" {
		d.readerIn = `{ name: report, schema: report }`
	}
	if d.writePort == "" {
		d.writePort = `{ to: artifacts/report.json, schema: report }`
	}
	if d.readPort == "" {
		d.readPort = `{ from: artifacts/report.json, schema: report }`
	}
	if d.stages == "" {
		d.stages = `  - { id: write, type: agent, harness: $harness, model: $model, agent: writer, output: { report: ` + d.writePort + ` } }
  - { id: read,  type: agent, harness: $harness, model: $model, agent: reader, input: { report: ` + d.readPort + ` } }
`
	}
	return header + d.schemas + `agents:
  - name: writer
    prompt: write the report
    outputs:
      - ` + d.writerOut + `
  - name: reader
    prompt: read the report
    inputs:
      - ` + d.readerIn + `
  - name: committer
    prompt: write the message
    outputs:
      - { name: commit, type: commit_message }
main:
` + d.stages
}

func TestSchemasParse(t *testing.T) {
	f, err := Parse("t.yaml", []byte(schemaDoc{}.render()))
	if err != nil {
		t.Fatal(err)
	}
	s := f.Schema("report")
	if s == nil || s.Description != "what the tests found" {
		t.Fatalf("schema not built: %+v", s)
	}
	// The prompt shows the schema as written: document order, not sorted.
	if got := string(s.Source); !strings.HasPrefix(got, `{"type":"object","properties":{"passed"`) {
		t.Errorf("source lost its order: %s", got)
	}
	if out := f.PortSchemas(*f.Stage("write").Output("report")); len(out) != 1 || out[0] != s {
		t.Errorf("writer's port schemas = %v", out)
	}
	if out := f.PortSchemas(Port{Type: "commit_message"}); len(out) != 1 || out[0] != TypeSchema("commit_message") {
		t.Errorf("a commit_message port's shape is the built-in schema, got %v", out)
	}
	// A type with a schema is checked against both, the type's shape first.
	both := f.PortSchemas(Port{Type: "commit_message", Schema: "report"})
	if len(both) != 2 || both[0] != TypeSchema("commit_message") || both[1] != s {
		t.Errorf("a commit_message with a schema = %v", both)
	}
	for _, w := range f.Warnings {
		if strings.Contains(w, "schema") {
			t.Errorf("unexpected warning: %s", w)
		}
	}
}

func TestSchemaCheck(t *testing.T) {
	f, err := Parse("t.yaml", []byte(schemaDoc{}.render()))
	if err != nil {
		t.Fatal(err)
	}
	s := f.Schema("report")
	ok := []struct{ path, data string }{
		{"artifacts/report.json", `{"passed": 3, "failures": []}`},
		{"artifacts/report.yaml", "passed: 3\nfailures: [a, b]\n"},
		{"artifacts/report.yml", "passed: 0\n"},
	}
	for _, c := range ok {
		if err := s.Check(c.path, []byte(c.data)); err != nil {
			t.Errorf("%s: %v", c.path, err)
		}
	}
	bad := []struct{ path, data, want string }{
		{"artifacts/report.json", `{"passed": -1, "extra": true}`, "at /passed"},
		{"artifacts/report.json", `{"passed": -1, "extra": true}`, "extra"},
		{"artifacts/report.json", `{"failures": [1]}`, "at /failures/0"},
		{"artifacts/report.json", `{"passed": 1`, "not valid JSON"},
		{"artifacts/report.yaml", "passed: [\n", "not valid YAML"},
	}
	for _, c := range bad {
		err := s.Check(c.path, []byte(c.data))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %s: want error containing %q, got %v", c.path, c.data, c.want, err)
		}
	}
}

func TestCommitMessageSchema(t *testing.T) {
	s := TypeSchema("commit_message")
	for _, data := range []string{
		`{"subject": "feat: add widgets"}`,
		`{"subject": "Fix the thing", "body": "Because it was broken.\n\nTwice."}`,
	} {
		if err := s.Check("artifacts/commit.json", []byte(data)); err != nil {
			t.Errorf("%s: %v", data, err)
		}
	}
	for _, data := range []string{
		`{}`,
		`{"subject": ""}`,
		`{"subject": "   "}`,
		`{"subject": "two\nlines"}`,
		`{"subject": "ok", "trailer": "x"}`,
		`{"subject": "` + strings.Repeat("x", 101) + `"}`,
	} {
		if err := s.Check("artifacts/commit.json", []byte(data)); err == nil {
			t.Errorf("%s: accepted", data)
		}
	}
}

func TestSchemaRules(t *testing.T) {
	twoSchemas := "schemas:\n  - { name: report, json_schema: {} }\n  - { name: other, json_schema: {} }\n"
	tests := []struct {
		name string
		doc  schemaDoc
		want string // error substring
	}{
		{name: "unknown schema on an agent's output",
			doc:  schemaDoc{writerOut: `{ name: report, schema: nope }`},
			want: `output "report": schema "nope" is not declared in this flow`},
		{name: "unknown schema on an agent's input",
			doc:  schemaDoc{readerIn: `{ name: report, schema: nope }`},
			want: `input "report": schema "nope" is not declared`},
		{name: "a type with a schema still needs a structured path",
			doc: schemaDoc{
				writerOut: `{ name: report, type: verdict, schema: report }`,
				writePort: `{ to: artifacts/report.md, type: verdict, schema: report }`},
			want: "must end in .json, .yaml or .yml"},
		{name: "a typed reader needs the type, whatever the schema",
			doc: schemaDoc{stages: `  - { id: write,  type: agent, harness: $harness, model: $model, agent: writer, output: { report: { to: artifacts/report.json, schema: report } } }
  - { id: commit, type: action, action: commit, input: { message: { from: artifacts/report.json, type: commit_message } } }
`},
			want: `stage "write" writes "artifacts/report.json" as schema "report"; the port reads a commit_message`},
		// An agent's input says what it reads; the stage's port cannot read
		// something else in its place.
		{name: "a stage port against its agent's schema",
			doc: schemaDoc{
				schemas:  twoSchemas,
				readPort: `{ from: artifacts/report.json, schema: other }`},
			want: `input "report": agent "reader" reads schema "report", the port says schema "other"`},
		{name: "a writer of another schema",
			doc: schemaDoc{
				schemas:   twoSchemas,
				writerOut: `{ name: report, schema: other }`,
				writePort: `{ to: artifacts/report.json, schema: other }`},
			want: `stage "write" writes "artifacts/report.json" as schema "other"; the port reads schema "report"`},
		{name: "a command reader and an agent writer that disagree",
			doc: schemaDoc{
				schemas:   twoSchemas,
				writerOut: `{ name: report, schema: other }`,
				stages: `  - { id: write, type: agent, harness: $harness, model: $model, agent: writer, output: { report: { to: artifacts/report.json, schema: other } } }
  - { id: use,   type: command, run: "true", input: { report: { from: artifacts/report.json, schema: report } } }
`},
			want: `stage "use": input "report": stage "write" writes "artifacts/report.json" as schema "other"; the port reads schema "report"`},
		{name: "a writer without the schema",
			doc: schemaDoc{
				writerOut: `{ name: report, type: file }`,
				writePort: `{ to: artifacts/report.json, type: file }`},
			want: `stage "write" writes "artifacts/report.json" as a file; the port reads schema "report"`},
		{name: "a command writer without the schema",
			doc: schemaDoc{stages: `  - { id: make, type: command, run: "true", output: { report: { to: artifacts/report.json, type: file } } }
  - { id: read, type: agent, harness: $harness, model: $model, agent: reader, input: { report: { from: artifacts/report.json, schema: report } } }
`},
			want: `stage "make" writes "artifacts/report.json" as a file; the port reads schema "report"`},
		{name: "a schema on a value the engine provides",
			doc:  schemaDoc{readPort: `{ from: run.git.status, type: changeset, schema: report }`},
			want: "a schema describes a file; a changeset is a value the engine provides"},
		{name: "a malformed schema",
			doc:  schemaDoc{schemas: "schemas:\n  - { name: report, json_schema: { type: 12 } }\n"},
			want: `schema "report": json_schema is not a valid JSON Schema`},
		{name: "a schema is required",
			doc:  schemaDoc{schemas: "schemas:\n  - { name: report }\n"},
			want: "json_schema is required"},
		{name: "duplicate schema",
			doc:  schemaDoc{schemas: "schemas:\n  - { name: report, json_schema: {} }\n  - { name: report, json_schema: {} }\n"},
			want: `schema "report": declared twice`},
		{name: "a ref never loads",
			doc:  schemaDoc{schemas: "schemas:\n  - { name: report, json_schema: { $ref: 'file:///etc/passwd' } }\n"},
			want: "not a valid JSON Schema"},
		{name: "a ref never fetches",
			doc:  schemaDoc{schemas: "schemas:\n  - { name: report, json_schema: { $ref: 'https://example.com/s.json' } }\n"},
			want: "not a valid JSON Schema"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("t.yaml", []byte(tt.doc.render()))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestSchemaRulesAccept(t *testing.T) {
	for name, doc := range map[string]schemaDoc{
		// A reader of any file reads whatever structure the writer declares.
		"a file reader": {readerIn: `{ name: report, type: file }`, readPort: `{ from: artifacts/report.json, type: file }`},
		// A stage may narrow an agent's file output with a schema of the
		// flow's, and a reader of that schema takes it.
		"a stage narrows a file output": {writerOut: `{ name: report, type: file }`},
		"the commit action reads a commit_message": {stages: `  - { id: msg,    type: agent, harness: $harness, model: $model, agent: committer, output: { commit: { to: artifacts/commit.json, type: commit_message } } }
  - { id: commit, type: action, action: commit, input: { message: { from: artifacts/commit.json, type: commit_message } } }
`},
		// A command states its structure on its port like any stage, and one
		// command hands a structure to another.
		"command to agent": {stages: `  - { id: write, type: command, run: "true", output: { report: { to: artifacts/report.json, schema: report } } }
  - { id: read,  type: agent, harness: $harness, model: $model, agent: reader, input: { report: { from: artifacts/report.json, schema: report } } }
`},
		"command to command": {stages: `  - { id: make, type: command, run: "true", output: { report: { to: artifacts/report.json, schema: report } } }
  - { id: use,  type: command, run: "true", input: { report: { from: artifacts/report.json, schema: report } } }
`},
		"a command writes a commit_message": {stages: `  - { id: make,   type: command, run: "true", output: { message: { to: artifacts/commit.json, type: commit_message } } }
  - { id: commit, type: action, action: commit, input: { message: { from: artifacts/commit.json, type: commit_message } } }
`},
		// A file of the repository is checked when it is read.
		"a repository file": {stages: `  - { id: use, type: command, run: "true", input: { report: { from: repo:docs/report.json, schema: report } } }
`},
		// A writer may promise more than a reader asks: a commit_message with
		// a schema of the flow's still feeds the commit action.
		"a typed writer with a schema": {
			schemas:   "schemas:\n  - { name: report, json_schema: {} }\n  - { name: conventional, json_schema: { properties: { subject: { pattern: '^(feat|fix|chore)' } } } }\n",
			writerOut: `{ name: report, type: commit_message, schema: conventional }`,
			readerIn:  `{ name: report, type: file }`,
			stages: `  - { id: write,  type: agent, harness: $harness, model: $model, agent: writer, output: { report: { to: artifacts/commit.json, type: commit_message, schema: conventional } } }
  - { id: commit, type: action, action: commit, input: { message: { from: artifacts/commit.json, type: commit_message } } }
`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse("t.yaml", []byte(doc.render())); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnusedSchemaWarns(t *testing.T) {
	doc := schemaDoc{schemas: `schemas:
  - { name: report, json_schema: {} }
  - { name: spare, json_schema: {} }
`}
	f, err := Parse("t.yaml", []byte(doc.render()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.Warnings, "\n"), `schema "spare" is declared but no input or output names it`) {
		t.Errorf("warnings: %v", f.Warnings)
	}
}

// The editor receives a schema in the order it was written, and the JSON
// it receives is the JSON the YAML says.
func TestDocSchemaJSON(t *testing.T) {
	doc, err := ParseDoc("t.yaml", []byte(schemaDoc{}.render()))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(doc.Schemas)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"name":"report","description":"what the tests found","json_schema":{"type":"object","properties":{"passed":{"type":"integer","minimum":0},"failures":{"type":"array","items":{"type":"string"}}},"required":["passed"],"additionalProperties":false}}]`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}
