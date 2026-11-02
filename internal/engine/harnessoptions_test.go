package engine

import (
	"context"
	"maps"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
)

const harnessOptionsFlowDoc = `name: opts
version: 2
parameters: { model: fake/model }
main:
  - { id: build, type: agent, harness: omp, model: $model, agent: builder,
      harness_options: { omp: { advisor: acme/adv } },
      input: { ticket: { from: run.ticket, type: ticket }, failure: { from: edge.failure, type: failure, optional: true } } }
  - { id: test,  type: command, run_ref: verify, on_fail: { to: build, max: 5 } }
`

// A stage's harness options reach its runner on the first attempt and on
// the attempt sent back into its session.
func TestHarnessOptionsReachTheRunner(t *testing.T) {
	h := newHarness(t, mustFlow(t, harnessOptionsFlowDoc))
	h.res.HarnessKinds = []string{"fake", "omp"}
	h.res.Runners = map[string]agentport.AgentRunner{"omp": h.runner}
	verifies := 0
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "make lint") {
			if verifies++; verifies == 1 {
				return envport.ExecResult{ExitCode: 1, Output: "BOOM"}, nil
			}
		}
		return envport.ExecResult{}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "opts", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(h.events(t, runID)))
	}
	want := map[string]string{"advisor": "acme/adv"}
	runs := h.runner.SnapshotRunCalls()
	if len(runs) == 0 || !maps.Equal(runs[0].In.HarnessOptions, want) {
		t.Fatalf("first attempt's options = %+v", runs)
	}
	if len(h.runner.ResumeCalls) != 1 || !maps.Equal(h.runner.ResumeCalls[0].In.HarnessOptions, want) {
		t.Errorf("second attempt's options = %+v", h.runner.ResumeCalls)
	}
}

// A runner that cannot apply harness options.
type noOptionsRunner struct{ *fake.AgentRunner }

func (noOptionsRunner) TakesHarnessOptions() bool { return false }

// A stage whose harness cannot apply its options is refused before the run
// spends anything.
func TestHarnessOptionsTheHarnessCannotApplyAreRefused(t *testing.T) {
	h := newHarness(t, mustFlow(t, harnessOptionsFlowDoc))
	h.res.HarnessKinds = []string{"fake", "omp"}
	h.res.Runners = map[string]agentport.AgentRunner{"omp": noOptionsRunner{h.runner}}
	_, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "opts", testProject)
	if err == nil || !strings.Contains(err.Error(), `preflight: stage "build" sets harness_options.omp, which the omp harness cannot apply`) {
		t.Fatalf("create run = %v", err)
	}
}
