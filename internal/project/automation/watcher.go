// Package automation watches a project's automations (confstore.Automation)
// and begins runs from them: a tracker query polled on the workshop's ticket
// source, each issue started once; a schedule firing a run of one ticket per
// slot. It lives outside the engine on purpose — an automation is a rule
// that starts a run of a flow the way a person would, and the run neither
// knows nor cares (its first phase_started says `by: automation`, nothing
// else).
// One pass (RunOnce) walks every enabled automation; the daemon runs a pass
// every couple of minutes.
package automation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"apothecary/internal/platform/logs"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/schedule"
	"apothecary/internal/project/tickets"
	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workitem"
	"apothecary/internal/workitem/mergerequest"
	"apothecary/internal/workitem/ticket"
)

// By is who an automation's runs say began them.
const By = "automation"

// Runs is the slice of the engine a pass needs: create a run, and tell
// whether a work item — a ticket, a merge request — has one.
type Runs interface {
	CreateRunWith(ctx context.Context, t runinput.Ticket, flowName, projectID string, in runinput.Input) (string, error)
	RunsOf(ctx context.Context, item workitem.Ref) ([]run.View, error)
}

// MRs is what a merge_request automation reads: a repository's open merge
// requests, and who the repository host integration is.
type MRs interface {
	ListMRs(ctx context.Context, projectID, repo string) ([]repoport.MRInfo, error)
	RepoHostUser(ctx context.Context, projectID, repo string) (string, error)
}

type Watcher struct {
	Runs    Runs
	MRs     MRs
	Tickets *tickets.Factory
	Conf    *confstore.Service
	// Notify, when set, is told once per streak of refused firings: a run
	// that cannot be created is a blocker a person should see, and the
	// ticket is kept for them.
	Notify notifyport.NotificationChannel
	Clock  func() time.Time
	Log    *slog.Logger
	// Limit is how many runs one tracker poll starts (the rest wait for the
	// next pass) and the page size it reads with; 50 by default.
	Limit int
}

func (w *Watcher) now() time.Time {
	if w.Clock != nil {
		return w.Clock()
	}
	return time.Now()
}

func (w *Watcher) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}

func (w *Watcher) limit() int {
	if w.Limit > 0 {
		return w.Limit
	}
	return 50
}

// RunOnce walks every enabled automation once. An automation's own failure
// (a tracker down, a refused run) is recorded on it and never fails the
// pass; only listing them does.
func (w *Watcher) RunOnce(ctx context.Context) error {
	list, err := w.Conf.EnabledAutomations(ctx)
	if err != nil {
		return err
	}
	for _, a := range list {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.pass(ctx, a)
	}
	return nil
}

func (w *Watcher) pass(ctx context.Context, a confstore.Automation) {
	var fired bool
	var err error
	switch a.Kind {
	case confstore.AutomationTrackerQuery:
		fired, err = w.pollTracker(ctx, a)
	case confstore.AutomationCron:
		fired, err = w.tick(ctx, a)
	case confstore.AutomationMergeRequest:
		fired, err = w.pollMRs(ctx, a)
	default:
		err = fmt.Errorf("unknown automation kind %q", a.Kind)
	}
	now := w.now()
	m := confstore.AutomationMark{PolledAt: now}
	if fired {
		m.FiredAt = now
	}
	if err != nil {
		m.Err = err.Error()
		w.log().Warn("automation pass failed", "automation", a.ID, "name", a.Name, "err", err)
		// Once per streak: the first refusal is news, the tenth is noise.
		if a.LastError == "" {
			w.blocker(ctx, a, err)
		}
	} else if a.LastError != "" {
		w.log().Info("automation recovered", "automation", a.ID, "name", a.Name)
	}
	if merr := w.Conf.MarkAutomation(ctx, a.ID, m); merr != nil {
		w.log().Error("automation mark failed", "automation", a.ID, "err", merr)
	}
}

// pollTracker lists the open issues of the workshop's ticket source matching
// the automation's query and starts one run per issue it claims: a remote
// issue seen for the first time is recorded as this automation's and fired.
// A record of this automation's that never got its run (a crash in between,
// a refused run) is fired again; one with runs, or one a person or another
// automation made, is left alone. On the internal tracker the tickets are
// the rows themselves, so only this automation's own are ever claimed.
//
// It reads page after page until the source has no more: issues already
// handled stay in the newest-first list, so a match past the first page
// would otherwise wait forever. The limit caps the runs a pass starts, not
// the issues it looks at; what is left fires on the next pass.
func (w *Watcher) pollTracker(ctx context.Context, a confstore.Automation) (bool, error) {
	p, err := w.Conf.GetProject(ctx, a.ProjectID)
	if err != nil {
		return false, fmt.Errorf("workshop: %w", err)
	}
	q := trackerport.SearchQuery{Text: a.Query, Done: trackerport.DoneOpen, Limit: w.limit()}
	var errs []string
	fired := false
	claimed := 0
	read := map[string]bool{}
	for claimed < w.limit() {
		page, err := w.Tickets.Live.List(ctx, p.TicketSourceID, q, false)
		if err != nil {
			errs = append(errs, fmt.Sprintf("search: %v", err))
			break
		}
		for _, row := range page.Rows {
			if claimed == w.limit() || ctx.Err() != nil {
				break
			}
			key := row.Issue.Key
			t, ok, err := w.claim(ctx, a, p.TicketSourceID, row)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", key, err))
				continue
			}
			if !ok {
				continue
			}
			claimed++
			if _, err := w.start(ctx, a, t); err != nil {
				if run, busy := activeRun(err); busy {
					err = fmt.Errorf("ticket busy: run %s", run)
				}
				errs = append(errs, fmt.Sprintf("%s: %v", key, err))
				continue
			}
			fired = true
		}
		// A tracker handing back a cursor it already gave would loop.
		if page.Next == "" || read[page.Next] || ctx.Err() != nil {
			break
		}
		read[page.Next] = true
		q.Cursor = page.Next
	}
	if len(errs) > 0 {
		return fired, errors.New(strings.Join(errs, "; "))
	}
	return fired, nil
}

// claim is the ticket of a listed issue and whether the automation fires on
// it. A remote issue is recorded (its last-seen columns rewritten) on every
// pass; the record keeps the automation that first made it.
func (w *Watcher) claim(ctx context.Context, a confstore.Automation, sourceID string, row tickets.LiveRow) (confstore.Ticket, bool, error) {
	var t confstore.Ticket
	if row.Record != nil && row.Record.Internal() {
		t = *row.Record
	} else {
		rec, created, err := w.Tickets.Record(ctx, sourceID, row.Issue, a.ID)
		if err != nil {
			return confstore.Ticket{}, false, err
		}
		if created {
			w.log().Info("automation recorded a ticket", "automation", a.ID, "name", a.Name, "ticket", rec.ID, "key", row.Issue.Key)
			return rec, true, nil
		}
		t = rec
	}
	if t.AutomationID != a.ID {
		return confstore.Ticket{}, false, nil
	}
	runs, err := w.Runs.RunsOf(ctx, ticket.Ref(t.ID))
	if err != nil {
		return confstore.Ticket{}, false, err
	}
	return t, len(runs) == 0, nil
}

// activeRun recognises the engine's refusal of a run on a ticket that has one
// going without importing the engine (the layering keeps automation below
// it), and names that run.
func activeRun(err error) (string, bool) {
	var busy interface{ ActiveRunID() string }
	if errors.As(err, &busy) {
		return busy.ActiveRunID(), true
	}
	return "", false
}

// pollMRs lists the repository's open merge requests and creates a run of
// the automation's flow on each one someone else opened that no run has
// been through yet — whoever started it — given the merge request's title
// and description as its work; no ticket is made. Drafts wait until they
// are marked ready; the integration's own merge requests are never picked.
func (w *Watcher) pollMRs(ctx context.Context, a confstore.Automation) (bool, error) {
	mrs, err := w.MRs.ListMRs(ctx, a.ProjectID, a.Repo)
	if err != nil {
		return false, fmt.Errorf("list merge requests: %w", err)
	}
	me, err := w.MRs.RepoHostUser(ctx, a.ProjectID, a.Repo)
	if err != nil {
		return false, fmt.Errorf("repository account: %w", err)
	}
	var errs []string
	fired := false
	for i, mr := range mrs {
		if i >= w.limit() {
			break
		}
		if mr.State != repoport.MROpen || mr.Draft || strings.EqualFold(mr.Author, me) {
			continue
		}
		runs, err := w.Runs.RunsOf(ctx, mergerequest.Ref(a.ProjectID, a.Repo, mr.Ref.IID))
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", mr.Ref.URL, err))
			continue
		}
		if len(runs) > 0 {
			continue
		}
		if _, err := w.startOn(ctx, a, mr); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", mr.Ref.URL, err))
			continue
		}
		fired = true
	}
	if len(errs) > 0 {
		return fired, errors.New(strings.Join(errs, "; "))
	}
	return fired, nil
}

// startOn creates the automation's run on a merge request.
func (w *Watcher) startOn(ctx context.Context, a confstore.Automation, mr repoport.MRInfo) (string, error) {
	id, err := w.Runs.CreateRunWith(ctx, mergerequest.Work(mr), a.Flow, a.ProjectID, runinput.Input{Params: maps.Clone(a.Params), By: By,
		MR: &runinput.MR{Repo: a.Repo, IID: mr.Ref.IID}})
	if err != nil {
		return "", err
	}
	w.log().Info("automation started a run on a merge request", "automation", a.ID, "name", a.Name,
		logs.RunID, id, "mr", mr.Ref.URL)
	return id, nil
}

// tick fires a cron automation once its next slot after the last firing has
// passed — at most once per slot, with no catch-up for slots missed while
// the daemon was down, and the firing recorded BEFORE the run is created so
// a crash in between cannot fire it twice. A rule edited after its last
// firing counts from the edit.
//
// A ticket has one active run at a time (workitem.ErrBusy): a slot that
// falls while the ticket's previous run is still going is skipped, recorded
// as "ticket busy: run <id>", and not made up later — the same policy as a
// slot missed while the daemon was down.
func (w *Watcher) tick(ctx context.Context, a confstore.Automation) (bool, error) {
	c, err := schedule.Parse(a.Schedule)
	if err != nil {
		return false, err
	}
	from := a.CreatedAt
	if a.LastFiredAt != nil && a.LastFiredAt.After(from) {
		from = *a.LastFiredAt
	}
	if a.UpdatedAt.After(from) {
		from = a.UpdatedAt
	}
	next := c.Next(from)
	now := w.now()
	if next.IsZero() || now.Before(next) {
		return false, nil
	}
	if err := w.Conf.MarkAutomation(ctx, a.ID, confstore.AutomationMark{FiredAt: now, Err: a.LastError}); err != nil {
		return false, err
	}
	t, err := w.Conf.GetTicket(ctx, a.TicketID)
	if err != nil {
		return true, fmt.Errorf("ticket %s: %w", a.TicketID, err)
	}
	if err := w.pinnedInWorkshop(ctx, a, t); err != nil {
		return true, err
	}
	_, err = w.start(ctx, a, t)
	if run, busy := activeRun(err); busy {
		w.log().Info("automation slot skipped: the ticket's run is still going", "automation", a.ID,
			"name", a.Name, "ticket", t.ID, logs.RunID, run)
		return true, fmt.Errorf("ticket busy: run %s", run)
	}
	return true, err
}

// pinnedInWorkshop refuses a pinned ticket the workshop no longer shows: one
// of another owner, or from a source other than the workshop's ticket
// source. The pin was checked when the automation was saved, but the
// workshop's source may have changed since; the slot is spent, not made up.
func (w *Watcher) pinnedInWorkshop(ctx context.Context, a confstore.Automation, t confstore.Ticket) error {
	p, err := w.Conf.GetProject(ctx, a.ProjectID)
	if err != nil {
		return fmt.Errorf("workshop: %w", err)
	}
	if t.UserID != p.UserID {
		return fmt.Errorf("ticket %s is not the workshop owner's", t.ID)
	}
	if t.SourceID != p.TicketSourceID {
		return fmt.Errorf("ticket %s is from ticket source %s, not the workshop's (%s): pin a ticket of the workshop's source", t.ID, t.SourceID, p.TicketSourceID)
	}
	return nil
}

func (w *Watcher) start(ctx context.Context, a confstore.Automation, t confstore.Ticket) (string, error) {
	var attIDs []string
	if w.Tickets != nil && w.Tickets.Attachments != nil {
		if atts, err := w.Tickets.Attachments.ForTicket(ctx, t.ID); err == nil {
			for _, x := range atts {
				attIDs = append(attIDs, x.ID)
			}
		}
	}
	id, err := w.Runs.CreateRunWith(ctx, runinput.Ticket{
		TicketID: t.ID, Title: t.Title, Goal: t.Goal, Branch: t.BaseBranch,
		ChangeType:          string(t.ChangeType),
		TicketAttachmentIDs: attIDs,
		SourceID:            t.SourceID, SourceKind: t.SourceKind, SourceKey: t.SourceKey, SourceURL: t.SourceURL,
	}, a.Flow, a.ProjectID, runinput.Input{Params: maps.Clone(a.Params), By: By})
	if err != nil {
		return "", err
	}
	w.log().Info("automation started a run", "automation", a.ID, "name", a.Name,
		"ticket", t.ID, logs.RunID, id, "flow", a.Flow)
	return id, nil
}

func (w *Watcher) blocker(ctx context.Context, a confstore.Automation, err error) {
	if w.Notify == nil {
		return
	}
	n := notifyport.Notification{
		Type:  "blocker",
		Title: fmt.Sprintf("Automation %q could not start a run", a.Name),
		Body:  err.Error(),
	}
	// To the automation's owner's channels: the project's.
	if p, perr := w.Conf.GetProject(ctx, a.ProjectID); perr == nil {
		n.UserID = p.UserID
	}
	if serr := w.Notify.Send(ctx, n); serr != nil {
		w.log().Warn("automation blocker notification failed", "automation", a.ID, "err", serr)
	}
}
