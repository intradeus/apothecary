package engine

// create_mr's labels: a list setting, from a list parameter the workshop may
// bind; the merge request gets them once it is open, and a host refusing
// them does not undo it.

import (
	"context"
	"errors"
	"slices"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/run"
	"apothecary/internal/runinput"
)

const labelsFlowDoc = `name: lb
version: 5
parameters:
  harness: fake
  model: fake/model
  mr_labels: { type: string, list: true, default: [apothecary] }
agents:
  - name: redactor
    prompt: describe it
    outputs:
      - { name: mr, type: file }
main:
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } } }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } }, with: { labels: $mr_labels } }
`

func labelsRun(t *testing.T, labelErr error) (*harness, string) {
	h := newHarness(t, mustFlow(t, labelsFlowDoc))
	h.repoHost.LabelErr = labelErr
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"redactor": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/mr.md", "# Fix\n")
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "lb", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	return h, runID
}

func TestCreateMRLabelsTheMergeRequest(t *testing.T) {
	h, runID := labelsRun(t, nil)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events %v", v.State, eventTypes(h.events(t, runID)))
	}
	if mrs := h.repoHost.SnapshotMRs(); len(mrs) != 1 || !slices.Equal(mrs[0].Labels, []string{"apothecary"}) {
		t.Errorf("merge requests = %+v", mrs)
	}
}

func TestARefusedLabelLeavesTheMergeRequest(t *testing.T) {
	h, runID := labelsRun(t, errors.New("label quota"))
	if v := h.view(t, runID); v.State != run.Completed || v.MRURL == "" {
		t.Fatalf("state = %s, mr %q", v.State, v.MRURL)
	}
}
