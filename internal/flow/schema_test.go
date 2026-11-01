package flow_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"apothecary/internal/flow"
	"apothecary/internal/flow/actionspec"
	"apothecary/internal/flow/agentspec"
)

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(flow.Schema()))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("flow.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("flow.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// asJSON reads a YAML document the way an editor's schema sees it.
func asJSON(t *testing.T, src []byte) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal(src, &v); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

// Every flow the project ships — the templates, the examples — is valid
// against the schema an editor checks as it types.
func TestSchemaAcceptsTheShippedFlows(t *testing.T) {
	s := compileSchema(t)
	files, _ := filepath.Glob("builtin/flows/*.yaml")
	examples, _ := filepath.Glob("../../examples/flows/*.yaml")
	files = append(files, examples...)
	if len(files) < 3 {
		t.Fatalf("found %d flows", len(files))
	}
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := flow.Parse(path, src); err != nil {
			continue // a document the parser refuses is no evidence either way
		}
		if err := s.Validate(asJSON(t, src)); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

// The schema catches what a person mistypes: an unknown key, a stage type,
// a signal, a start policy, an action's setting, a port.
func TestSchemaRefusesMistakes(t *testing.T) {
	s := compileSchema(t)
	head := "name: t\nversion: 5\nparameters: { harness: h, model: a/b }\n"
	for name, doc := range map[string]string{
		"unknown key":          head + "mian:\n  - { id: a, type: command, run: x }\n",
		"stage type":           head + "main:\n  - { id: a, type: comand, run: x }\n",
		"stage key":            head + "main:\n  - { id: a, type: command, script: x }\n",
		"signal":               head + "watchers:\n  - { id: w, on: mr_conflict, stages: [ { id: a, type: command, run: x } ] }\n",
		"start policy":         head + "watchers:\n  - { id: w, on: { signal: mr_conflicts, start: later }, stages: [ { id: a, type: command, run: x } ] }\n",
		"action setting":       head + "main:\n  - { id: a, type: action, action: sync_target, with: { strategy: squash } }\n",
		"action setting name":  head + "main:\n  - { id: a, type: action, action: sync_target, with: { stratgy: merge } }\n",
		"setting as a port":    head + "main:\n  - { id: a, type: action, action: sync_target, input: { strategy: { from: repo:s.md, type: file } } }\n",
		"action input name":    head + "main:\n  - { id: a, type: action, action: create_mr, input: { bdy: { from: repo:mr.md, type: file } } }\n",
		"agent without one":    head + "main:\n  - { id: a, type: agent, harness: $harness, model: $model }\n",
		"outcome's then":       head + "outcomes:\n  - { id: m, on: mr_merged, then: finish }\n",
		"version":              strings.Replace(head, "version: 5", "version: 4", 1) + "main:\n  - { id: a, type: command, run: x }\n",
		"bare port":            head + "main:\n  - { id: a, type: command, run: x, input: { r: artifacts/a.md } }\n",
		"port key":             head + "main:\n  - { id: a, type: command, run: x, output: { r: { to: a.json, type: file, shape: s } } }\n",
		"port without a shape": head + "main:\n  - { id: a, type: command, run: x, output: { r: { to: a.json } } }\n",
		"input going to":       head + "main:\n  - { id: a, type: command, run: x, input: { r: { to: a.md, type: file } } }\n",
		"optional output":      head + "main:\n  - { id: a, type: command, run: x, output: { r: { to: a.md, type: file, optional: true } } }\n",
		"value type written":   head + "main:\n  - { id: a, type: command, run: x, output: { r: { to: a.md, type: ticket } } }\n",
		"port type":            head + "main:\n  - { id: a, type: command, run: x, input: { r: { from: repo:a.md, type: pdf } } }\n",
		"agent output path":    head + "agents:\n  - { name: w, prompt: p, outputs: [ { name: o, type: file, path: a.md } ] }\nmain:\n  - { id: a, type: command, run: x }\n",
		"agent untyped input":  head + "agents:\n  - { name: w, prompt: p, inputs: [ { name: i } ] }\nmain:\n  - { id: a, type: command, run: x }\n",
		"schema body":          head + "schemas:\n  - { name: s, json_schema: 12 }\n",
	} {
		if err := s.Validate(asJSON(t, []byte(doc))); err == nil {
			t.Errorf("%s: valid against the schema\n%s", name, doc)
		}
	}
}

// Structures as the editor sees them as they are typed: a schema, a typed
// output with one, ports of every source, an action's settings.
func TestSchemaAcceptsStructures(t *testing.T) {
	s := compileSchema(t)
	doc := `name: t
version: 5
parameters: { harness: h, model: a/b }
schemas:
  - name: report
    json_schema: { type: object, required: [passed] }
agents:
  - name: w
    prompt: p
    outputs:
      - { name: c, type: commit_message, schema: report }
    inputs:
      - { name: r, schema: report }
      - { name: t, type: ticket }
      - { name: f, type: failure, optional: true }
main:
  - { id: a, type: command, run: x, input: { r: { from: repo:r.json, schema: report } },
      output: { r: { to: artifacts/r.json, schema: report }, log: { to: artifacts/l.txt, type: file, description: the log } } }
  - id: w
    type: agent
    harness: $harness
    model: $model
    agent: w
    input:
      r: { from: artifacts/r.json, schema: report }
      t: { from: run.ticket, type: ticket }
      f: { from: edge.failure, type: failure, optional: true }
    output:
      c: { to: artifacts/c.json, type: commit_message, schema: report }
    on_fail: { to: w, max: 1 }
  - { id: commit, type: action, action: commit, input: { message: { from: artifacts/c.json, type: commit_message } } }
  - { id: sync, type: action, action: sync_target, with: { strategy: rebase } }
  - { id: load, type: action, action: load_review_comments, with: { select: all }, output: { threads: { to: artifacts/threads.yaml, type: mr_threads } } }
  - { id: gate, type: human_gate, input: { mr: { from: run.mr.url, type: link } } }
`
	if err := s.Validate(asJSON(t, []byte(doc))); err != nil {
		t.Error(err)
	}
}

// The schema names every key the parser reads, and none it does not: the
// two cannot drift.
func TestSchemaMatchesTheGrammar(t *testing.T) {
	var sch map[string]any
	if err := json.Unmarshal(flow.Schema(), &sch); err != nil {
		t.Fatal(err)
	}
	defs := sch["$defs"].(map[string]any)
	props := func(o any) []string {
		var out []string
		for k := range o.(map[string]any)["properties"].(map[string]any) {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	items := func(o any) any { return o.(map[string]any)["items"] }
	top := sch["properties"].(map[string]any)
	objOf := func(o any) any { // the object form of an anyOf
		for _, alt := range o.(map[string]any)["anyOf"].([]any) {
			if alt.(map[string]any)["type"] == "object" {
				return alt
			}
		}
		t.Fatalf("no object form in %v", o)
		return nil
	}
	agent := items(top["agents"])
	agentProps := agent.(map[string]any)["properties"].(map[string]any)
	for _, tc := range []struct {
		name   string
		typ    any
		schema []string
	}{
		{"document", flow.Doc{}, props(sch)},
		{"stage", flow.DocStage{}, props(defs["stage"])},
		{"outcome", flow.DocWatcher{}, props(items(top["outcomes"]))},
		{"wait", flow.DocWait{}, props(objOf(defs["wait"]))},
		{"sequence", flow.DocSequence{}, props(items(top["sequences"]))},
		{"agent", agentspec.Spec{}, props(agent)},
		{"agent input", agentspec.InputDecl{}, props(items(agentProps["inputs"]))},
		{"agent output", agentspec.OutputDecl{}, props(items(agentProps["outputs"]))},
		{"parameter", flow.DocParam{}, props(objOf(defs["param"]))},
	} {
		if got := yamlKeys(reflect.TypeOf(tc.typ)); !slices.Equal(got, tc.schema) {
			t.Errorf("%s: the parser reads %v, the schema names %v", tc.name, got, tc.schema)
		}
	}
	// A port is read by hand; its keys are DocPort's. An input has no
	// destination, an output no source, is never optional and takes no
	// ticket_comments options.
	port := jsonKeys(reflect.TypeOf(flow.DocPort{}))
	without := func(drop ...string) []string {
		return slices.DeleteFunc(slices.Clone(port), func(k string) bool { return slices.Contains(drop, k) })
	}
	if got, want := props(defs["port_in"]), without("to"); !slices.Equal(got, want) {
		t.Errorf("port_in: the parser reads %v, the schema names %v", want, got)
	}
	if got, want := props(defs["port_out"]), without("from", "optional", "limit", "exclude_own"); !slices.Equal(got, want) {
		t.Errorf("port_out: the parser reads %v, the schema names %v", want, got)
	}
	stage := defs["stage"].(map[string]any)["properties"].(map[string]any)
	for field, def := range map[string]string{"input": "#/$defs/port_in", "output": "#/$defs/port_out"} {
		if ref := stage[field].(map[string]any)["additionalProperties"].(map[string]any)["$ref"]; ref != def {
			t.Errorf("stage %s: ports are %v, want %s", field, ref, def)
		}
	}
	// Each action's input, output and with name exactly its ports and its
	// settings, so an editor completes them and refuses a stray one.
	rules := map[string]map[string]any{}
	for _, r := range defs["stage"].(map[string]any)["allOf"].([]any) {
		rule := r.(map[string]any)
		action, ok := rule["if"].(map[string]any)["properties"].(map[string]any)["action"]
		if !ok {
			continue
		}
		rules[action.(map[string]any)["const"].(string)] = rule["then"].(map[string]any)["properties"].(map[string]any)
	}
	for _, act := range actionspec.Registry() {
		then := rules[act.Slug]
		if then == nil {
			t.Errorf("action %s: no rule", act.Slug)
			continue
		}
		portNames := func(ps []actionspec.PortSig) []string {
			var out []string
			for _, p := range ps {
				out = append(out, p.Name)
			}
			slices.Sort(out)
			return out
		}
		var settings []string
		for _, st := range act.Settings {
			settings = append(settings, st.Name)
		}
		slices.Sort(settings)
		for field, want := range map[string][]string{"input": portNames(act.Inputs), "output": portNames(act.Outputs), "with": settings} {
			if got := props(then[field]); !slices.Equal(got, want) {
				t.Errorf("action %s %s: the registry has %v, the schema names %v", act.Slug, field, want, got)
			}
		}
	}
	// An edge is read by hand (its mapping form): to, max, then.
	if got := props(objOf(defs["edge"])); !slices.Equal(got, []string{"max", "then", "to"}) {
		t.Errorf("edge: the schema names %v", got)
	}
}

// jsonKeys lists a struct's JSON keys.
func jsonKeys(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		key, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if key != "" && key != "-" {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}

// yamlKeys lists a struct's YAML keys, the retired ones (json "-") aside.
func yamlKeys(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		key, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if key == "" || key == "-" || f.Tag.Get("json") == "-" {
			continue
		}
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}
