package runcore

import (
	"apothecary/internal/run/event"
	"apothecary/internal/run/ref"
)

// PingRefs gives each ping_raised in ps that has none its reference
// (apothecary/internal/run/ref): the n-th ping raised in or about its stage's
// latest attempt, whatever its type (build#3/decision#1) — a debug or an
// impasse ping comes after the attempt it is about, and belongs to it — or
// run/decision#n for a ping with no stage, or whose stage has not started.
//
// A ping the run already holds keeps the reference it was first recorded
// with (a re-raise after a crash is the same ping). Called as the pings are
// recorded, before they are folded, so two in one batch are numbered in
// their order. The number depends on the log alone: a replay gives the same.
func (rs *State) PingRefs(ps []event.Payload) {
	next := map[string]int{} // attempt reference → the last number given
	for _, p := range ps {
		pr, ok := p.(*event.PingRaised)
		if !ok || pr.Ref != "" {
			continue
		}
		if had := rs.RaisedPings[pr.PingID]; had != nil && had.Ref != "" {
			pr.Ref = had.Ref
			continue
		}
		attempt := ""
		if n := rs.Attempts[pr.StageID]; pr.StageID != "" && n > 0 {
			attempt = ref.Attempt(pr.StageID, n)
		}
		if _, ok := next[attempt]; !ok {
			next[attempt] = rs.pingsIn(attempt)
		}
		next[attempt]++
		pr.Ref = ref.In(attempt, ref.Decision, next[attempt])
	}
}

// pingsIn counts the pings the run has raised in an attempt ("": outside
// any).
func (rs *State) pingsIn(attempt string) int {
	n := 0
	for _, p := range rs.RaisedPings {
		if r, err := ref.Parse(p.Ref); err == nil && r.Kind == ref.Decision && r.AttemptRef() == attempt {
			n++
		}
	}
	return n
}
