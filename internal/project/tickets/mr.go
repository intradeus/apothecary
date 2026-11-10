package tickets

// A run on a merge request someone opened works on that merge request, not
// on a ticket: no ticket is made for it. Its title and description are the
// work the run is given, and its runs are found by what they worked on
// (engine.RunsOnMR), however they began — the merge_request automation, or a
// person starting one from the workshop or the New run dialog.

import (
	"context"
	"fmt"
	"strings"

	"apothecary/internal/ports/repoport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/run/runinput"
)

// FindByKey is the project's ticket whose source key is key (compared
// without case), if there is one — an indexed lookup.
func (f *Factory) FindByKey(ctx context.Context, projectID, key string) (confstore.Ticket, bool, error) {
	return f.Conf.TicketByProjectKey(ctx, projectID, key)
}

// MRWork is the work a run on a merge request is given: titled with it and
// its author, its description and a line naming it as the goal. No ticket:
// the run's ticket fields carry it, so a flow's prompts read it the way
// they read a ticket's.
func MRWork(mr repoport.MRInfo) runinput.Ticket {
	goal := strings.TrimSpace(mr.Body)
	if goal != "" {
		goal += "\n\n"
	}
	goal += fmt.Sprintf("Merge request %s by %s: %s → %s.", mr.Ref.URL, mr.Author, mr.Source, mr.Target)
	return runinput.Ticket{Title: fmt.Sprintf("%s (!%d by %s)", mr.Title, mr.Ref.IID, mr.Author), Goal: goal}
}
