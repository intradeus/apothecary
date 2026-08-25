package engine

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/ports"
)

// lastPrompt returns the prompt of the most recent agent call.
func lastPrompt(h *harness) string {
	var p string
	for _, c := range h.runner.RunCalls {
		p = c.In.Prompt
	}
	return p
}

// A run's own direction is ADDITIVE: it gets its own prompt section and the
// ticket's goal stays the # TASK spec.
func TestRunGoalRendersAdditionalDirection(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	runID, err := h.e.CreateRun(context.Background(), Ticket{
		TicketID: "tk-1",
		Title:    "Add a health endpoint",
		Goal:     "Expose /healthz returning 200.",
		RunGoal:  "Last try broke the router — use the existing middleware.",
	}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	prompt := lastPrompt(h)
	if !strings.Contains(prompt, "# ADDITIONAL DIRECTION FOR THIS ATTEMPT") ||
		!strings.Contains(prompt, "use the existing middleware") {
		t.Errorf("prompt missing the run direction:\n%s", prompt)
	}
	// The ticket's goal is untouched — direction never replaces the spec.
	if !strings.Contains(prompt, "Expose /healthz returning 200.") {
		t.Errorf("ticket goal missing from # TASK:\n%s", prompt)
	}
}

// No direction ⇒ no empty section.
func TestNoRunGoalOmitsDirectionSection(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	runID, err := h.e.CreateRun(context.Background(),
		Ticket{TicketID: "tk-1", Title: "T", Goal: "g"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if strings.Contains(lastPrompt(h), "ADDITIONAL DIRECTION") {
		t.Errorf("empty direction section rendered:\n%s", lastPrompt(h))
	}
}

// A TICKET's attachments reach EVERY run of that ticket — the bug being
// fixed (the old table bound one run_id, so they used to reach only the
// first run; ownership is structural now).
func TestTicketAttachmentsReachEveryRun(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	att, snaps, _ := newFiles(t)
	h.e.SetFiles(att, snaps)
	up, err := att.Save(context.Background(), "design.md", "text/markdown", strings.NewReader("# Design"))
	if err != nil {
		t.Fatal(err)
	}
	if err := att.ClaimForTicket(context.Background(), []string{up.ID}, "tk-shared"); err != nil {
		t.Fatal(err)
	}
	wantPath := ".apothecary/attachments/goal/" + up.ID[:8] + "/design.md"

	for attempt := 1; attempt <= 2; attempt++ {
		runID, err := h.e.CreateRun(context.Background(), Ticket{
			TicketID: "tk-shared", Title: "T", Goal: "g",
			TicketAttachmentIDs: []string{up.ID},
		}, "loop", testProject)
		if err != nil {
			t.Fatal(err)
		}
		h.e.Wait(runID)

		if p := lastPrompt(h); !strings.Contains(p, "# ATTACHED FILES") || !strings.Contains(p, wantPath) {
			t.Errorf("attempt %d: prompt missing the ticket attachment:\n%s", attempt, p)
		}
		env := h.view(t, runID).EnvID
		data, err := h.envp.ReadFile(context.Background(), ports.EnvID(env), wantPath)
		if err != nil || string(data) != "# Design" {
			t.Errorf("attempt %d: ticket artifact not delivered: %q, %v", attempt, data, err)
		}
	}
}

// Restart carries this attempt's direction (and a flow override) onto the new
// run, while keeping the ticket linkage.
func TestRestartCarriesDirection(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	id, err := h.e.CreateRun(ctx,
		Ticket{TicketID: "tk-9", Title: "T", Goal: "g", Branch: "feat/x"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)

	newID, err := h.e.RestartRun(ctx, id, RunInput{Goal: "avoid the cache this time"})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(newID)

	nv := h.view(t, newID)
	if nv.RunGoal != "avoid the cache this time" {
		t.Errorf("restart run_goal = %q", nv.RunGoal)
	}
	if nv.TicketID != "tk-9" {
		t.Errorf("restart lost the ticket link: %q", nv.TicketID)
	}
	if !strings.Contains(lastPrompt(h), "avoid the cache this time") {
		t.Errorf("restart direction not in the prompt:\n%s", lastPrompt(h))
	}
}
