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

	"apothecary/internal/agentspec"
	"apothecary/internal/flow"
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
	files, _ := filepath.Glob("../builtin/flows/*.yaml")
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
// a signal, a start policy, an action's input.
func TestSchemaRefusesMistakes(t *testing.T) {
	s := compileSchema(t)
	head := "name: t\nversion: 5\nparameters: { harness: h, model: a/b }\n"
	for name, doc := range map[string]string{
		"unknown key":       head + "mian:\n  - { id: a, type: command, run: x }\n",
		"stage type":        head + "main:\n  - { id: a, type: comand, run: x }\n",
		"stage key":         head + "main:\n  - { id: a, type: command, script: x }\n",
		"signal":            head + "watchers:\n  - { id: w, on: mr_conflict, stages: [ { id: a, type: command, run: x } ] }\n",
		"start policy":      head + "watchers:\n  - { id: w, on: { signal: mr_conflicts, start: later }, stages: [ { id: a, type: command, run: x } ] }\n",
		"action input":      head + "main:\n  - { id: a, type: action, action: sync_target, input: { strategy: squash } }\n",
		"action input name": head + "main:\n  - { id: a, type: action, action: sync_target, input: { stratgy: merge } }\n",
		"agent without one": head + "main:\n  - { id: a, type: agent }\n",
		"outcome's then":    head + "outcomes:\n  - { id: m, on: mr_merged, then: finish }\n",
		"version":           strings.Replace(head, "version: 5", "version: 4", 1) + "main:\n  - { id: a, type: command, run: x }\n",
	} {
		if err := s.Validate(asJSON(t, []byte(doc))); err == nil {
			t.Errorf("%s: valid against the schema\n%s", name, doc)
		}
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
	// An edge is read by hand (its mapping form): to, max, then.
	if got := props(objOf(defs["edge"])); !slices.Equal(got, []string{"max", "then", "to"}) {
		t.Errorf("edge: the schema names %v", got)
	}
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
