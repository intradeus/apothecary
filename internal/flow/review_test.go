package flow

import (
	"strings"
	"testing"
)

// reviewDoc's gate shows a plan, a folder of screenshots and the merge
// request; what the person said on approving reaches the stage after it.
const reviewDoc = `name: t
version: 5
parameters:
  harness: fake
  model: fake/model
agents:
  - name: writer
    prompt: write the plan and take the shots
    outputs:
      - { name: plan, type: file }
      - { name: shots, type: files }
  - name: builder
    prompt: build it
    inputs:
      - { name: plan, type: file }
      - { name: review, type: verdict, optional: true }
main:
  - id: write
    type: agent
    harness: $harness
    model: $model
    agent: writer
    output:
      plan: { to: artifacts/plan.md, type: file }
      shots: { to: artifacts/shots/, type: files }
  - id: gate
    type: human_gate
    input:
      plan: { from: artifacts/plan.md, type: file }
      shots: { from: artifacts/shots/, type: files }
      workspace: { from: run.workspace, type: link }
    on_reject: { to: write, max: 2 }
  - id: build
    type: agent
    harness: $harness
    model: $model
    agent: builder
    input:
      plan: { from: artifacts/plan.md, type: file }
      review: { from: edge.verdict, type: verdict, optional: true }
`

func TestGateShowsFilesAndCarriesItsApproval(t *testing.T) {
	f, err := Parse("t.yaml", []byte(reviewDoc))
	if err != nil {
		t.Fatal(err)
	}
	g := f.Stage("gate")
	if len(g.Inputs) != 3 || g.Input("shots").Type != "files" || !GivesVerdict(g) {
		t.Errorf("gate = %+v", g.Inputs)
	}
	if GivesVerdict(f.Stage("build")) {
		t.Error("an agent without a verdict decides nothing")
	}
}

func TestEdgeDecisionNeedsADecisionEntering(t *testing.T) {
	// A command between the gate and the reader: nothing carries the
	// approval past it.
	doc := strings.Replace(reviewDoc, "  - id: build\n", "  - { id: lint, type: command, run: \"true\" }\n  - id: build\n", 1)
	_, err := Parse("t.yaml", []byte(doc))
	if err == nil || !strings.Contains(err.Error(), `no on_reject edge enters stage "build", nor the success of a gate or a verdict`) {
		t.Fatalf("err = %v", err)
	}
}
