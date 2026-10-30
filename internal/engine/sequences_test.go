package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
	"apothecary/internal/runinput"
)

// A sequence runs as its expanded stages: the events name them by their
// sequence path, and `fail` leaves the sequence by the sequence stage's
// on_fail — here back to build, whose second pass gets through.
func TestSequenceRunsAsItsStages(t *testing.T) {
	doc := `name: seq
version: 5
parameters: { harness: fake, model: fake/model }
sequences:
  - id: check
    stages:
      - { id: lint, type: command, run: "./lint.sh" }
      - { id: test, type: command, run: "./test.sh", on_fail: fail }
main:
  - { id: build, type: command, run: "./build.sh" }
  - { id: check, sequence: check, on_fail: { to: build, max: 1 } }
`
	h := newHarness(t, mustFlow(t, doc))
	var tests atomic.Int32
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "./test.sh") && tests.Add(1) == 1 {
			return envport.ExecResult{ExitCode: 1, Output: "one test fails"}, nil
		}
		return envport.ExecResult{}, nil
	}
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "sequences"}, "seq", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	evs := h.events(t, id)
	if v := h.view(t, id); v.State != run.Completed {
		t.Fatalf("state = %s; failures: %s", v.State, failures(evs))
	}
	var started []string
	for _, e := range evs {
		if s, ok := e.Payload.(*event.StageStarted); ok {
			started = append(started, s.StageID)
		}
	}
	if got := strings.Join(started, " "); got != "build check.lint check.test build check.lint check.test" {
		t.Errorf("stages started: %s", got)
	}
	edge, _ := findPayload[*event.EdgeTaken](evs)
	if edge == nil || edge.From != "check.test" || edge.To != "build" {
		t.Errorf("edge = %+v, want check.test's fail back to build", edge)
	}
}
