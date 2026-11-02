package engine

// Tracker actions (FLOWLANG §2 action, ticket_*): the run writes to its
// ticket's tracker — moves the ticket, comments on it, fills a field,
// attaches the merge request or the branch. Every ticket has exactly one
// tracker, resolved per call from the ticket's CURRENT row
// (Resolution.TicketTracker), never from the run's snapshot: the remote issue
// it mirrors (Jira, GitHub, GitLab) when it has a source, else Apothecary's
// internal tracker, which writes the ticket row itself. A run with no ticket
// has nothing to write to: those stages complete as SKIPPED, visibly
// (stage_completed{skipped, note}), and the run goes on down on_success.
//
// Every call runs engine-side like the repository host's, so no tracker
// credential ever reaches a workspace; and none of these needs the workspace
// unless a file input is bound (actionspec.Action.Remote, runcore.NeedsWorkspace) —
// the in-progress move lands seconds after the run starts, and the done move
// after a parked wait never re-provisions a reaped workspace. Each call
// carries the stage as its actor (resolution.TrackerActor), which the
// internal tracker records as who moved or commented.
//
// A move names a flow STEP (`to: in_progress`, trackerport.FlowStep), which
// the ticket's tracker's status list resolves to the status holding it
// (Resolution.TicketStep) — so the same flow moves a Jira issue to "In
// Progress" and a hand-written ticket to its own — or the tracker's literal
// status (`status: "Ready for prod"`). A step no status holds is not a
// failure: the stage completes as SKIPPED with the reason. So is a write
// with no meaning on this tracker (*trackerport.ErrNotApplicable: a field
// the internal tracker does not have, a ticket deleted mid-run).
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

	"apothecary/internal/flow"
	"apothecary/internal/flow/actionspec"
	"apothecary/internal/platform/logs"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
)

// actionSkip records a completion that did no work — the stage had nothing
// to act on — and follows on_success. The sibling of actionFail (and like
// it, returns routed=true).
func (x *exec) actionSkip(st *flow.Stage, note string) (bool, error) {
	x.e.log.InfoContext(x.bg, "stage skipped", logs.RunID, x.rs.RunID, logs.StageID, st.ID, "note", note)
	if err := x.append(&event.StageCompleted{StageID: st.ID, Skipped: true, Note: note}); err != nil {
		return true, err
	}
	x.rs.RouteSuccess(st, "")
	return true, nil
}

// trackerTarget is a resolved tracker action target: the writer, the
// ticket's key on it, the context its calls carry (the stage as actor), and
// whether the tracker is a remote one — whose write the ticket's mirror only
// shows after a refresh (written).
type trackerTarget struct {
	w      trackerport.TicketWriter
	key    string
	ctx    context.Context
	remote bool
}

// written settles a successful tracker action: on a remote tracker the
// ticket's mirror is refreshed so it shows the write now, not at the next
// poll (best-effort: a failed refresh never fails the stage). The internal
// tracker wrote the row itself. The stage completes and follows on_success.
func (x *exec) written(tg trackerTarget) (bool, error) {
	if tg.remote {
		x.e.refreshTicket(x.ctx, x.rs.View.TicketID)
	}
	return false, nil
}

// tracker resolves the run's ticket's tracker. routed=true means the stage
// is already settled — skipped (no ticket, or one deleted) or failed (the
// tracker cannot be reached or written) — and the executor returns (true,
// err).
func (x *exec) tracker(st *flow.Stage) (tg trackerTarget, routed bool, err error) {
	rs := x.rs
	if rs.View.TicketID == "" {
		routed, err = x.actionSkip(st, "the run has no ticket; nothing to write to")
		return tg, routed, err
	}
	if x.res.TicketTracker == nil {
		routed, err = x.actionFail(st, event.ReasonInfra, st.Action+": this build cannot write to a ticket tracker")
		return tg, routed, err
	}
	w, key, werr := x.res.TicketTracker(x.ctx, rs.View.TicketID)
	if werr != nil {
		var na *trackerport.ErrNotApplicable
		if errors.As(werr, &na) {
			routed, err = x.actionSkip(st, st.Action+": "+werr.Error())
			return tg, routed, err
		}
		routed, err = x.actionFail(st, event.ReasonInfra, fmt.Sprintf("%s: ticket %s's tracker: %v", st.Action, rs.View.TicketID, werr))
		return tg, routed, err
	}
	ctx := resolution.WithTrackerActor(x.ctx, resolution.TrackerActor{RunID: rs.RunID, Stage: x.stageOccurrence(st)})
	// The internal tracker keys a run's comments by the actor (and is the
	// only one that does); every other tracker is a remote issue.
	_, internal := w.(runKeyedCommenter)
	return trackerTarget{w: w, key: key, ctx: ctx, remote: !internal}, false, nil
}

// stageOccurrence names this run of the stage: the phase run and the stage
// (the trailer's "<phase>-<n>/<stage>" without the run id), never the
// attempt — a retried attempt must find what it already wrote.
func (x *exec) stageOccurrence(st *flow.Stage) string {
	return strings.TrimPrefix(x.roundID(), x.rs.RunID+"/") + "/" + st.ID
}

// trackerFail routes a tracker error: a step no status holds, or a write
// with no meaning on this tracker, skips the stage (the flow fits, this
// tracker cannot say it); a typed refusal (status unreachable, field unknown
// or unwritable) is the flow's literal not fitting the tracker —
// input_invalid; anything else is the tracker itself — infra, still routable.
// A cancelled run is none of these.
func (x *exec) trackerFail(st *flow.Stage, err error) (bool, error) {
	if x.ctx.Err() != nil {
		return true, context.Cause(x.ctx)
	}
	var noStep *trackerport.ErrStatusUnsupported
	var na *trackerport.ErrNotApplicable
	if errors.As(err, &noStep) || errors.As(err, &na) {
		return x.actionSkip(st, st.Action+": "+err.Error())
	}
	reason := event.ReasonInfra
	var unreachable *trackerport.ErrStatusUnreachable
	var unknown *trackerport.ErrFieldUnknown
	var unsupported *trackerport.ErrFieldUnsupported
	if errors.As(err, &unreachable) || errors.As(err, &unknown) || errors.As(err, &unsupported) {
		reason = event.ReasonInputInvalid
	}
	return x.actionFail(st, reason, st.Action+": "+err.Error())
}

// actionTicketTransition moves the ticket — to the status holding a flow
// step (`to`) or to a literal status (`status`); the parser holds the stage
// to exactly one. Idempotent by nature: the tracker reads the current status
// first, and "already there" is a no-op.
func (x *exec) actionTicketTransition(st *flow.Stage) (bool, error) {
	tg, routed, err := x.tracker(st)
	if routed {
		return true, err
	}
	status := strings.TrimSpace(st.Setting("status", ""))
	if to := strings.TrimSpace(st.Setting("to", "")); to != "" {
		if x.res.TicketStep == nil {
			return x.actionFail(st, event.ReasonInfra, st.Action+": this build cannot resolve a flow step to a status")
		}
		if status, err = x.res.TicketStep(tg.ctx, x.rs.View.TicketID, trackerport.FlowStep(to)); err != nil {
			return x.trackerFail(st, err)
		}
	}
	if err := tg.w.Transition(tg.ctx, tg.key, status); err != nil {
		return x.trackerFail(st, err)
	}
	x.e.log.InfoContext(x.bg, "ticket moved", logs.RunID, x.rs.RunID, logs.StageID, st.ID, "ticket", tg.key, "to", status)
	return x.written(tg)
}

// ticketTrailer is the line a comment posted to a REMOTE tracker ends with:
// who posted it and for what. It is also the comment's idempotency key —
// looked for on a re-entry — so it names the run, the phase run and the
// stage, the way review replies are marked, and NEVER the attempt: a
// failed-then-retried attempt must recognise the comment it already posted.
// Visible text rather than an HTML comment, because Jira renders comments as
// ADF and would strip one.
func (x *exec) ticketTrailer(st *flow.Stage) string {
	return fmt.Sprintf("— apothecary · run %s · stage %s", x.roundID(), st.ID)
}

// runKeyedCommenter is a tracker that keys a run's comment by the actor on
// the context (resolution.TrackerActor: the run and the stage occurrence) —
// the internal tracker. Its comments carry no trailer: posting one again is
// a no-op there, and RunCommentPosted is the reconciliation read.
type runKeyedCommenter interface {
	RunCommentPosted(ctx context.Context, key string) (bool, error)
}

// commentPosted is the re-entry's question: is this stage occurrence's
// comment already on the ticket? By key on a tracker that keys them, else by
// the trailer among the comments.
func (x *exec) commentPosted(tg trackerTarget, trailer string) (bool, error) {
	if kc, ok := tg.w.(runKeyedCommenter); ok {
		return kc.RunCommentPosted(tg.ctx, tg.key)
	}
	existing, err := tg.w.ListComments(tg.ctx, tg.key)
	if err != nil {
		return false, err
	}
	for _, c := range existing {
		if strings.Contains(c.Body, trailer) {
			return true, nil
		}
	}
	return false, nil
}

// actionTicketComment posts the body artifact as a comment. The effect
// lands outside the run, so an intent (action_started) is recorded first and
// a re-entry with the intent still pending asks the tracker whether the
// comment is already there before posting again (PLAN5 M20, as create_mr).
// The files of its attachments port go up to the tracker just before the
// comment (trackerport.TicketAttacher), and the body links them (attach.go).
func (x *exec) actionTicketComment(st *flow.Stage) (bool, error) {
	rs := x.rs
	tg, routed, err := x.tracker(st)
	if routed {
		return true, err
	}
	trailer := x.ticketTrailer(st)
	if rs.ActionPending[st.ID] {
		// Reconcile before reading anything: the comment may already be
		// there, and then neither the body nor the workspace matters.
		posted, cerr := x.commentPosted(tg, trailer)
		if cerr != nil {
			return x.trackerFail(st, fmt.Errorf("cannot tell whether the comment was already posted: %w", cerr))
		}
		if posted {
			x.e.log.InfoContext(x.bg, "adopted a comment posted before an interrupted record",
				logs.RunID, rs.RunID, logs.StageID, st.ID, "ticket", tg.key)
			return x.written(tg)
		}
	}
	path := inputPath(st, "body")
	data, rerr := x.res.Envp.ReadFile(x.ctx, rs.EnvID, path)
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
	var attacher trackerport.TicketAttacher
	if len(files) > 0 {
		var ok bool
		if attacher, ok = tg.w.(trackerport.TicketAttacher); !ok {
			return x.actionFail(st, event.ReasonInfra, fmt.Sprintf("%s: ticket %s's tracker cannot host attachments", st.Action, tg.key))
		}
	}
	if !rs.ActionPending[st.ID] {
		// Record the intent BEFORE the effect: a crash past this point makes
		// the next boot reconcile instead of posting a second comment.
		if err := x.append(&event.ActionStarted{StageID: st.ID, Action: st.Action}); err != nil {
			return true, err
		}
	}
	if len(files) > 0 {
		body, err = x.attach(body, files, func(name, ct string, data []byte) (repoport.Attachment, error) {
			return attacher.Attach(tg.ctx, tg.key, name, ct, data)
		})
		if err != nil {
			return x.trackerFail(st, err)
		}
		body = strings.TrimSpace(body)
	}
	if _, keyed := tg.w.(runKeyedCommenter); !keyed {
		body += "\n\n" + trailer
	}
	if err := tg.w.Comment(tg.ctx, tg.key, body); err != nil {
		return x.trackerFail(st, err)
	}
	return x.written(tg)
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
		data, rerr := x.res.Envp.ReadFile(x.ctx, rs.EnvID, path)
		if rerr != nil {
			return x.actionFail(st, event.ReasonInputMissing, fmt.Sprintf("field value %s not readable: %v", path, rerr))
		}
		value = strings.TrimSpace(string(data))
	} else {
		value = strings.TrimSpace(st.Setting("text", ""))
	}
	if st.Setting("mode", actionspec.ModeReplace) == actionspec.ModeAppend {
		cur, ferr := tg.w.Fields(tg.ctx, tg.key, []string{field})
		if ferr != nil {
			return x.trackerFail(st, ferr)
		}
		value = mergeItems(cur[field], value)
	}
	if err := tg.w.SetFields(tg.ctx, tg.key, map[string]string{field: value}); err != nil {
		return x.trackerFail(st, err)
	}
	x.e.log.InfoContext(x.bg, "ticket field set", logs.RunID, rs.RunID, logs.StageID, st.ID, "ticket", tg.key, "field", field)
	return x.written(tg)
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
	mrs := rs.MRs
	if co := x.rs.SubjectCheckout(); co != nil {
		mrs = nil
		if mr := rs.MRFor(co.Name, co.Branch); mr != nil {
			mrs = []runcore.MRRecord{*mr}
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
		if err := tg.w.AddLink(tg.ctx, tg.key, trackerport.TicketLink{Title: title, URL: mr.Ref.URL}); err != nil {
			return x.trackerFail(st, err)
		}
	}
	x.e.log.InfoContext(x.bg, "ticket linked to merge request(s)", logs.RunID, rs.RunID, logs.StageID, st.ID, "ticket", tg.key, "count", len(mrs))
	return x.written(tg)
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
	checkouts := rs.Checkouts
	if co := x.rs.SubjectCheckout(); co != nil {
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
		if err := tg.w.AddLink(tg.ctx, tg.key, trackerport.TicketLink{Title: title, URL: url}); err != nil {
			return x.trackerFail(st, err)
		}
		linked++
	}
	if linked == 0 {
		return x.actionSkip(st, "no checkout of this run has a repository host to link a branch from")
	}
	x.e.log.InfoContext(x.bg, "ticket linked to branch(es)", logs.RunID, rs.RunID, logs.StageID, st.ID, "ticket", tg.key, "count", linked)
	return x.written(tg)
}
