package engine

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

const manualDoc = `name: hands
version: 5
parameters: { harness: fake, model: fake/model }
main:
  - { id: work, type: manual, question: "do it", input: { ticket: { from: run.ticket, type: ticket } } }
  - { id: after, type: command, run: "./after.sh" }
`

// countAfter counts ./after.sh runs; every other command succeeds.
func countAfter(h *harness) *int {
	n := new(int)
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "./after.sh") {
			*n++
		}
		return envport.ExecResult{}, nil
	}
	return n
}

// A manual stage hands the workspace to a person: the run waits on them
// with the brief on the card and in the workspace, nothing after the stage
// runs, and Done goes on.
func TestManualStageHandsTheWorkspaceOver(t *testing.T) {
	h := newHarness(t, mustFlow(t, manualDoc))
	afters := countAfter(h)
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "Fix the login"}, "hands", testProject)
	if err != nil {
		t.Fatal(err)
	}
	p := h.waitPing(t, PingManual)
	if p.StageID != "work" || p.Phase != flow.MainPhase || p.EndsPhase {
		t.Errorf("ping = stage %q phase %q ends_phase %v; want work in the main chain", p.StageID, p.Phase, p.EndsPhase)
	}
	if !strings.Contains(p.Context, "do it") || !strings.Contains(p.Context, "Fix the login") {
		t.Errorf("the card's brief:\n%s", p.Context)
	}
	waitEvent[*event.RunPaused](t, h, runID)
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if rp, ok := evs[len(evs)-1].Payload.(*event.RunPaused); !ok || rp.Reason != event.PauseWaiting {
		t.Fatalf("last event = %T, want run_paused waiting_on_human; events: %v", evs[len(evs)-1].Payload, eventTypes(evs))
	}
	brief, err := h.envp.ReadFile(context.Background(), envport.EnvID(h.view(t, runID).EnvID), ".apothecary/manual/work.md")
	if err != nil || string(brief) != p.Context {
		t.Errorf("the workspace's brief = %q (%v), want the card's", brief, err)
	}
	if *afters != 0 {
		t.Fatalf("./after.sh ran %d times before Done", *afters)
	}

	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve", Feedback: "built it"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs = h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(evs))
	}
	if sc, ok := findPayload[*event.StageCompleted](evs); !ok || sc.StageID != "work" {
		t.Errorf("first stage_completed = %+v, want work's", sc)
	}
	if *afters != 1 {
		t.Errorf("./after.sh ran %d times, want once after Done", *afters)
	}
}

// Giving up at a manual stage is a dead end: in the main chain the run is
// abandoned, with the person's words.
func TestManualStageGiveUpAbandonsTheRun(t *testing.T) {
	h := newHarness(t, mustFlow(t, manualDoc))
	afters := countAfter(h)
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "hands", testProject)
	if err != nil {
		t.Fatal(err)
	}
	p := h.waitPing(t, PingManual)
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "reject", Feedback: "not today"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Abandoned {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(evs))
	}
	ra, _ := findPayload[*event.RunAbandoned](evs)
	if !strings.Contains(ra.Reason, "gave up at manual stage work: not today") {
		t.Errorf("run_abandoned = %+v", ra)
	}
	if *afters != 0 {
		t.Errorf("./after.sh ran %d times after giving up", *afters)
	}
}

// What a reviewer rejected comes back to the person, in the brief.
func TestManualStageShowsWhatCameBack(t *testing.T) {
	doc := `name: back
version: 5
parameters: { harness: fake, model: fake/model }
main:
  - { id: work, type: manual, input: { review: { from: edge.verdict, type: verdict, optional: true } } }
  - { id: review, type: agent, harness: $harness, model: $model, agent: reviewer,
      output: { verdict: { to: artifacts/verdict.json, type: verdict } },
      on_reject: { to: work, max: -1 } }
`
	h := newHarness(t, mustFlow(t, doc))
	reviews := 0
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"reviewer": func(c fake.RunCall) (agentport.AgentResult, error) {
			reviews++
			body := `{"verdict":"APPROVE","comments":[]}`
			if reviews == 1 {
				body = `{"verdict":"REJECT","comments":["the login still loops"]}`
			}
			h.writeFile(t, c.Env, "artifacts/verdict.json", body)
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "back", testProject)
	if err != nil {
		t.Fatal(err)
	}
	p1 := h.waitPing(t, PingManual)
	if strings.Contains(p1.Context, "# INPUT: review") {
		t.Errorf("a first entry carries no verdict:\n%s", p1.Context)
	}
	if err := h.e.AnswerPing(p1.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	p2 := h.waitPing(t, PingManual, p1.ID)
	if !strings.Contains(p2.Context, "REJECTED by the review at stage review") || !strings.Contains(p2.Context, "the login still loops") {
		t.Errorf("the second brief lacks the rejection:\n%s", p2.Context)
	}
	if err := h.e.AnswerPing(p2.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, runID)))
	}
}
