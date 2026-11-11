// Package ticket is the ticket kind of work item (operates_on: ticket): a
// unit of work written here or mirrored from a tracker, owning many runs.
// A run names one by its id; each run of it gets a branch of its own, the
// ticket's base plus the run's tail, so attempts never collide.
package ticket

import (
	"context"
	"fmt"

	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workitem"
	"apothecary/internal/workspace/checkout"
	"apothecary/internal/workspace/wsname"
)

// Name is the kind's operates_on value.
const Name = "ticket"

// Kind is the ticket kind.
type Kind struct{}

var _ workitem.Kind = Kind{}

// Ref names ticket id of a project.
func Ref(projectID, id string) workitem.Ref {
	return workitem.Ref{Kind: Name, Project: projectID, Key: id}
}

func (Kind) Name() string                   { return Name }
func (Kind) Noun() string                   { return "ticket" }
func (Kind) Describe(r workitem.Ref) string { return "ticket " + r.Key }

// Required is false: a run may start on a bare title, with no ticket behind
// it (the direct path tests and legacy runs take).
func (Kind) Required() bool { return false }

// Accompanies is true: a ticket's work may be what a run on another item
// does — a ticket's run on the merge request it was opened for.
func (Kind) Accompanies() bool { return true }

func (Kind) Requested(projectID string, t runinput.Ticket, _ runinput.Input) (workitem.Ref, bool) {
	if t.TicketID == "" {
		return workitem.Ref{}, false
	}
	return Ref(projectID, t.TicketID), true
}

func (Kind) Held(v run.View) []workitem.Ref {
	if v.TicketID == "" {
		return nil
	}
	return []workitem.Ref{Ref(v.ProjectID, v.TicketID)}
}

// Prepare gives the run its own branch: the ticket's base — its own, or one
// from its title — plus the run's tail, so each attempt is a unique,
// readable branch and the container name, slugified from the base, stays
// sane.
func (Kind) Prepare(_ context.Context, s *workitem.Start, _ workitem.Ref) error {
	if s.Base == "" {
		s.Base = "apothecary/" + wsname.Slug(s.Title, workitem.MaxBranchSlug)
	}
	s.Branch = s.Base + "-" + wsname.RunIDTail(s.RunID)
	if err := checkout.CheckBranch(s.Branch); err != nil {
		return fmt.Errorf("branch %q: %v", s.Branch, err)
	}
	return nil
}
