package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// oneStage is a body that passes: most cases below replace one piece of it.
const oneStage = `main:
  - { id: s, type: agent, harness: $harness, model: $model, agent: b, output: { patch: { to: artifacts/p.md, type: file } } }
`

// bAgents declares the agent oneStage runs.
const bAgents = `agents:
  - name: b
    prompt: do b
    outputs:
      - { name: patch, type: file }
`

// agentReadsFailure declares an agent that takes what failed.
const agentReadsFailure = `agents:
  - name: fix
    prompt: fix it
    inputs:
      - { name: failure, type: failure }
`

// A document is refused for one named reason, on the line that holds the
// mistake: that message is all an author gets.
func TestMalformedDocuments(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
		line int
	}{
		// --- the document as a whole ---
		{"not yaml", "name: [", "line 1", 0},
		{"two documents", header + bAgents + oneStage + "---\nname: other\n", `holds one document`, 1},
		{"an unknown top-level key", header + "x: 1\n", `unknown key "x" in the document`, 4},
		{"anchor on a stage", header + bAgents + "main:\n  - &s { id: s, type: command, run: 'true' }\n", "YAML anchors (&s) are not allowed", 10},
		{"alias to a stage", header + bAgents + "main:\n  - &s { id: s, type: command, run: 'true' }\n  - *s\n", "YAML anchors (&s) are not allowed", 10},
		{"bad flow name", "name: Bad Name\nversion: 5\nparameters: { harness: fake, model: fake/model }\n" + bAgents + oneStage, `flow name "Bad Name" is not a slug`, 1},

		// --- parameters ---
		{"parameters not a mapping", "name: t\nversion: 5\nparameters: [a]\n" + bAgents + oneStage, "line 3: parameters must be a mapping", 0},
		{"parameter field twice", "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  x: { type: string, type: int, default: a }\n" + bAgents + oneStage, `line 6: "type" set twice`, 0},
		{"parameter unknown field", "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  x: { type: string, nope: 1 }\n" + bAgents + oneStage, `unknown key "nope" in a parameter`, 6},
		{"parameter is a list", "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  x: [1, 2]\n" + bAgents + oneStage, `line 6: parameter "x" must be a mapping`, 0},
		{"parameter bad type field", "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  x: { type: [a], default: b }\n" + bAgents + oneStage, "line 6", 0},
		{"list default holding a mapping", "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  x: { type: string, list: true, default: [{a: 1}] }\n" + bAgents + oneStage, "line 6: a list default holds values", 0},
		{"list default holding null", "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  x: { type: string, list: true, default: [~] }\n" + bAgents + oneStage, "line 6: a list default holds values", 0},
		{"mapping default", "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  x: { type: string, default: {a: 1} }\n" + bAgents + oneStage, "line 6: a parameter's default is a value, or a list of values", 0},
		{"a bad list item for the type", "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  x: { type: int, list: true, default: [1, nope] }\n" + bAgents + oneStage, "x: default:", 6},
		{"a long description", "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  x: { type: string, default: a, description: " + strings.Repeat("d", 501) + " }\n" + bAgents + oneStage, "description is longer than 500 characters", 6},
		{"duplicate enum values", "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  x: { type: string, default: a, enum: [a, a] }\n" + bAgents + oneStage, "enum values must be distinct and non-empty", 6},
		{"empty enum value", "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  x: { type: string, default: a, enum: [a, ''] }\n" + bAgents + oneStage, "enum values must be distinct and non-empty", 6},
		{"default $ref beyond the enum", "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  a: { type: string, default: x, enum: [x, y] }\n  b: { type: string, default: $a, enum: [x] }\n" + bAgents + oneStage, `default $a allows "y", which is not in its enum`, 7},
		{"default $ref with no enum", "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  a: { type: string, default: x }\n  b: { type: string, default: $a, enum: [x] }\n" + bAgents + oneStage, "default $a needs an enum within x", 7},
		{"zero timeout", "name: t\nversion: 5\nparameters: { harness: fake, model: fake/model, timeout: 0s }\n" + bAgents + oneStage, `parameter "timeout" takes a duration`, 3},
		{"zero max_turns", "name: t\nversion: 5\nparameters: { harness: fake, model: fake/model, max_turns: 0 }\n" + bAgents + oneStage, "max_turns must be > 0, got 0", 3},
		{"zero max_total_turns", "name: t\nversion: 5\nparameters: { harness: fake, model: fake/model, max_total_turns: 0 }\n" + bAgents + oneStage, "max_total_turns must be > 0, got 0", 3},

		// --- agents ---
		{"nameless agent", header + "agents:\n  - prompt: x\n" + oneStage, "agent #1: name is required", 5},
		{"bad agent name", header + "agents:\n  - { name: Bad, prompt: x }\n" + oneStage, "name must match", 5},
		{"promptless agent", header + "agents:\n  - { name: b }\n" + oneStage, "prompt is required", 5},
		{"agent named twice", header + "agents:\n  - { name: b, prompt: x, outputs: [{ name: patch, type: file }] }\n  - { name: b, prompt: y }\n" + oneStage, `agent "b": declared twice (first at line 5)`, 6},
		{"agent lists an mcp twice", header + "mcp_servers:\n  - { name: m, url: 'http://x.test' }\nagents:\n  - { name: b, prompt: x, mcp: [m, m] }\n" + oneStage, `mcp: "m" listed twice`, 7},
		{"agent lists a bad mcp", header + "agents:\n  - { name: b, prompt: x, mcp: [Bad] }\n" + oneStage, `mcp: "Bad" must name one of the flow's mcp_servers`, 5},
		{"agent output schema on files", header + "schemas:\n  - { name: s1, json_schema: { type: object } }\nagents:\n  - { name: b, prompt: x, outputs: [{ name: o, type: files, schema: s1 }] }\n" + oneStage, "a schema describes one file; files is a directory of them", 7},

		// --- stages ---
		{"stage id not a slug", header + bAgents + "main:\n  - { id: Bad, type: agent, agent: b }\n", "not a slug", 0},
		{"agent stage's agent not a slug", header + bAgents + "main:\n  - { id: s, type: agent, agent: Bad }\n", `agent "Bad" is not a slug`, 0},
		{"run_ref not a slug", header + "main:\n  - { id: s, type: command, run_ref: 'Not A Slug' }\n", `run_ref "Not A Slug" is not a slug`, 0},
		{"service stage without a service", header + "main:\n  - { id: s, type: service }\n", "service is required", 0},
		{"service not a slug", header + "main:\n  - { id: s, type: service, service: 'Not A Slug' }\n", `service "Not A Slug" is not a slug`, 0},
		{"service reads a parameter that is not declared", header + "main:\n  - { id: s, type: service, service: $app }\n", "service:", 0},
		{"on_success that goes nowhere", header + "main:\n  - { id: s, type: command, run: 'true', on_success: 'not a path' }\n", `on_success "not a path" must be a stage id`, 0},
		{"edge without a target", header + "main:\n  - { id: s, type: command, run: 'true', on_fail: { max: 2 } }\n", "to is required", 0},
		{"edge that is a list", header + "main:\n  - { id: s, type: command, run: 'true', on_fail: [a] }\n", "edge must be a mapping", 0},
		{"edge's bad field", header + "main:\n  - { id: s, type: command, run: 'true', on_fail: { to: s, nope: 1 } }\n", `unknown key "nope" in a route`, 5},
		{"retired feed on a terminal edge", header + "main:\n  - { id: s, type: command, run: 'true', on_fail: { to: end, feed: x } }\n", `"feed" is retired: read edge.failure from an input port on the target stage instead`, 0},
		{"with is a bare value", header + "main:\n  - { id: s, type: action, action: merge_mr, with: x }\n", `must be a {name: value} mapping, not a bare value — name what "x" is for`, 0},
		{"with is a list", header + "main:\n  - { id: s, type: action, action: merge_mr, with: [x] }\n", "must be a {name: value} mapping, not a list", 0},
		{"with given twice", header + "main:\n  - { id: s, type: action, action: merge_mr, with: { a: 1, a: 2 } }\n", `"a" given twice`, 0},
		{"with holds a mapping", header + "main:\n  - { id: s, type: action, action: merge_mr, with: { a: { b: 1 } } }\n", `"a": value must be a literal or a list of them, not a mapping`, 0},
		{"with holds a nested list", header + "main:\n  - { id: s, type: action, action: merge_mr, with: { a: [[1]] } }\n", `"a": a list holds literals, not mappings or lists`, 0},
		{"max_turns is a list", header + bAgents + "main:\n  - { id: s, type: agent, agent: b, max_turns: [1] }\n", "expected an integer or a $parameter", 0},
		{"max_turns is a word", header + bAgents + "main:\n  - { id: s, type: agent, agent: b, max_turns: lots }\n", `"lots" is not an integer or a $parameter`, 0},
		{"max_turns is a float", header + bAgents + "main:\n  - { id: s, type: agent, agent: b, max_turns: 1.5 }\n", `"1.5" is not an integer or a $parameter`, 0},
		{"max_turns overflows", header + bAgents + "main:\n  - { id: s, type: agent, agent: b, max_turns: 99999999999999999999 }\n", "99999999999999999999", 0},

		// --- watchers and outcomes ---
		{"watcher without an id", header + bAgents + oneStage + "watchers:\n  - { on: manual, stages: [{ id: w, type: command, run: 'true' }] }\n", "id is required", 0},
		{"watcher id not a slug", header + bAgents + oneStage + "watchers:\n  - { id: Bad, on: manual, stages: [{ id: w, type: command, run: 'true' }] }\n", "id is not a slug", 0},
		{"on is a list", header + bAgents + oneStage + "watchers:\n  - { id: w, on: [a], stages: [{ id: w1, type: command, run: 'true' }] }\n", "on must be a slug", 0},
		{"on field twice", header + bAgents + oneStage + "watchers:\n  - { id: w, on: { signal: mr_merged, signal: mr_closed }, stages: [{ id: w1, type: command, run: 'true' }] }\n", `"signal" set twice`, 0},
		{"on's max is a word", header + bAgents + oneStage + "watchers:\n  - { id: w, on: { signal: mr_merged, max: lots }, stages: [{ id: w1, type: command, run: 'true' }] }\n", `"lots" is not an integer or a $parameter`, 0},
		{"quiet_for that does not parse", header + bAgents + oneStage + "watchers:\n  - { id: w, on: { signal: mr_review_settled, quiet_for: soon }, stages: [{ id: w1, type: command, run: 'true' }] }\n", `quiet_for: invalid duration "soon"`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse("t.yaml", []byte(c.doc))
			if err == nil {
				t.Fatalf("accepted:\n%s", c.doc)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %q, want it to say %q", err, c.want)
			}
			var fe *Error
			if !errors.As(err, &fe) {
				t.Fatalf("err is %T, want *flow.Error", err)
			}
			if c.line > 0 && fe.Line != c.line {
				t.Errorf("reported at line %d, want %d: %v", fe.Line, c.line, err)
			}
		})
	}
}

// A bound value breaks the same rules a document's own would, naming that it
// was bound.
func TestBindingsAreValidated(t *testing.T) {
	doc := []byte("name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  timeout: 30m\n  max_turns: 5\n  max_total_turns: 9\n" + bAgents + oneStage)
	cases := []struct {
		bind map[string]string
		want string
	}{
		{map[string]string{"timeout": "-1m"}, `parameter "timeout" takes a duration`},
		{map[string]string{"max_turns": "0"}, "max_turns must be > 0, got 0 (bound)"},
		{map[string]string{"max_total_turns": "-1"}, "max_total_turns must be > 0, got -1 (bound)"},
		{map[string]string{"model": "no-provider"}, `model: "no-provider" (bound) is not of the form provider/model`},
		{map[string]string{"harness": "Not A Slug"}, `harness: "Not A Slug" (bound) is not a slug`},
		{map[string]string{"nope": "1"}, "nope"},
	}
	for _, c := range cases {
		if _, err := ParseBound("t.yaml", doc, c.bind); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v, want %q", c.bind, err, c.want)
		}
	}
	f, err := ParseBound("t.yaml", doc, map[string]string{"max_turns": "7"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Defaults.MaxTurns != 7 || f.Bindings["max_turns"] != "7" {
		t.Errorf("bound flow = %+v, bindings %v", f.Defaults, f.Bindings)
	}
}

func TestDocumentLimits(t *testing.T) {
	big := header + oneStage + "# " + strings.Repeat("x", maxDocBytes) + "\n"
	if _, err := Parse("t.yaml", []byte(big)); err == nil || !strings.Contains(err.Error(), "the limit is 1048576") {
		t.Errorf("oversize document = %v", err)
	}

	var stages strings.Builder
	stages.WriteString(header + "main:\n")
	for i := range maxStages + 1 {
		fmt.Fprintf(&stages, "  - { id: s%d, type: command, run: 'true' }\n", i)
	}
	if _, err := Parse("t.yaml", []byte(stages.String())); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("flow declares %d stages; the limit is %d", maxStages+1, maxStages)) {
		t.Errorf("too many stages = %v", err)
	}

	var agents strings.Builder
	agents.WriteString(header + "agents:\n")
	for i := range maxAgents + 1 {
		fmt.Fprintf(&agents, "  - { name: a%d, prompt: x }\n", i)
	}
	agents.WriteString(oneStage)
	if _, err := Parse("t.yaml", []byte(agents.String())); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("flow declares %d agents; the limit is %d", maxAgents+1, maxAgents)) {
		t.Errorf("too many agents = %v", err)
	}

	var params strings.Builder
	params.WriteString("name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n")
	for i := range maxParams + 1 {
		fmt.Fprintf(&params, "  p%d: { type: string, default: a }\n", i)
	}
	params.WriteString(bAgents + oneStage)
	if _, err := Parse("t.yaml", []byte(params.String())); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("declares more than %d parameters", maxParams)) {
		t.Errorf("too many parameters = %v", err)
	}

	// A sequence used many times stays under the document's count and over
	// the expanded one: the limit is on what runs.
	var seq strings.Builder
	seq.WriteString(header + "sequences:\n  - id: q\n    stages:\n")
	for i := range 20 {
		fmt.Fprintf(&seq, "      - { id: q%d, type: command, run: 'true' }\n", i)
	}
	seq.WriteString("main:\n")
	for i := range 8 {
		fmt.Fprintf(&seq, "  - { id: u%d, sequence: q }\n", i)
	}
	if _, err := Parse("t.yaml", []byte(seq.String())); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("expands to more than %d stages", maxStages)) {
		t.Errorf("expanded past the limit = %v", err)
	}
}

func TestRename(t *testing.T) {
	src := "# a flow\nname: old # keep me\nversion: 5\nmain:\n  - { id: name, type: command, run: 'name: old' }\n"
	out, err := Rename(src, "new-one")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Name != "new-one" {
		t.Errorf("name = %q", doc.Name)
	}
	for _, keep := range []string{"# a flow", "# keep me", "'name: old'", "id: name"} {
		if !strings.Contains(out, keep) {
			t.Errorf("rename lost %q:\n%s", keep, out)
		}
	}
	// A name that would read as another YAML type stays a string.
	out, err = Rename(`name: "quoted"`, "123")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil || doc.Name != "123" {
		t.Errorf("numeric name = %q, %v\n%s", doc.Name, err, out)
	}

	for doc, want := range map[string]string{
		"name: [":      "flow document:",
		"- a\n- b\n":   "top level must be a mapping",
		"version: 5\n": "no top-level name",
		"":             "top level must be a mapping",
	} {
		if _, err := Rename(doc, "x"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Rename(%q) = %v, want %q", doc, err, want)
		}
	}
}

// DocInt and DocIO decode what the editor sends the way the YAML side does,
// refusing the same things.
func TestDocIntJSON(t *testing.T) {
	var d DocInt
	if err := json.Unmarshal([]byte(`12`), &d); err != nil || d.Int != 12 {
		t.Errorf("number = %+v, %v", d, err)
	}
	if out, _ := json.Marshal(DocInt{Int: 12}); string(out) != "12" {
		t.Errorf("marshal = %s", out)
	}
	var r DocInt
	if err := json.Unmarshal([]byte(`"$turns"`), &r); err != nil || r.Ref != "$turns" {
		t.Errorf("ref = %+v, %v", r, err)
	}
	if out, _ := json.Marshal(r); string(out) != `"$turns"` {
		t.Errorf("marshal = %s", out)
	}
	for in, want := range map[string]string{
		`1.5`:    "1.5 is not an integer",
		`"lots"`: `"lots" is not an integer or a $parameter`,
		`true`:   "expected an integer or a $parameter",
		`[`:      "unexpected",
	} {
		var d DocInt
		if err := d.UnmarshalJSON([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", in, err, want)
		}
	}
}

func TestDocIOJSON(t *testing.T) {
	var d DocIO
	if err := json.Unmarshal([]byte(`{"b":"1","a":["x","y"]}`), &d); err != nil {
		t.Fatal(err)
	}
	want := []IOPair{{Name: "b", Value: "1"}, {Name: "a", IsList: true, Items: []string{"x", "y"}}}
	if !reflect.DeepEqual(d.Pairs, want) {
		t.Errorf("pairs = %+v", d.Pairs)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"b":"1","a":["x","y"]}` {
		t.Errorf("order lost: %s", out)
	}
	var empty DocIO
	if err := empty.UnmarshalJSON([]byte(` null `)); err != nil || empty.Pairs != nil {
		t.Errorf("null = %+v, %v", empty, err)
	}
	for in, want := range map[string]string{
		`[1]`:             "expected a JSON object",
		`{"a":{"b":1}}`:   "cannot unmarshal",
		`{"a":[{"b":1}]}`: "cannot unmarshal",
		`{"a"`:            "EOF",
	} {
		var d DocIO
		if err := d.UnmarshalJSON([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", in, err, want)
		}
	}
}

// The parameters block keeps its order through the editor's JSON, and what
// it refuses there it refuses for the same reason.
func TestDocParamsJSONRefusals(t *testing.T) {
	var ok DocParams
	if err := ok.UnmarshalJSON([]byte(` null `)); err != nil || len(ok.Entries) != 0 {
		t.Errorf("null = %+v, %v", ok, err)
	}
	for in, want := range map[string]string{
		`[1]`:                    "parameters: expected a JSON object",
		`{"a":{"type":1}}`:       "cannot unmarshal",
		`{"a":{"default":{}}}`:   "a parameter default is",
		`{"a":{"default":[{}]}}`: "a parameter default is",
		`{`:                      "unexpected end of JSON input",
	} {
		var d DocParams
		if err := d.UnmarshalJSON([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", in, err, want)
		}
	}
}

func TestDocParamValueListDefaultJSON(t *testing.T) {
	var d DocParams
	if err := d.UnmarshalJSON([]byte(`{"a":{"type":"string","list":true,"default":[]},"b":{"type":"int","list":true,"default":[1,2]}}`)); err != nil {
		t.Fatal(err)
	}
	if len(d.Entries) != 2 || d.Entries[0].Name != "a" || d.Entries[1].Name != "b" {
		t.Fatalf("entries = %+v", d.Entries)
	}
	if a := d.Entries[0].Param.Default; !a.IsList || !a.Set || a.Hint != ParamString || len(a.Items) != 0 {
		t.Errorf("empty list default = %+v", a)
	}
	if b := d.Entries[1].Param.Default; !reflect.DeepEqual(b.Items, []string{"1", "2"}) || b.Hint != ParamInt {
		t.Errorf("int list default = %+v", b)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(filepath.Join(dir, "missing.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file = %v", err)
	}
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("name: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("a broken file loaded")
	}
}

func TestParseDocRefusesWhatParseRefuses(t *testing.T) {
	if _, err := ParseDoc("t.yaml", []byte("name: [")); err == nil {
		t.Error("broken document produced a Doc")
	}
	d, err := ParseDoc("t.yaml", []byte(header+bAgents+oneStage))
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "t" || len(d.Main) != 1 {
		t.Errorf("doc = %+v", d)
	}
}

// The blocks around the stages: MCP servers, schemas and sequences, each
// refused for what an author wrote wrong in it.
func TestMalformedBlocks(t *testing.T) {
	const anyStage = "main:\n  - { id: s, type: command, run: 'true' }\n"
	mcp := func(body string) string { return header + "mcp_servers:\n  - " + body + "\n" + anyStage }
	schema := func(body string) string { return header + "schemas:\n  - " + body + "\n" + anyStage }
	seq := func(body string) string {
		return header + "sequences:\n" + body + "main:\n  - { id: m, sequence: q }\n"
	}
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"mcp without a name", mcp("{ url: 'http://x.test' }"), `mcp server "#1": name is required`},
		{"mcp named twice", header + "mcp_servers:\n  - { name: m, url: 'http://x.test' }\n  - { name: m, url: 'http://y.test' }\n" + anyStage, `mcp server "m": declared twice`},
		{"mcp description reads a parameter", mcp("{ name: m, url: 'http://x.test', description: '$harness' }"), `mcp server "m": description:`},
		{"mcp with neither", mcp("{ name: m }"), "exactly one of builtin"},
		{"mcp with both", mcp("{ name: m, url: 'http://x.test', command: [a] }"), "exactly one of builtin"},
		{"unknown builtin", mcp("{ name: m, builtin: nope }"), `unknown builtin "nope"`},
		{"builtin with env", mcp("{ name: m, builtin: browser, env: { A: b } }"), "a builtin is apothecary's: it takes no env or headers"},
		{"blank program", mcp("{ name: m, command: ['  '] }"), "command: the program is required"},
		{"command with headers", mcp("{ name: m, command: [a], headers: { X: y } }"), "headers are a url server's; a command takes env"},
		{"url of another scheme", mcp("{ name: m, url: 'ftp://x.test' }"), `url "ftp://x.test" must be an http(s) URL`},
		{"url without a host", mcp("{ name: m, url: 'http://' }"), "must be an http(s) URL"},
		{"url with env", mcp("{ name: m, url: 'http://x.test', env: { A: b } }"), "env is a command's; a url server takes headers"},
		{"secret in a command", mcp("{ name: m, command: ['a', '${secrets.token}'] }"), "a secret is read in env or headers only"},
		{"secret in a url", mcp("{ name: m, url: 'http://x.test/${secrets.token}' }"), "a secret is read in env or headers only"},
		{"bad env name", mcp("{ name: m, command: [a], env: { 'not valid': b } }"), `env "not valid" is not a valid name`},
		{"bad header name", mcp("{ name: m, url: 'http://x.test', headers: { 'not valid': b } }"), `headers "not valid" is not a valid name`},
		{"bad secret name", mcp("{ name: m, command: [a], env: { A: '${secrets.Bad}' } }"), `env "A": ${secrets.Bad} must name a project secret`},

		{"schema name", schema("{ name: Bad, json_schema: { type: object } }"), `schema #1: name must match`},
		{"schema description reads a parameter", schema("{ name: s1, description: '$harness', json_schema: { type: object } }"), `schema "s1": description:`},
		{"schema with an integer key", schema("{ name: s1, json_schema: { 1: x } }"), `schema "s1": json_schema: line`},
		{"schema with a not-a-number", schema("{ name: s1, json_schema: { minimum: .nan } }"), "is not a JSON number"},
		{"schema with an infinity", schema("{ name: s1, json_schema: { maximum: .inf } }"), "is not a JSON number"},

		{"sequence without an id", seq("  - { stages: [{ id: a, type: command, run: 'true' }] }\n"), `sequence "#1": id is required`},
		{"sequence id not a slug", seq("  - { id: Bad, stages: [{ id: a, type: command, run: 'true' }] }\n"), `sequence "Bad": id is not a slug`},
		{"sequence stage retired", header + "sequences:\n  - { id: q, stages: [{ id: a, type: command, run: 'true' }] }\nmain:\n  - { id: m, sequence: q, feed: x }\n", ""},
		{"sequence stage id missing", header + "sequences:\n  - { id: q, stages: [{ id: a, type: command, run: 'true' }] }\nmain:\n  - { sequence: q }\n", `stage "#1": id is required`},
		{"sequence stage named twice", header + "sequences:\n  - { id: q, stages: [{ id: a, type: command, run: 'true' }] }\nmain:\n  - { id: m, sequence: q }\n  - { id: m, sequence: q }\n", "duplicate id (first declared at line 7)"},
		{"a plain stage after a sequence stage of its id", header + "sequences:\n  - { id: q, stages: [{ id: a, type: command, run: 'true' }] }\nmain:\n  - { id: m, sequence: q }\n  - { id: m, type: command, run: 'true' }\n", `duplicate id (first declared at line 7)`},
		{"a sequence stage naming nothing", header + "main:\n  - { id: m, sequence: q }\n", "no sequence of that name (sequences: main)"},

		{"port field twice", header + "main:\n  - { id: s, type: command, run: 'true', input: { a: { from: run.ticket, from: run.ticket, type: ticket } } }\n", `port "a": "from" given twice`},
		{"port optional not a bool", header + "main:\n  - { id: s, type: command, run: 'true', input: { a: { from: run.ticket, type: ticket, optional: maybe } } }\n", `port "a": optional is true or false`},
		{"input without from", header + "main:\n  - { id: s, type: command, run: 'true', input: { a: { type: ticket } } }\n", "from is required"},
		{"output without to", header + "main:\n  - { id: s, type: command, run: 'true', output: { a: { type: file } } }\n", "to is required: the path it is written to"},
		{"an action's output of the wrong type", header + "main:\n  - { id: s, type: action, action: load_review_comments, output: { threads: { to: artifacts/d/, type: files } } }\n", `output "threads": action "load_review_comments" writes`},
		{"an agent's output of the wrong type", header + bAgents + "main:\n  - { id: s, type: agent, harness: $harness, model: $model, agent: b, output: { patch: { to: artifacts/d/, type: files } } }\n", `output "patch": agent "b" writes`},
		{"an agent's output left unstated", header + "schemas:\n  - { name: s1, json_schema: { type: object } }\nagents:\n  - { name: b, prompt: x, outputs: [{ name: o, schema: s1 }] }\nmain:\n  - { id: s, type: agent, harness: $harness, model: $model, agent: b }\n", "schema: s1"},
		{"a ci failure outside a ci watcher", header + agentReadsFailure + "main:\n  - { id: s, type: agent, harness: $harness, model: $model, agent: fix, input: { failure: { from: run.ci.failure, type: failure } } }\n", "is what failed when a ci_failed watcher opened"},
		{"a command reading the ci failure", header + "main:\n  - { id: s, type: command, run: 'true', input: { a: { from: run.ci.failure, type: failure } } }\n", "a command reads files; run.ci.failure is a value the engine provides"},
		{"a list given to a one-value setting", header + "main:\n  - { id: s, type: action, action: mr_merge, with: { method: [a, b] } }\n", `with "method" takes one value, not a list`},
		{"a setting's parameter of the wrong shape", "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  ls: { type: string, default: x }\nmain:\n  - { id: s, type: action, action: mr_merge, with: { method: $ls } }\n", `with "method"`},
		{"an action missing a required setting", header + "main:\n  - { id: s, type: action, action: ticket_set_field, with: { text: x } }\n", `action "ticket_set_field" requires with "field"`},
		{"a wait's timeout that does not parse", header + "main:\n  - { id: s, type: wait, for: mr_merged, timeout: soon }\n", "soon"},
		{"a service's timeout that does not parse", header + "main:\n  - { id: s, type: service, service: app, timeout: soon }\n", "soon"},
		{"a comparison with nothing on the left", header + "main:\n  - { id: s, type: command, run: 'true', run_if: '== fix' }\n", "run_if"},
		{"a watcher's quiet_for reads nothing", header + oneStage + "watchers:\n  - { id: w, on: { signal: mr_review_settled, quiet_for: $nope }, stages: [{ id: w1, type: command, run: 'true' }] }\n", "quiet_for"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse("t.yaml", []byte(c.doc))
			if err == nil {
				t.Fatalf("accepted:\n%s", c.doc)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %q, want it to say %q", err, c.want)
			}
		})
	}
}

func TestDocJSONAndNodeJSON(t *testing.T) {
	const doc = `name: t
version: 5
parameters: { harness: fake, model: fake/model }
schemas:
  - name: s1
    json_schema:
      type: object
      required: [b, a]
      properties:
        b: { type: integer, minimum: 1.5, nullable: null }
        a: { type: boolean, const: true }
main:
  - { id: s, type: command, run: 'true' }
`
	d, err := ParseDoc("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(d.Schemas[0].JSONSchema)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"type":"object","required":["b","a"],"properties":{"b":{"type":"integer","minimum":1.5,"nullable":null},"a":{"type":"boolean","const":true}}}`
	if string(out) != want {
		t.Errorf("schema JSON = %s", out)
	}
	if out, _ := json.Marshal(DocJSON{}); string(out) != "null" {
		t.Errorf("empty DocJSON = %s", out)
	}
	var back DocJSON
	if err := json.Unmarshal([]byte(`{"a":[1,"x"]}`), &back); err != nil {
		t.Fatal(err)
	}
	if out, err := json.Marshal(back); err != nil || !strings.Contains(string(out), `"a":[1,"x"]`) {
		t.Errorf("round trip = %s, %v", out, err)
	}
	if err := back.UnmarshalJSON([]byte(`{`)); err == nil {
		t.Error("broken JSON decoded")
	}

	var empty yaml.Node
	empty.Kind = yaml.DocumentNode
	if out, err := (DocJSON{node: empty}).MarshalJSON(); err != nil || string(out) != "null" {
		t.Errorf("empty document = %s, %v", out, err)
	}
	if _, err := (DocJSON{node: yaml.Node{Kind: yaml.AliasNode, Line: 3}}).MarshalJSON(); err == nil || !strings.Contains(err.Error(), "line 3: an alias is not JSON") {
		t.Errorf("alias = %v", err)
	}
	// A failure deep in a list or a mapping comes back out.
	bad := yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: ".nan", Line: 9}
	for _, n := range []yaml.Node{
		{Kind: yaml.SequenceNode, Content: []*yaml.Node{&bad}},
		{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: "k"}, &bad}},
	} {
		if _, err := (DocJSON{node: n}).MarshalJSON(); err == nil || !strings.Contains(err.Error(), "line 9") {
			t.Errorf("nested failure = %v", err)
		}
	}
}

func TestCheckRefusesWhatItCannotRead(t *testing.T) {
	f, err := Parse("t.yaml", []byte(header+"schemas:\n  - { name: s1, json_schema: { type: object, required: [a] } }\nmain:\n  - { id: s, type: command, run: 'true' }\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := &f.Schemas[0]
	if err := s.Check("out.json", []byte(`{"a":1}`)); err != nil {
		t.Errorf("a good file: %v", err)
	}
	if err := s.Check("out.yaml", []byte("a: 1\n")); err != nil {
		t.Errorf("a good yaml file: %v", err)
	}
	for _, c := range []struct{ path, data, want string }{
		{"out.json", `{`, "out.json is not valid JSON"},
		{"out.yaml", "a: [", "out.yaml is not valid YAML"},
		{"out.json", `{}`, `out.json does not match schema "s1"`},
		{"out.yaml", "? [a]\n: b\n", "out.yaml"},
	} {
		if err := s.Check(c.path, []byte(c.data)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %q: err = %v, want %q", c.path, c.data, err, c.want)
		}
	}
}

func TestParameterValuesAreRefusedByType(t *testing.T) {
	f, err := Parse("t.yaml", []byte("name: t\nversion: 5\nparameters:\n  harness: fake\n  model: fake/model\n  rate: { type: float, default: 0.5 }\n  many: { type: int, list: true, default: [1] }\n  names: { type: string, list: true, default: [a] }\n"+"main:\n  - { id: s, type: command, run: 'true' }\n"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		bind map[string]string
		want string
	}{
		{map[string]string{"rate": "fast"}, `parameter "rate" takes a number, got "fast"`},
		{map[string]string{"many": "[1, null]"}, `parameter "many" takes a list of int values, got <nil>`},
		{map[string]string{"many": "[1, [2]]"}, `parameter "many" takes a list of int values`},
		{map[string]string{"many": "3"}, `parameter "many" takes a list`},
	}
	for _, c := range cases {
		if err := ValidateBindings(f.Params, c.bind); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v, want %q", c.bind, err, c.want)
		}
	}
	if err := ValidateBindings(f.Params, map[string]string{"rate": "1.25", "many": "[1,2]", "names": `["a","b"]`}); err != nil {
		t.Errorf("good bindings: %v", err)
	}
}

func TestDecodeParamList(t *testing.T) {
	got, err := DecodeParamList(`["a", 1, true]`)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"a", "1", "true"}) {
		t.Errorf("items = %q", got)
	}
	if _, err := DecodeParamList("not json"); err == nil {
		t.Error("broken list decoded")
	}
}

func TestSourcesServeThePinnedVersion(t *testing.T) {
	f := &Flow{Name: "f", Hash: "h1"}
	m := MapSource{"f": f}
	if got, err := m.Get(nil, "p", "f"); err != nil || got != f {
		t.Errorf("Get = %v, %v", got, err)
	}
	if _, err := m.Get(nil, "p", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
	if got, err := m.GetVersion(nil, "p", "f", "h1"); err != nil || got != f {
		t.Errorf("GetVersion = %v, %v", got, err)
	}
	if _, err := m.GetVersion(nil, "p", "f", "h2"); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "h2") {
		t.Errorf("stale hash = %v", err)
	}
}

func TestNilFlowAccessors(t *testing.T) {
	var f *Flow
	if f.AgentSpecs() != nil {
		t.Error("a nil flow has agents")
	}
	if got := f.WorkspaceAgents(); len(got) != 0 {
		t.Errorf("a nil flow prepares %d agents", len(got))
	}
}

// A workspace is prepared with the flow's agents, and the Ask pane's when the
// flow has one, last.
func TestWorkspaceAgents(t *testing.T) {
	doc := header + "ask: { harness: fake }\n" + bAgents + oneStage
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, sp := range f.WorkspaceAgents() {
		names = append(names, sp.Name)
	}
	if !reflect.DeepEqual(names, []string{"b", AskAgent}) {
		t.Errorf("agents = %v", names)
	}
	if f.AgentHash("b") == "" || f.AgentHash("nope") != "" {
		t.Errorf("hash of b = %q, of nothing = %q", f.AgentHash("b"), f.AgentHash("nope"))
	}
}

// Each prepared agent carries the paths its stages write its outputs to, as
// the engine hands them to it: what a read-only agent may still edit. The
// flow's own definitions stay untouched (and their hash with them).
func TestWorkspaceAgentsCarryOutputPaths(t *testing.T) {
	doc := header + "ask: { harness: fake }\n" + `agents:
  - name: r
    prompt: review
    read_only: true
    outputs:
      - { name: notes, type: file }
      - { name: shots, type: files }
  - name: idle
    prompt: nothing
main:
  - { id: s1, type: agent, harness: $harness, model: $model, agent: r, output: { notes: { to: artifacts/r1.md, type: file }, shots: { to: artifacts/shots/, type: files } }, on_success: s2 }
  - { id: s2, type: agent, harness: $harness, model: $model, agent: r, output: { notes: { to: artifacts/r2.md, type: file }, shots: { to: artifacts/shots/, type: files } } }
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	hash := f.AgentHash("r")
	got := map[string][]string{}
	for _, sp := range f.WorkspaceAgents() {
		got[sp.Name] = sp.OutputPaths
	}
	want := map[string][]string{"r": {"artifacts/r1.md", "artifacts/r2.md", "artifacts/shots/"}, "idle": nil, AskAgent: nil}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("output paths = %v, want %v", got, want)
	}
	if f.Agent("r").OutputPaths != nil || f.AgentHash("r") != hash {
		t.Errorf("the flow's own agent changed: %v, hash %s → %s", f.Agent("r").OutputPaths, hash, f.AgentHash("r"))
	}
}

func TestReadsCIFailure(t *testing.T) {
	doc := header + agentReadsFailure + "main:\n  - { id: m, type: command, run: 'true', on_success: done }\nwatchers:\n  - { id: fix, on: ci_failed, stages: [{ id: w1, type: agent, harness: $harness, model: $model, agent: fix, input: { failure: { from: run.ci.failure, type: failure } } }] }\n  - { id: other, on: mr_merged, stages: [{ id: w2, type: command, run: 'true' }] }\n"
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if !f.ReadsCIFailure(f.Phase("fix")) {
		t.Error("the ci watcher reads the failure")
	}
	if f.ReadsCIFailure(f.Phase("other")) {
		t.Error("the other watcher does not")
	}
}

func TestValidArtifactPathRefusesShellMeaningfulPaths(t *testing.T) {
	for path, want := range map[string]bool{
		"artifacts/plan.md": true, "/etc/passwd": false, "../up": false, "a/../b": false, "": false,
	} {
		if got := ValidArtifactPath(path); got != want {
			t.Errorf("ValidArtifactPath(%q) = %v", path, got)
		}
	}
}
