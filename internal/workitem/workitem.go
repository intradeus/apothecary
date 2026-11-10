// Package workitem is what runs are started on: the work items — a ticket,
// a merge request — and what every kind of them shares, so that what starts,
// guards and lists runs handles any kind the same way.
//
// A kind is a value of a flow's operates_on (FLOWLANG §1). Its package
// (workitem/ticket, workitem/mergerequest) says how a start names one of its
// items, which runs hold one, and how a new run is pointed at it; the kinds
// are registered once (workitem/items). Nothing that starts, guards or lists
// runs names a kind: a new kind is its package, one registration, and its
// operates_on value in the language.
//
// Everything a kind decides is pure over what it is handed — a run's view,
// a start's inputs, the project's resolution. Reading an item from where it
// lives (the ticket store, the repository host) is its source's job, outside
// the engine.
package workitem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"

	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

// MaxBranchSlug bounds a branch derived from a title. It is NOT a
// readability cap — a branch name may be as long as the title needs — only a
// filesystem backstop: a loose ref is a file, so a path component over 255
// bytes is unwritable on ext4/APFS. The container name has its own, much
// tighter budget and is truncated separately (wsname.For).
const MaxBranchSlug = 200

// Ref names one work item: its kind, the project it belongs to, and its key
// within that kind (a ticket's id; a merge request's repo!iid).
type Ref struct {
	Kind    string `json:"kind"`
	Project string `json:"project"`
	Key     string `json:"key"`
}

func (r Ref) String() string { return r.Kind + ":" + r.Project + "/" + r.Key }

// Kind is one kind of work item, as everything that starts runs needs it.
type Kind interface {
	// Name is the kind's operates_on value.
	Name() string
	// Describe names an item for a person: "ticket 01ABC", "merge request !12".
	Describe(Ref) string
	// Noun is the kind for a person, with no item: "ticket", "merge request".
	Noun() string
	// Required: a run of a flow of this kind names one of its items; a
	// kind that is not may start a run on a bare title.
	Required() bool
	// Accompanies: one of its items may come with a run of a flow of
	// another kind — a ticket's work behind a run on a merge request.
	Accompanies() bool
	// Requested is the item of this kind a start names, if any.
	Requested(projectID string, t runinput.Ticket, in runinput.Input) (Ref, bool)
	// Held are the items of this kind a run holds, read from its view: the
	// one it was started on, and any it made its own on the way.
	Held(v run.View) []Ref
	// Prepare points the new run at the item: its branch, its checkouts.
	// Kinds prepare in registration order.
	Prepare(ctx context.Context, s *Start, item Ref) error
}

// Start is the new run as the items it is started on shape it, before its
// run_created is written.
type Start struct {
	RunID string
	Title string
	Res   *resolution.Resolution
	// Branch is the run's own branch, Base the stem it derives from;
	// SourceBranch is what it forks off.
	Branch, Base, SourceBranch string
	// Repoints put checkouts on an item's own branch instead of the run's.
	Repoints []Repoint
	// Adopted is the merge request someone opened that the run is started
	// on (run_created's mr).
	Adopted *event.AdoptedMR
}

// Repoint puts the checkout Repo on a branch of an item's own — a merge
// request's head — fetched from Fetch, merging into Source.
type Repoint struct {
	Repo, Branch, Source, Fetch string
	Fork                        bool
}

// Registry is the kinds, in the order they prepare a run.
type Registry struct {
	kinds []Kind
	by    map[string]Kind
	locks sync.Map
}

// NewRegistry registers kinds once: a name used twice is refused.
func NewRegistry(kinds ...Kind) (*Registry, error) {
	r := &Registry{by: map[string]Kind{}}
	for _, k := range kinds {
		if _, dup := r.by[k.Name()]; dup {
			return nil, fmt.Errorf("work item kind %q registered twice", k.Name())
		}
		r.by[k.Name()] = k
		r.kinds = append(r.kinds, k)
	}
	return r, nil
}

// Kinds are the registered kinds, in registration order.
func (r *Registry) Kinds() []Kind { return slices.Clone(r.kinds) }

// Get is the kind named name.
func (r *Registry) Get(name string) (Kind, bool) {
	k, ok := r.by[name]
	return k, ok
}

// Requested are the items a start names, in registration order.
func (r *Registry) Requested(projectID string, t runinput.Ticket, in runinput.Input) []Ref {
	var out []Ref
	for _, k := range r.kinds {
		if ref, ok := k.Requested(projectID, t, in); ok {
			out = append(out, ref)
		}
	}
	return out
}

// Held are every item a run holds, of every kind.
func (r *Registry) Held(v run.View) []Ref {
	var out []Ref
	for _, k := range r.kinds {
		out = append(out, k.Held(v)...)
	}
	return out
}

// Holds reports whether the run holds the item.
func (r *Registry) Holds(v run.View, item Ref) bool {
	k, ok := r.by[item.Kind]
	return ok && slices.Contains(k.Held(v), item)
}

// Describe names an item for a person, whatever its kind.
func (r *Registry) Describe(item Ref) string {
	if k, ok := r.by[item.Kind]; ok {
		return k.Describe(item)
	}
	return item.String()
}

// StartsOn checks a start names what a run of a flow operating on kind
// needs: one of its items when the kind requires it, and nothing of a kind
// that cannot come along.
func (r *Registry) StartsOn(flowName, kind string, items []Ref) error {
	k, ok := r.by[kind]
	if !ok {
		return fmt.Errorf("flow %q operates on %q, which is no kind of work item", flowName, kind)
	}
	named := slices.ContainsFunc(items, func(i Ref) bool { return i.Kind == kind })
	if k.Required() && !named {
		return fmt.Errorf("flow %q starts its runs on a %s (operates_on: %s): name the %s", flowName, k.Noun(), kind, k.Noun())
	}
	for _, i := range items {
		if i.Kind == kind {
			continue
		}
		if other, ok := r.by[i.Kind]; !ok || !other.Accompanies() {
			return fmt.Errorf("flow %q starts its runs on a %s (operates_on: %s), not on a %s", flowName, k.Noun(), kind, r.noun(i.Kind))
		}
	}
	return nil
}

func (r *Registry) noun(kind string) string {
	if k, ok := r.by[kind]; ok {
		return k.Noun()
	}
	return kind
}

// Prepare has every kind with an item in items point the start at it.
func (r *Registry) Prepare(ctx context.Context, s *Start, items []Ref) error {
	for _, k := range r.kinds {
		for _, i := range items {
			if i.Kind != k.Name() {
				continue
			}
			if err := k.Prepare(ctx, s, i); err != nil {
				return err
			}
		}
	}
	return nil
}

// Lock serializes run creation on the items, held from the busy check until
// run_created is appended, so two starts on one item cannot both see it
// idle. A lock and not a unique index on purpose: the runs table is a
// projection rebuilt from the event log, and older history may hold two
// live runs of one item. Items lock in one order, so two starts naming the
// same two cannot deadlock.
func (r *Registry) Lock(items []Ref) (unlock func()) {
	sorted := slices.Clone(items)
	slices.SortFunc(sorted, func(a, b Ref) int { return cmp.Compare(a.String(), b.String()) })
	sorted = slices.Compact(sorted)
	var held []*sync.Mutex
	for _, i := range sorted {
		m, _ := r.locks.LoadOrStore(i.String(), &sync.Mutex{})
		mu := m.(*sync.Mutex)
		mu.Lock()
		held = append(held, mu)
	}
	return func() {
		for _, mu := range slices.Backward(held) {
			mu.Unlock()
		}
	}
}

// Idle refuses with ErrBusy when a non-terminal run among runs holds one of
// the items: an item has at most one run going. Two runs on one item undo
// each other — they push to one branch, or write conflicting meanings to
// one tracker issue — so the second waits for the first to end, or for a
// person to abort it. The caller holds Lock.
func (r *Registry) Idle(runs []run.View, items []Ref) error {
	for _, i := range items {
		for _, v := range runs {
			if !v.State.Terminal() && r.Holds(v, i) {
				return ErrBusy{Item: i, What: r.Describe(i), RunID: v.RunID}
			}
		}
	}
	return nil
}

// ErrBusy refuses a run on an item that already has one going. It is a
// value the caller takes apart with errors.As: the HTTP API answers 409
// naming RunID, and an automation records the skipped firing.
type ErrBusy struct {
	Item  Ref
	What  string // the item for a person ("ticket 01ABC")
	RunID string // the item's active run
}

func (e ErrBusy) Error() string {
	return fmt.Sprintf("%s is busy: run %s is still active; abort it or wait for it to end", e.What, e.RunID)
}

// ActiveRunID names the run holding the item. A caller that may not import
// this package recognises the refusal by it: errors.As against
// interface{ ActiveRunID() string }.
func (e ErrBusy) ActiveRunID() string { return e.RunID }
