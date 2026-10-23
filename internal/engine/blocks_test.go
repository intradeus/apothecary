package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// A block runs as its expanded stages: the events name them by their
// include path, and `fail` leaves the block by the include's on_fail — here
// back to build, whose second pass gets through.
func TestIncludedBlockRunsAsItsStages(t *testing.T) {
	doc := `name: blk
version: 4
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
blocks:
  - id: check
    stages:
      - { id: lint, type: command, run: "./lint.sh" }
      - { id: test, type: command, run: "./test.sh", on_fail: fail }
stages:
  - { id: build, type: command, run: "./build.sh" }
  - { id: check, include: check, on_fail: { to: build, max: 1 } }
`
	h := newHarness(t, mustFlow(t, doc))
	var tests atomic.Int32
	h.envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "./test.sh") && tests.Add(1) == 1 {
			return ports.ExecResult{ExitCode: 1, Output: "one test fails"}, nil
		}
		return ports.ExecResult{}, nil
	}
	id, err := h.e.CreateRun(context.Background(), Ticket{Title: "blocks"}, "blk", testProject)
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
