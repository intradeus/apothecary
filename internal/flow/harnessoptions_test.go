package flow

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
)

const optHead = `name: t
version: 5
agents:
  - { name: a, prompt: p }
`

// The block of the kind a stage runs on is resolved onto the stage; its
// model options join the stage's models in registry order.
func TestHarnessOptionsResolve(t *testing.T) {
	f, err := Parse("t.yaml", []byte(optHead+`main:
  - id: s
    type: agent
    agent: a
    harness: omp
    model: acme/main
    harness_options: { omp: { advisor: acme/adv, advisor_review: agent-end, smol: acme/s, slow: acme/l } }
`))
	if err != nil {
		t.Fatal(err)
	}
	st := f.Stage("s")
	want := map[string]string{"advisor": "acme/adv", "advisor_review": "agent-end", "smol": "acme/s", "slow": "acme/l"}
	if !maps.Equal(st.HarnessOptions, want) {
		t.Errorf("options = %v, want %v", st.HarnessOptions, want)
	}
	wantModels := []ModelUse{
		{"model", "acme/main"},
		{"harness_options.omp.advisor", "acme/adv"},
		{"harness_options.omp.smol", "acme/s"},
		{"harness_options.omp.slow", "acme/l"},
	}
	if got := st.Models(); !slices.Equal(got, wantModels) {
		t.Errorf("models = %v, want %v", got, wantModels)
	}
}

// An advisor read from a parameter with no value waits like a model — but
// only matters when the stage runs on the harness its block is for.
func TestHarnessOptionModelUnboundOnlyOnItsHarness(t *testing.T) {
	doc := optHead + `parameters:
  harness: fake
  model: fake/model
  adv: { type: string }
main:
  - { id: s, type: agent, agent: a, harness: $harness, model: $model, harness_options: { omp: { advisor: $adv } } }
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if st := f.Stage("s"); st.HarnessOptions != nil {
		t.Errorf("options on fake = %v, want none", st.HarnessOptions)
	}
	if ub := f.Unbound(); len(ub) != 0 {
		t.Errorf("unbound on fake = %+v, want none", ub)
	}
	if pr := f.Param("adv"); pr.Role != RoleModel || pr.Harness != "omp" {
		t.Errorf("adv = %+v, want role model on omp", pr)
	}

	onOmp, err := f.Resolve(map[string]string{"harness": "omp"})
	if err != nil {
		t.Fatal(err)
	}
	ub := onOmp.Unbound()
	if len(ub) != 1 || ub[0].Param != "adv" || strings.Join(ub[0].Sites, ",") != "s" {
		t.Fatalf("unbound on omp = %+v", ub)
	}
	if err := UnboundError(ub); err == nil || !strings.Contains(err.Error(), `"adv" has no value (read by s)`) {
		t.Errorf("message = %v", err)
	}
	bound, err := f.Resolve(map[string]string{"harness": "omp", "adv": "acme/adv"})
	if err != nil {
		t.Fatal(err)
	}
	if got := bound.Stage("s").HarnessOptions; !maps.Equal(got, map[string]string{"advisor": "acme/adv"}) {
		t.Errorf("bound options = %v", got)
	}

	d, err := ParseDoc("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d)
	if !strings.Contains(string(b), `"harness_options":{"omp":{"advisor":"$adv"}}`) {
		t.Errorf("document JSON lost the block: %s", b)
	}
}

func TestHarnessOptionErrors(t *testing.T) {
	stage := func(harness, opts string) string {
		return optHead + "parameters: { harness: fake, model: fake/model }\nmain:\n  - { id: s, type: agent, agent: a, harness: " + harness + ", model: fake/model, harness_options: " + opts + " }\n"
	}
	for _, tc := range []struct{ name, doc, substr string }{
		{"unknown kind", stage("$harness", "{ codex: { x: y } }"), "takes no options (harnesses with options: omp)"},
		{"unknown option", stage("omp", "{ omp: { brain: acme/b } }"), "is not an option of omp (options: advisor, advisor_review, smol, slow)"},
		{"block for another harness", stage("fake", "{ omp: { advisor: acme/a } }"), "the stage runs on harness fake, so these options never apply"},
		{"enum out of range", stage("omp", "{ omp: { advisor: acme/a, advisor_review: hourly } }"), "is not one of turn | agent-end"},
		{"needs its partner", stage("omp", "{ omp: { advisor_review: turn } }"), "needs advisor in the same block"},
		{"blank value", stage("omp", `{ omp: { advisor: "" } }`), "a value is required"},
		{"not a model", stage("omp", "{ omp: { advisor: nope } }"), "is not of the form provider/model"},
		{"empty parameter", optHead + "parameters: { m: { type: string, default: \"\" } }\nmain:\n  - { id: s, type: agent, agent: a, harness: omp, model: fake/model, harness_options: { omp: { advisor: $m } } }\n", "is empty — bind a model"},
		{"on a command", optHead + "main:\n  - { id: s, type: command, run: x, harness_options: { omp: { advisor: acme/a } } }\n", `"harness_options" does not apply to type command`},
		{"on a sequence stage", optHead + "sequences:\n  - id: b\n    stages:\n      - { id: c, type: command, run: x }\nmain:\n  - { id: s, sequence: b, harness_options: { omp: { advisor: acme/a } }, on_success: done }\n", `"harness_options" does not apply to a sequence stage`},
		{"model shared across harnesses", optHead + "parameters: { harness: fake, m: { type: string, default: acme/m } }\nmain:\n  - { id: s, type: agent, agent: a, harness: $harness, model: $m, harness_options: { omp: { advisor: $m } } }\n", "stages sharing a model parameter must share a harness"},
	} {
		t.Run(tc.name, func(t *testing.T) { wantErr(t, tc.doc, tc.substr, 0) })
	}
}
