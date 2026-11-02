package engine

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/platform/telemetry"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

// Each attempt's work runs in the attempt's own trace, under the span the
// telemetry sink exports from its stage_started: a retry is a new trace, not
// more spans in the first one. (With no tracer installed the invocation's
// context carries its parent's span context, which is what is checked.)
func TestEachAttemptRunsInItsOwnTrace(t *testing.T) {
	doc := `name: contract
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: plan, type: agent, harness: $harness, model: $model, agent: planner, output: { plan: { to: artifacts/plan.md, type: file } }, on_fail: { to: plan, max: 1 } }
`
	h := newHarness(t, mustFlow(t, doc))
	var seen []trace.SpanContext
	h.runner.RunFunc = func(ctx context.Context, _ fake.RunCall) (agentport.AgentResult, error) {
		seen = append(seen, trace.SpanContextFromContext(ctx))
		return agentport.AgentResult{}, nil // never writes the artifact: every attempt fails
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "contract", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	var iters []int
	for _, e := range h.events(t, runID) {
		if p, ok := e.Payload.(*event.StageStarted); ok {
			iters = append(iters, p.Iteration)
		}
	}
	if len(seen) != 2 || len(iters) != 2 {
		t.Fatalf("invocations %d, attempts %v: want two of each", len(seen), iters)
	}
	for i, sc := range seen {
		want := telemetry.StageSpanContext(runID, "plan", iters[i])
		if sc.TraceID() != want.TraceID() || sc.SpanID() != want.SpanID() {
			t.Errorf("attempt %d ran under %s/%s, want its own trace %s/%s",
				iters[i], sc.TraceID(), sc.SpanID(), want.TraceID(), want.SpanID())
		}
	}
	if seen[0].TraceID() == seen[1].TraceID() {
		t.Error("the retry shares the first attempt's trace")
	}
}
