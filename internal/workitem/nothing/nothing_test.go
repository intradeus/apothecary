package nothing

import (
	"context"
	"testing"

	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workitem"
)

// The kind has no items: a start names none whatever it carries, a run
// holds none, and preparing changes nothing.
func TestNothingHasNoItems(t *testing.T) {
	k := Kind{}
	if k.Name() != "nothing" || k.Noun() != "nothing" || k.Describe(workitem.Ref{}) != "nothing" {
		t.Errorf("names = %q %q %q", k.Name(), k.Noun(), k.Describe(workitem.Ref{}))
	}
	if k.Required() || k.Accompanies() || !k.Alone() {
		t.Errorf("required %v, accompanies %v, alone %v", k.Required(), k.Accompanies(), k.Alone())
	}
	if ref, ok := k.Requested("p", runinput.Ticket{TicketID: "t1", Title: "T"}, runinput.Input{MR: &runinput.MR{IID: 3}}); ok {
		t.Errorf("requested %v", ref)
	}
	if held := k.Held(run.View{TicketID: "t1"}); held != nil {
		t.Errorf("held %v", held)
	}
	s := &workitem.Start{Branch: "b", Base: "b"}
	if err := k.Prepare(context.Background(), s, workitem.Ref{}); err != nil || s.Branch != "b" {
		t.Errorf("prepare = %v, %+v", err, s)
	}
}
