package engine

// Tracker actions (FLOWLANG §2 action, ticket_*): the run writes back to the
// issue its ticket was imported from — moves it, comments on it, fills a
// field, attaches the merge request or the branch. The tracker is the
// TICKET's (the ticketsource integration recorded at import), never the
// project's, and a
// ticket written by hand has none: those stages complete as SKIPPED, visibly
// (stage_completed{skipped, note}), and the run goes on down on_success.
//
// Every call runs engine-side like the repository host's, so no tracker
// credential ever reaches a workspace; and none of these needs the workspace
// unless a file input is bound (actionspec.Action.Remote, needsWorkspace) —
// the in-progress move lands seconds after the run starts, and the done move
// after a parked wait never re-provisions a reaped workspace.
//
// A move names a CANONICAL status (`to: in_progress`, ports.TicketStatus)
// that the ticket's tracker translates into its own vocabulary — the same
// flow moves a Jira issue to "In Progress" and leaves a GitHub issue open —
// or the tracker's literal status (`status: "Ready for prod"`). A canonical
// status the tracker has no word for is not a failure: the stage completes
// as SKIPPED with the reason, like a ticket without a tracker.
//
// Failures are OUTCOME failures (actionFail → on_fail, else the run is
// abandoned): a status the tracker cannot reach, a field it cannot write, a
// tracker that is down are all things a human fixes outside the run — retry,
// debug, or route past the stage. Nothing here is terminal.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"apothecary/internal/actionspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
)

// actionSkip records a completion that did no work — the stage had nothing
// to act on — and follows on_success. The sibling of actionFail (and like
// it, returns routed=true).
func (x *exec) actionSkip(st *flow.Stage, note string) (bool, error) {
	x.e.log.Info("stage skipped", "run", x.rs.runID, "stage", st.ID, "note", note)
	if err := x.append(&event.StageCompleted{StageID: st.ID, Skipped: true, Note: note}); err != nil {
		return true, err
	}
	x.advance(st)
	return true, nil
}

// trackerTarget is a resolved tracker action target: the writer and the
// issue key it acts on.
type trackerTarget struct {
	w   ports.TicketWriter
	key string
}

// tracker resolves the writer for the run's ticket. routed=true means the
// stage is already settled — skipped (no tracker issue) or failed (the
// integration cannot write) — and the executor returns (true, err).
func (x *exec) tracker(st *flow.Stage) (tg trackerTarget, routed bool, err error) {
	rs := x.rs
	if rs.view.TicketSourceID == "" || rs.view.TicketKey == "" {
		routed, err = x.actionSkip(st, "the ticket has no tracker issue (it was written by hand); nothing to write to")
		return tg, routed, err
	}
	if x.res.TicketWriter == nil {
		routed, err = x.actionFail(st, event.ReasonInfra, st.Action+": this build cannot write to a ticket tracker")
		return tg, routed, err
	}
	w, werr := x.res.TicketWriter(x.ctx, rs.view.TicketSourceID)
	if werr != nil {
		routed, err = x.actionFail(st, event.ReasonInfra, fmt.Sprintf("%s: ticket %s's tracker: %v", st.Action, rs.view.TicketKey, werr))
		return tg, routed, err
	}
	return trackerTarget{w: w, key: rs.view.TicketKey}, false, nil
}

// trackerFail routes a tracker error: a canonical status the tracker has no
// word for skips the stage (the flow fits, this tracker cannot say it); a
// typed refusal (status unreachable, field unknown or unwritable) is the
// flow's literal not fitting the tracker — input_invalid; anything else is
// the tracker itself — infra, still routable. A cancelled run is none of
// these.
func (x *exec) trackerFail(st *flow.Stage, err error) (bool, error) {
	if x.ctx.Err() != nil {
		return true, context.Cause(x.ctx)
	}
	var noWord *ports.ErrStatusUnsupported
	if errors.As(err, &noWord) {
		return x.actionSkip(st, st.Action+": "+err.Error())
	}
	reason := event.ReasonInfra
	var unreachable *ports.ErrStatusUnreachable
	var unknown *ports.ErrFieldUnknown
	var unsupported *ports.ErrFieldUnsupported
	if errors.As(err, &unreachable) || errors.As(err, &unknown) || errors.As(err, &unsupported) {
		reason = event.ReasonInputInvalid
	}
	return x.actionFail(st, reason, st.Action+": "+err.Error())
}

// actionTicketTransition moves the issue — to a canonical status the tracker
// translates (`to`) or to its literal one (`status`); the parser holds the
// stage to exactly one. Idempotent by nature: the adapter reads the current
// status first, and "already there" is a no-op.
func (x *exec) actionTicketTransition(st *flow.Stage) (bool, error) {
	tg, routed, err := x.tracker(st)
	if routed {
		return true, err
	}
	to := strings.TrimSpace(st.Setting("to", ""))
	if to != "" {
		err = tg.w.TransitionTo(x.ctx, tg.key, ports.TicketStatus(to))
	} else {
		to = strings.TrimSpace(st.Setting("status", ""))
		err = tg.w.Transition(x.ctx, tg.key, to)
	}
	if err != nil {
		return x.trackerFail(st, err)
	}
	x.e.log.Info("ticket moved", "run", x.rs.runID, "stage", st.ID, "ticket", tg.key, "to", to)
	return false, nil
}

// ticketTrailer is the line a posted comment ends with: who posted it and
// for what. It is also the comment's idempotency key — looked for on a
// re-entry — so it names the run, the phase run and the stage, the way
// review replies are marked, and NEVER the attempt: a failed-then-retried
// attempt must recognise the comment it already posted. Visible text rather
// than an HTML comment, because Jira renders comments as ADF and would strip
// one.
func (x *exec) ticketTrailer(st *flow.Stage) string {
	return fmt.Sprintf("— apothecary · run %s · stage %s", x.roundID(), st.ID)
}

// actionTicketComment posts the body artifact as a comment. The effect
// lands outside the run, so an intent (action_started) is recorded first and
// a re-entry with the intent still pending asks the tracker whether the
// comment is already there before posting again (PLAN5 M20, as create_mr).
// The files of its attachments port go up to the tracker just before the
// comment (ports.TicketAttacher), and the body links them (attach.go).
func (x *exec) actionTicketComment(st *flow.Stage) (bool, error) {
	rs := x.rs
	tg, routed, err := x.tracker(st)
	if routed {
		return true, err
	}
	trailer := x.ticketTrailer(st)
	if rs.actionPending[st.ID] {
		// Reconcile before reading anything: the comment may already be
		// there, and then neither the body nor the workspace matters.
		existing, cerr := tg.w.Comments(x.ctx, tg.key)
		if cerr != nil {
			return x.trackerFail(st, fmt.Errorf("cannot tell whether the comment was already posted: %w", cerr))
		}
		for _, c := range existing {
			if strings.Contains(c, trailer) {
				x.e.log.Info("adopted a comment posted before an interrupted record",
					"run", rs.runID, "stage", st.ID, "ticket", tg.key)
				delete(rs.actionPending, st.ID)
				return false, nil
			}
		}
	}
	path := inputPath(st, "body")
	data, rerr := x.res.Envp.ReadFile(x.ctx, rs.envID, path)
	if rerr != nil {
		return x.actionFail(st, event.ReasonInputMissing, fmt.Sprintf("comment body %s not readable: %v", path, rerr))
	}
	body := strings.TrimSpace(string(data))
	if body == "" {
		return x.actionFail(st, event.ReasonInputMissing, fmt.Sprintf("comment body %s is empty", path))
	}
	files, routed, err := x.attachmentFiles(st)
	if routed || err != nil {
		return true, err
	}
	var attacher ports.TicketAttacher
	if len(files) > 0 {
		var ok bool
		if attacher, ok = tg.w.(ports.TicketAttacher); !ok {
			return x.actionFail(st, event.ReasonInfra, fmt.Sprintf("%s: ticket %s's tracker cannot host attachments", st.Action, tg.key))
		}
	}
	if !rs.actionPending[st.ID] {
		// Record the intent BEFORE the effect: a crash past this point makes
		// the next boot reconcile instead of posting a second comment.
		if err := x.append(&event.ActionStarted{StageID: st.ID, Action: st.Action}); err != nil {
			return true, err
		}
		rs.actionPending[st.ID] = true
	}
	if len(files) > 0 {
		body, err = x.attach(body, files, func(name, ct string, data []byte) (ports.Attachment, error) {
			return attacher.Attach(x.ctx, tg.key, name, ct, data)
		})
		if err != nil {
			return x.trackerFail(st, err)
		}
		body = strings.TrimSpace(body)
	}
	if err := tg.w.Comment(x.ctx, tg.key, body+"\n\n"+trailer); err != nil {
		return x.trackerFail(st, err)
	}
	delete(rs.actionPending, st.ID)
	return false, nil
}

// actionTicketSetField writes one field: its value from the bound artifact
// or the flow's own text, replacing the field or merging into it. Both modes
// are read-compare-write in effect (append is a union), so a re-entry
// changes nothing.
func (x *exec) actionTicketSetField(st *flow.Stage) (bool, error) {
	rs := x.rs
	tg, routed, err := x.tracker(st)
	if routed {
		return true, err
	}
	field := strings.TrimSpace(st.Setting("field", ""))
	var value string
	if path := inputPath(st, "value"); path != "" {
		data, rerr := x.res.Envp.ReadFile(x.ctx, rs.envID, path)
		if rerr != nil {
			return x.actionFail(st, event.ReasonInputMissing, fmt.Sprintf("field value %s not readable: %v", path, rerr))
		}
		value = strings.TrimSpace(string(data))
	} else {
		value = strings.TrimSpace(st.Setting("text", ""))
	}
	if st.Setting("mode", actionspec.ModeReplace) == actionspec.ModeAppend {
		cur, ferr := tg.w.Fields(x.ctx, tg.key, []string{field})
		if ferr != nil {
			return x.trackerFail(st, ferr)
		}
		value = mergeItems(cur[field], value)
	}
	if err := tg.w.SetFields(x.ctx, tg.key, map[string]string{field: value}); err != nil {
		return x.trackerFail(st, err)
	}
	x.e.log.Info("ticket field set", "run", rs.runID, "stage", st.ID, "ticket", tg.key, "field", field)
	return false, nil
}

// mergeItems is append mode: the union of two comma-separated lists, in order
// of first appearance, so applying the same value twice changes nothing.
func mergeItems(current, added string) string {
	var out []string
	seen := map[string]bool{}
	for part := range strings.SplitSeq(current+","+added, ",") {
		p := strings.TrimSpace(part)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return strings.Join(out, ", ")
}

// actionTicketLink attaches the run's merge request(s) to the issue — every
// one the run opened, or the subject's when the chain acts on one. Idempotent in
// the adapter (an upsert on URL), so a re-entry never attaches twice.
func (x *exec) actionTicketLink(st *flow.Stage) (bool, error) {
	rs := x.rs
	tg, routed, err := x.tracker(st)
	if routed {
		return true, err
	}
	mrs := rs.mrs
	if co := x.subjectCheckout(); co != nil {
		mrs = nil
		if mr := rs.mrFor(co.Name, co.Branch); mr != nil {
			mrs = []mrRecord{*mr}
		}
	}
	if len(mrs) == 0 {
		return x.actionFail(st, event.ReasonInputMissing, "no merge request to link: ticket_link runs after create_mr")
	}
	for _, mr := range mrs {
		title := "Merge request: " + mr.Branch
		if mr.Repo != "" && mr.Repo != event.PrimaryCheckoutName {
			title = fmt.Sprintf("Merge request (%s): %s", mr.Repo, mr.Branch)
		}
		if err := tg.w.AddLink(x.ctx, tg.key, ports.TicketLink{Title: title, URL: mr.Ref.URL}); err != nil {
			return x.trackerFail(st, err)
		}
	}
	x.e.log.Info("ticket linked to merge request(s)", "run", rs.runID, "stage", st.ID, "ticket", tg.key, "count", len(mrs))
	return false, nil
}

// actionTicketLinkBranch attaches the run's branch to the issue — every
// checkout's that is on its repository host, or the subject's when the chain
// acts on one — linking the host's own page for it, so the link always
// resolves. An extra repo the run left alone was never pushed and is passed
// over; the primary (or the subject) not being there means the flow links
// before it pushes, which input_missing says. Idempotent like ticket_link.
func (x *exec) actionTicketLinkBranch(st *flow.Stage) (bool, error) {
	rs := x.rs
	tg, routed, err := x.tracker(st)
	if routed {
		return true, err
	}
	checkouts := rs.checkouts
	if co := x.subjectCheckout(); co != nil {
		checkouts = []event.Checkout{*co}
	}
	linked := 0
	for _, co := range checkouts {
		repoHost := x.res.RepoHostFor(co.Name)
		if repoHost == nil {
			continue // context-only extra repo: nowhere to link to
		}
		url, ok, err := repoHost.BranchURL(x.ctx, x.repoRefFor(co), co.Branch)
		if err != nil {
			if x.ctx.Err() != nil {
				return true, context.Cause(x.ctx)
			}
			return x.actionFail(st, event.ReasonInfra, fmt.Sprintf("reading branch %s on the repository host: %v", co.Branch, err))
		}
		if !ok {
			if co.Primary || len(checkouts) == 1 {
				return x.actionFail(st, event.ReasonInputMissing, fmt.Sprintf("branch %s is not on the repository host yet: ticket_link_branch runs after push_branch", co.Branch))
			}
			continue
		}
		title := "Branch: " + co.Branch
		if !co.Primary {
			title = fmt.Sprintf("Branch (%s): %s", co.Name, co.Branch)
		}
		if err := tg.w.AddLink(x.ctx, tg.key, ports.TicketLink{Title: title, URL: url}); err != nil {
			return x.trackerFail(st, err)
		}
		linked++
	}
	if linked == 0 {
		return x.actionSkip(st, "no checkout of this run has a repository host to link a branch from")
	}
	x.e.log.Info("ticket linked to branch(es)", "run", rs.runID, "stage", st.ID, "ticket", tg.key, "count", linked)
	return false, nil
}
