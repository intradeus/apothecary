package flow

import (
	"slices"
	"strings"
	"testing"
)

// labelsDoc labels its merge request from a list parameter.
const labelsDoc = `name: t
version: 5
parameters:
  harness: fake
  model: a/b
  labels: { type: string, list: true, default: [apothecary, bot], description: "the merge request's labels" }
  wait:   { type: duration, default: 10m }
agents:
  - name: w
    prompt: write
    outputs:
      - { name: mr, type: file }
main:
  - { id: write, type: agent, harness: $harness, model: $model, agent: w, output: { mr: { to: artifacts/mr.md, type: file } } }
  - id: mr
    type: action
    action: create_mr
    input: { body: { from: artifacts/mr.md, type: file } }
    with: { labels: $labels }
`

func TestAListParameterFeedsAListSetting(t *testing.T) {
	f, err := Parse("t.yaml", []byte(labelsDoc))
	if err != nil {
		t.Fatal(err)
	}
	if pr := f.Param("labels"); !pr.List || pr.Type != ParamString || pr.Default != `["apothecary","bot"]` {
		t.Errorf("labels = %+v", pr)
	}
	if got := f.Stage("mr").SettingList("labels"); !slices.Equal(got, []string{"apothecary", "bot"}) {
		t.Errorf("labels setting = %v", got)
	}
	// Bound, the list is the workshop's.
	g, err := ParseBound("t.yaml", []byte(labelsDoc), map[string]string{"labels": `["team-a"]`})
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Stage("mr").SettingList("labels"); !slices.Equal(got, []string{"team-a"}) {
		t.Errorf("bound labels = %v", got)
	}
	// A literal list, written in the flow.
	lit, err := Parse("t.yaml", []byte(strings.Replace(labelsDoc, "with: { labels: $labels }", "with: { labels: [a, b] }", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if got := lit.Stage("mr").SettingList("labels"); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("literal labels = %v", got)
	}
}

func TestListsRefused(t *testing.T) {
	for _, c := range []struct{ name, from, to, want string }{
		{"one value for a list", "with: { labels: $labels }", "with: { labels: apothecary }", "a list: labels: [apothecary]"},
		{"a scalar parameter for a list", "with: { labels: $labels }", "with: { labels: $wait }", `parameter "wait" is a duration, and this field takes a list of strings`},
		{"a blank item", "with: { labels: $labels }", `with: { labels: [a, " "] }`, `item " " must be a literal`},
		{"a parameter inside a list", "with: { labels: $labels }", "with: { labels: [$labels] }", `item "$labels" must be a literal`},
		{"a list for one value", "type: agent, harness: $harness, model: $model, agent: w,", "type: agent, harness: $harness, model: $model, agent: w, timeout: $labels,", `parameter "labels" is a list of strings, and this field takes a duration`},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := strings.Replace(labelsDoc, c.from, c.to, 1)
			if src == labelsDoc {
				t.Fatalf("%q is not in the document", c.from)
			}
			_, err := Parse("t.yaml", []byte(src))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// A bound list is a JSON array of the parameter's type; each item meets its
// enum.
func TestListBindings(t *testing.T) {
	params := []Param{
		{Name: "labels", Type: ParamString, List: true},
		{Name: "pick", Type: ParamString, List: true, Enum: []string{"a", "b"}},
		{Name: "ns", Type: ParamInt, List: true},
	}
	for _, c := range []struct {
		name, value, want string
	}{
		{"labels", `["x", "y"]`, ""},
		{"labels", `x`, "takes a list — a JSON array"},
		{"pick", `["a", "c"]`, `takes one of a | b, got "c"`},
		{"ns", `[1, 2]`, ""},
		{"ns", `["1", "z"]`, `takes an integer, got "z"`},
	} {
		err := ValidateBindings(params, map[string]string{c.name: c.value})
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s = %s: err = %v, want %q", c.name, c.value, err, c.want)
		}
	}
	if v, _ := ParseParamValue(&params[2], `[1, "2"]`); v != `[1,2]` {
		t.Errorf("canonical = %s", v)
	}
}
