// Package nothing is the kind of a flow whose runs are started on no work
// item (operates_on: nothing): a workspace on a branch, with no ticket and
// no merge request behind it — a person's own sandbox, or a chore an
// automation runs on a schedule. It has no items: a start names none, a run
// holds none, so nothing is ever busy and any number may run at once.
package nothing

import (
	"context"

	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workitem"
)

// Name is the kind's operates_on value.
const Name = "nothing"

// Kind is the nothing kind.
type Kind struct{}

var _ workitem.Kind = Kind{}

func (Kind) Name() string                 { return Name }
func (Kind) Noun() string                 { return "nothing" }
func (Kind) Describe(workitem.Ref) string { return "nothing" }

// Required is false: there is no item to name.
func (Kind) Required() bool { return false }

// Accompanies is false: there is no item to bring along.
func (Kind) Accompanies() bool { return false }

// Alone is true: a start of such a flow names no item of any kind — a
// ticket's work is a ticket flow's.
func (Kind) Alone() bool { return true }

func (Kind) Requested(string, runinput.Ticket, runinput.Input) (workitem.Ref, bool) {
	return workitem.Ref{}, false
}

func (Kind) Held(run.View) []workitem.Ref { return nil }

// Prepare has nothing to point the run at: the start's own branch stands.
func (Kind) Prepare(context.Context, *workitem.Start, workitem.Ref) error { return nil }
