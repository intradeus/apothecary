package run

import (
	"context"
	"fmt"
	"slices"

	"apothecary/internal/run/event"
)

// Reader reads run views from the event store: one run by reducing its log,
// every run through the store's projection when it keeps one. It is the read
// side every service shares, so none of them needs the engine to look a run
// up.
type Reader struct {
	Store event.Store
}

// View is one run's view, reduced from its log; a run with no events is an
// error ("run <id> not found").
func (r *Reader) View(ctx context.Context, runID string) (View, error) {
	evs, err := r.Store.Events(ctx, runID, 0)
	if err != nil {
		return View{}, err
	}
	if len(evs) == 0 {
		return View{}, fmt.Errorf("run %s not found", runID)
	}
	return Reduce(evs), nil
}

// All is every run's read-model row: one query when the store keeps the
// projection (ViewSource), else each run's log reduced — the in-memory
// store's path, and the reference the projection is checked against.
func (r *Reader) All(ctx context.Context) ([]View, error) {
	if vs, ok := r.Store.(ViewSource); ok {
		return vs.RunViews(ctx)
	}
	ids, err := r.Store.RunIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(ids))
	for _, id := range ids {
		evs, err := r.Store.Events(ctx, id, 0)
		if err != nil {
			return nil, err
		}
		if len(evs) == 0 {
			continue
		}
		out = append(out, Reduce(evs))
	}
	return out, nil
}

// ListFilter scopes a run list: always to one owner, optionally to a
// project and a ticket.
type ListFilter struct{ UserID, ProjectID, TicketID string }

// ViewLister is an OPTIONAL event.Store capability: one user's runs, scoped
// in the query, newest first — so a run list never scans everyone's runs.
type ViewLister interface {
	ListRunViews(ctx context.Context, f ListFilter) ([]View, error)
}

// List is the runs f selects, newest first: in the store's query when it
// can (ViewLister), else every view filtered here.
func (r *Reader) List(ctx context.Context, f ListFilter) ([]View, error) {
	if l, ok := r.Store.(ViewLister); ok {
		return l.ListRunViews(ctx, f)
	}
	all, err := r.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(all))
	for _, v := range all {
		if v.UserID != f.UserID || (f.ProjectID != "" && v.ProjectID != f.ProjectID) || (f.TicketID != "" && v.TicketID != f.TicketID) {
			continue
		}
		out = append(out, v)
	}
	slices.SortStableFunc(out, func(a, b View) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}
