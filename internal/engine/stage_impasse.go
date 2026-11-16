package engine

import (
	"fmt"
	"strings"

	"apothecary/internal/flow"
	"apothecary/internal/platform/shell"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
)

// stageImpasse is the impasse (FLOWLANG §3): st was rejected past its
// on_reject budget, and a person decides what follows. No stage of its own —
// the ping belongs to st — and, unlike the debug leaf, nobody needs the
// checkout: the chain parks like a gate (parkAt) and the idle loop routes
// the answer when it is recorded (runcore.State.PlanResume, routeImpasse).
// The engine composes no prose: the ping carries which stage, the verdict
// that spent the chain and the choices, and the card is the UI's.
func (x *exec) stageImpasse(st *flow.Stage) error {
	rs := x.rs
	// Answered before the chain parked — a crash between the question and
	// the park, or after the resume but before the routing: route it.
	if rs.GateAnswer != nil {
		return x.routeImpasse(st, rs.GateAnswer)
	}
	// Asked already (recovery): park on that question, never a second one.
	if op := rs.OpenPing(st.ID, PingImpasse); op != nil {
		return x.parkAt(st, op.PingID)
	}
	p := &Ping{Type: PingImpasse, Question: rejectedQuestion(st.ID, x.rejections(st)),
		Context: rs.Feed, StageID: st.ID, AnswerKind: event.AnswerChoice, Choices: impasseChoices(st),
		Phase: rs.Phase, EndsPhase: !x.endsRunAtDeadEnd()}
	if err := x.raiseYesNo(p); err != nil {
		return err
	}
	x.e.notify(x.bg, notifyport.Notification{
		RunID: rs.RunID, PingID: p.ID, Type: p.Type,
		Title: p.Question, Body: p.Primary(),
	})
	return x.parkAt(st, p.ID)
}

// rejections is how many rejections in a row brought st to the impasse: one
// per stage link of its on_reject chain taken since the chain's budget was
// last restored, plus the one that reached the impasse.
func (x *exec) rejections(st *flow.Stage) int {
	n, depth := 1, 0
	for e := st.Edge(flow.EdgeReject); e != nil; e, depth = e.Then, depth+1 {
		if e.To != flow.ReservedImpasse {
			n += x.rs.EdgeCounts[runcore.ChainKey(st.ID, flow.EdgeReject, depth)]
		}
	}
	return n
}

// rejectedQuestion is the impasse's question: which stage, how many
// rejections in a row.
func rejectedQuestion(stage string, n int) string {
	if n == 1 {
		return stage + " rejected once"
	}
	return fmt.Sprintf("%s rejected %d times", stage, n)
}

// impasseChoices is what the impasse offers for st. Keep going needs a stage
// to send the rejection to: a chain that is the impasse alone
// (`on_reject: impasse`) has none, and would only ask again.
func impasseChoices(st *flow.Stage) []event.Choice {
	choices := event.ImpasseChoices()
	if e := st.Edge(flow.EdgeReject); e != nil && e.To == flow.ReservedImpasse {
		return choices[1:]
	}
	return choices
}

// routeImpasse acts on the person's decision at the impasse.
//
//   - keep going: the rejected stage's on_reject budget is already back
//     (folded from the answer, runcore.State.RestoreRejectBudget), so the
//     rejection takes its chain again from the first link, carrying the
//     verdict.
//   - skip: the stage's outputs are cleared — a stale REJECT must never read
//     as current — and it completes as skipped, by whoever answered, and
//     on_success follows with nothing carried.
//   - give up: a dead end, as at the debug leaf.
func (x *exec) routeImpasse(st *flow.Stage, ans *event.PingAnswered) error {
	rs := x.rs
	note := strings.TrimSpace(x.redact.Scrub(ans.Feedback))
	switch ans.Answer {
	case event.ChoiceKeepGoing:
		return x.takeEdge(st, flow.EdgeReject, rs.Feed)
	case event.ChoiceSkip:
		// Best effort: with no workspace there is nothing stale to clear —
		// the next stage that needs one starts from a fresh checkout.
		if rs.EnvID != "" {
			x.clearOutputs(st.Outputs)
		}
		why := "skipped by a person at the impasse"
		if note != "" {
			why += ": " + shell.Tail(note, 2<<10)
		}
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: rs.Iters[st.ID] + 1}); err != nil {
			return err
		}
		if err := x.append(&event.StageCompleted{StageID: st.ID, Skipped: true, Note: why, By: ans.By}); err != nil {
			return err
		}
		rs.RouteSuccess(st, "")
		return nil
	case event.ChoiceGiveUp:
		reason := fmt.Sprintf("gave up at the impasse of stage %s", st.ID)
		if note != "" {
			reason += ": " + shell.Tail(note, 2<<10)
		}
		return x.deadEnd(st, "", reason)
	}
	return fmt.Errorf("impasse at stage %s: unknown answer %q", st.ID, ans.Answer)
}
