// Package automation watches a project's automations (confstore.Automation)
// and begins runs from them: a tracker query polled into tickets, each
// started once; a schedule firing a run of one ticket per slot. It lives
// outside the engine on purpose — an automation is a rule that starts a run
// of a flow the way a person would, and the run neither knows nor cares (its
// first phase_started says `by: automation`, nothing else).
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
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/schedule"
	"apothecary/internal/project/tickets"
	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
)

// By is who an automation's runs say began them.
const By = "automation"

// Runs is the slice of the engine a pass needs: create a run, and tell
// whether a ticket — or a merge request — has one.
type Runs interface {
	CreateRunWith(ctx context.Context, t runinput.Ticket, flowName, projectID string, in runinput.Input) (string, error)
	RunsOfTicket(ctx context.Context, ticketID string) ([]run.View, error)
	RunsOnMR(ctx context.Context, projectID, repo string, iid int) ([]run.View, error)
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
	// Limit is how many issues one tracker poll takes; 50 by default.
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

// pollTracker asks the tracker, imports every issue it finds that is not a
// ticket yet, and starts one run per import. An issue imported by an earlier
// pass that never got its run (a crash in between) is adopted; one a person
// imported is theirs and left alone.
func (w *Watcher) pollTracker(ctx context.Context, a confstore.Automation) (bool, error) {
	found, err := w.Tickets.Search(ctx, a.SourceID, a.Query, w.limit())
	if err != nil {
		return false, fmt.Errorf("search: %w", err)
	}
	var errs []string
	fired := false
	for _, remote := range found {
		t, ok, err := w.importOrAdopt(ctx, a, remote.Key)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", remote.Key, err))
			continue
		}
		if !ok {
			continue
		}
		if _, err := w.start(ctx, a, t); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", remote.Key, err))
			continue
		}
		fired = true
	}
	if len(errs) > 0 {
		return fired, errors.New(strings.Join(errs, "; "))
	}
	return fired, nil
}

func (w *Watcher) importOrAdopt(ctx context.Context, a confstore.Automation, key string) (confstore.Ticket, bool, error) {
	t, _, err := w.Tickets.CreateFromSource(ctx, a.SourceID, key,
		confstore.Ticket{ProjectID: a.ProjectID, AutomationID: a.ID}, nil)
	if err == nil {
		w.log().Info("automation imported a ticket", "automation", a.ID, "name", a.Name, "ticket", t.ID, "key", key)
		return t, true, nil
	}
	var dup *tickets.DuplicateError
	if !errors.As(err, &dup) {
		return confstore.Ticket{}, false, err
	}
	existing := dup.Ticket
	if existing.AutomationID != a.ID || existing.ProjectID != a.ProjectID {
		return confstore.Ticket{}, false, nil
	}
	runs, err := w.Runs.RunsOfTicket(ctx, existing.ID)
	if err != nil {
		return confstore.Ticket{}, false, err
	}
	if len(runs) > 0 {
		return confstore.Ticket{}, false, nil
	}
	return existing, true, nil
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
		runs, err := w.Runs.RunsOnMR(ctx, a.ProjectID, a.Repo, mr.Ref.IID)
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
	id, err := w.Runs.CreateRunWith(ctx, tickets.MRWork(mr), a.Flow, a.ProjectID, runinput.Input{Params: maps.Clone(a.Params), By: By,
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
// A ticket has one active run at a time (engine.ErrTicketBusy): a slot that
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
	_, err = w.start(ctx, a, t)
	// The engine's refusal, recognised without importing the engine (the
	// layering keeps automation below it).
	var busy interface{ ActiveRunID() string }
	if errors.As(err, &busy) {
		w.log().Info("automation slot skipped: the ticket's run is still going", "automation", a.ID,
			"name", a.Name, "ticket", t.ID, logs.RunID, busy.ActiveRunID())
		return true, fmt.Errorf("ticket busy: run %s", busy.ActiveRunID())
	}
	return true, err
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
