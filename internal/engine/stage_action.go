package engine

import (
	"fmt"
	"strings"

	"apothecary/internal/actionspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/runcore"
)

// stageAction dispatches to the executor for the stage's registered action
// (actionspec). Executors must be idempotent: an action stage always re-enters
// its executor on recovery (the routeAfterFail shortcut skips it), because a
// half-finished side effect on a real service cannot be replayed away.
func (x *exec) stageAction(st *flow.Stage) error {
	rs := x.rs
	if rs.Ph != runcore.PhIn {
		iter := rs.Iters[st.ID] + 1
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
			return err
		}
		// What it reads must be there and hold its shape, as for any stage:
		// an action handed a malformed file would act on it. Not while an
		// effect is pending: the executor reconciles first, and an effect
		// that already landed needs neither the file nor the workspace.
		if reason, msg := x.checkStructuredInputs(st); reason != "" && !rs.ActionPending[st.ID] {
			if err := x.append(&event.StageFailed{StageID: st.ID, Reason: reason, Error: msg}); err != nil {
				return err
			}
			return x.takeEdge(st, flow.EdgeFail, msg)
		}
	}
	started := x.e.clock()

	// Executors report routed=true when they already decided where the run
	// goes next (an on_fail or on_next edge, a terminal failure); otherwise
	// the stage completes and follows on_success.
	var routed bool
	var err error
	switch st.Action {
	case actionspec.CreateMR:
		routed, err = x.actionCreateMR(st)
	case actionspec.LoadReviewComments:
		routed, err = x.actionLoadReviewComments(st)
	case actionspec.ReplyReviewComments:
		routed, err = x.actionReplyReviewComments(st)
	case actionspec.SyncTarget:
		routed, err = x.actionSyncTarget(st)
	case actionspec.Commit:
		routed, err = x.actionCommit(st)
	case actionspec.PushBranch:
		routed, err = x.actionPushBranch(st)
	case actionspec.MRMerge:
		routed, err = x.actionMRMerge(st)
	case actionspec.PostReview:
		routed, err = x.actionPostReview(st)
	case actionspec.TicketTransition:
		routed, err = x.actionTicketTransition(st)
	case actionspec.TicketComment:
		routed, err = x.actionTicketComment(st)
	case actionspec.TicketSetField:
		routed, err = x.actionTicketSetField(st)
	case actionspec.TicketLink:
		routed, err = x.actionTicketLink(st)
	case actionspec.TicketLinkBranch:
		routed, err = x.actionTicketLinkBranch(st)
	default:
		// Unreachable: the parser validates the slug against the registry.
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInfra,
			Error: fmt.Sprintf("action %q has no executor in this build", st.Action)}); aerr != nil {
			return aerr
		}
		return x.fail(fmt.Sprintf("stage %s: action %q has no executor", st.ID, st.Action))
	}
	if err != nil || rs.Done || routed {
		return err
	}

	if err := x.append(&event.StageCompleted{StageID: st.ID, DurationMS: x.e.clock().Sub(started).Milliseconds()}); err != nil {
		return err
	}
	if x.emptyRound(st) {
		x.rs.EndPhase()
		return nil
	}
	x.rs.RouteSuccess(st, "")
	return nil
}

// mrTitle extracts the first `# ` heading, else falls back to the ticket title (§3.1).
func mrTitle(body, ticketTitle string) string {
	for line := range strings.SplitSeq(body, "\n") {
		if t, ok := strings.CutPrefix(line, "# "); ok && strings.TrimSpace(t) != "" {
			return strings.TrimSpace(t)
		}
	}
	return ticketTitle
}
