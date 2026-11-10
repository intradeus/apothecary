package engine

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

// A failed attempt's spend is real: it lands on its stage_failed and in the
// run's facts, like a completed attempt's.
func TestFailedAttemptKeepsItsSpend(t *testing.T) {
	doc := `name: contract
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: plan, type: agent, harness: $harness, model: $model, agent: planner, output: { plan: { to: artifacts/plan.md, type: file } } }
`
	h := newHarness(t, mustFlow(t, doc))
	// The planner spends, then never writes the declared artifact.
	h.runner.RunFunc = func(context.Context, fake.RunCall) (agentport.AgentResult, error) {
		return agentport.AgentResult{TurnsUsed: 3, Usage: agentport.Usage{CostUSD: 0.4}}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "contract", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	evs := h.events(t, runID)
	sf, ok := findPayload[*event.StageFailed](evs)
	if !ok || sf.Reason != event.ReasonOutputContract {
		t.Fatalf("stage_failed = %+v", sf)
	}
	if sf.TurnsUsed != 3 || sf.Usage == nil || sf.Usage.CostUSD != 0.4 {
		t.Errorf("stage_failed spend = turns %d usage %+v, want 3 turns and $0.4", sf.TurnsUsed, sf.Usage)
	}
	if f := run.Reduce(evs).Facts; f.CostUSD != 0.4 || f.Turns != 3 || f.FailedAttempts != 1 {
		t.Errorf("facts = cost %v turns %d failed %d", f.CostUSD, f.Turns, f.FailedAttempts)
	}
}

// A retry loop on failure meets the budget: the failed spend counts, so the
// run stops once it is over, not after the loop runs out.
func TestFailedSpendCountsAgainstTheBudget(t *testing.T) {
	doc := `name: contract
version: 2
parameters: { harness: fake, model: fake/model, max_cost_usd: 0.5 }
main:
  - { id: plan, type: agent, harness: $harness, model: $model, agent: planner, output: { plan: { to: artifacts/plan.md, type: file } }, on_fail: { to: plan, max: 3 } }
`
	h := newHarness(t, mustFlow(t, doc))
	h.runner.RunFunc = func(context.Context, fake.RunCall) (agentport.AgentResult, error) {
		return agentport.AgentResult{TurnsUsed: 3, Usage: agentport.Usage{CostUSD: 0.4}}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "contract", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Paused {
		t.Fatalf("state = %s: %v", v.State, eventTypes(evs))
	}
	rp, _ := findPayload[*event.RunPaused](evs)
	if rp == nil || rp.Reason != event.PauseBudget || !strings.HasPrefix(rp.Note, "budget exceeded") {
		t.Errorf("run_paused = %+v", rp)
	}
	if got := len(h.runner.RunCalls); got != 2 {
		t.Errorf("plan ran %d times, want 2 (0.4 + 0.4 > 0.5)", got)
	}
	if f := run.Reduce(evs).Facts; f.FailedAttempts != 2 || f.Retries != 1 {
		t.Errorf("facts = failed %d retries %d", f.FailedAttempts, f.Retries)
	}
}
