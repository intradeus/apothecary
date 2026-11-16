package flow

import (
	"strings"
	"testing"
)

// impasseStages: a builder and a reviewer whose rejections run out into the
// impasse. REVIEW_REJECT is replaced by each case's on_reject.
const impasseStages = `  - id: build
    type: agent
    harness: $harness
    model: $model
    agent: reader
    input:
      plan:      { from: repo:plan.md, type: file }
      rejection: { from: edge.verdict, type: verdict, optional: true }
  - id: review
    type: agent
    harness: $harness
    model: $model
    agent: reviewer
    input: { changes: { from: run.git.diff_target, type: changeset } }
    output: { verdict: { to: artifacts/verdict.json, type: verdict } }
    on_reject: REVIEW_REJECT
`

func impasseDoc(onReject, more string) string {
	return portsDoc + strings.Replace(impasseStages, "REVIEW_REJECT", onReject, 1) + more
}

// TestImpasseLeaf: the impasse is a reserved target of on_reject chains
// only — as the escalation of a chain or on its own — takes no max or then,
// is no stage of its own, and makes the stage that reaches it skippable.
func TestImpasseLeaf(t *testing.T) {
	f, err := Parse("t.yaml", []byte(impasseDoc("{ to: build, max: 5, then: impasse }", "")))
	if err != nil {
		t.Fatal(err)
	}
	re := f.Stage("review").Edge(EdgeReject)
	if re == nil || re.To != "build" || re.Max != 5 || re.Then == nil ||
		re.Then.To != ReservedImpasse || re.Then.Max != 0 || re.Then.Then != nil {
		t.Fatalf("review on_reject = %+v / %+v, want five rounds then the impasse", re, re.Then)
	}
	if f.Stage(ReservedImpasse) != nil || len(f.Stages) != 2 {
		t.Fatalf("stages = %d; the impasse must not become a stage", len(f.Stages))
	}
	if !f.Stage("review").Skippable() || f.Stage("build").Skippable() {
		t.Errorf("skippable: review %v build %v, want only the stage that reaches the impasse",
			f.Stage("review").Skippable(), f.Stage("build").Skippable())
	}

	// On its own, every rejection asks a person; a gate may say it too: a
	// flow that makes little sense is still a valid one.
	// (Nothing then sends build a verdict, so it reads none.)
	alone := strings.Replace(impasseDoc("impasse", ""), "      rejection: { from: edge.verdict, type: verdict, optional: true }\n", "", 1)
	if _, err := Parse("t.yaml", []byte(alone)); err != nil {
		t.Errorf("on_reject: impasse: %v", err)
	}
	gate := "  - { id: gate, type: human_gate, question: q, on_reject: impasse }\n"
	if _, err := Parse("t.yaml", []byte(impasseDoc("{ to: build, max: 1 }", gate))); err != nil {
		t.Errorf("a gate's on_reject: impasse: %v", err)
	}
}

func TestImpasseLeafRefusals(t *testing.T) {
	cmd := func(edges string) string {
		return impasseDoc("{ to: build, max: 1 }", "  - { id: test, type: command, run: t, "+edges+" }\n")
	}
	for _, tc := range []struct {
		name, doc, want string
	}{
		{"on_fail", cmd("on_fail: impasse"), "reached only from on_reject"},
		{"on_fail's then", cmd("on_fail: { to: test, max: 1, then: impasse }"), "reached only from on_reject"},
		{"on_timeout", cmd("on_timeout: impasse"), "reached only from on_reject"},
		{"on_success", cmd("on_success: impasse"), "on_success cannot be \"impasse\""},
		{"max", impasseDoc("{ to: impasse, max: 2 }", ""), "max does not apply when to is \"impasse\""},
		{"then", impasseDoc("{ to: impasse, then: abort }", ""), "then does not apply when to is \"impasse\""},
		{"max deeper", impasseDoc("{ to: build, max: 2, then: { to: impasse, max: 1 } }", ""), "max does not apply when to is \"impasse\""},
		{"stage id", impasseDoc("{ to: build, max: 1 }", "  - { id: impasse, type: command, run: t }\n"), "id \"impasse\" is reserved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse("t.yaml", []byte(tc.doc)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

// What a skippable stage writes is produced only on some routes: a later
// required read of it is refused, naming the impasse; an optional one is fine.
func TestImpasseMakesOutputsConditional(t *testing.T) {
	read := func(optional string) string {
		return `  - id: after
    type: agent
    harness: $harness
    model: $model
    agent: reader
    input:
      plan:      { from: repo:plan.md, type: file }
      rejection: { from: artifacts/verdict.json, type: verdict` + optional + ` }
`
	}
	_, err := Parse("t.yaml", []byte(impasseDoc("{ to: build, max: 5, then: impasse }", read(""))))
	if err == nil || !strings.Contains(err.Error(), `stage "review" may be skipped at its impasse`) {
		t.Errorf("required read of a skippable stage's output: %v", err)
	}
	if _, err := Parse("t.yaml", []byte(impasseDoc("{ to: build, max: 5, then: impasse }", read(", optional: true")))); err != nil {
		t.Errorf("optional read: %v", err)
	}
	// Without the impasse the reviewer always writes it.
	if _, err := Parse("t.yaml", []byte(impasseDoc("{ to: build, max: 5, then: debug }", read("")))); err != nil {
		t.Errorf("required read, no impasse: %v", err)
	}
}
