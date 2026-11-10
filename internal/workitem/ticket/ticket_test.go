package ticket

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workitem"
)

func TestTicketKind(t *testing.T) {
	k := Kind{}
	if k.Name() != "ticket" || k.Noun() != "ticket" || k.Required() || !k.Accompanies() {
		t.Error("kind")
	}
	ref, ok := k.Requested("p", runinput.Ticket{TicketID: "tk"}, runinput.Input{})
	if !ok || ref != Ref("p", "tk") || k.Describe(ref) != "ticket tk" {
		t.Errorf("requested = %v %v", ref, ok)
	}
	if _, ok := k.Requested("p", runinput.Ticket{Title: "bare"}, runinput.Input{}); ok {
		t.Error("a bare title named a ticket")
	}
	if held := k.Held(run.View{ProjectID: "p", TicketID: "tk"}); len(held) != 1 || held[0] != ref {
		t.Errorf("held = %v", held)
	}
	if held := k.Held(run.View{ProjectID: "p"}); held != nil {
		t.Errorf("a ticketless run held %v", held)
	}
}

// Each run of a ticket gets its own branch: the ticket's base, or one from
// its title, plus the run's tail.
func TestTicketPrepare(t *testing.T) {
	ctx := context.Background()
	s := &workitem.Start{RunID: "01RUNABCD", Title: "Fix the login form"}
	if err := (Kind{}).Prepare(ctx, s, Ref("p", "tk")); err != nil {
		t.Fatal(err)
	}
	if s.Base != "apothecary/fix-the-login-form" || s.Branch != "apothecary/fix-the-login-form-abcd" {
		t.Errorf("branch = %q base %q", s.Branch, s.Base)
	}
	s = &workitem.Start{RunID: "01RUNABCD", Title: "x", Base: "feat/DEV-1"}
	if err := (Kind{}).Prepare(ctx, s, Ref("p", "tk")); err != nil || s.Branch != "feat/DEV-1-abcd" {
		t.Errorf("own base: %q, %v", s.Branch, err)
	}
	s = &workitem.Start{RunID: "r", Title: "x", Base: "bad..base"}
	if err := (Kind{}).Prepare(ctx, s, Ref("p", "tk")); err == nil || !strings.Contains(err.Error(), `branch "bad..base-r"`) {
		t.Errorf("a bad base = %v", err)
	}
}
