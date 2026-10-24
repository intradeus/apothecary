package tickets

// Tickets for merge requests: a run created on a merge request someone opened
// belongs to a ticket of its own — the merge request's title and description,
// keyed by its URL, with no tracker behind it (tracker actions skip). The
// merge_request automation makes them as it finds merge requests; the
// workshop's Repositories tab when a person starts a run on one. Both find the
// same ticket for the same merge request, so a merge request's runs, however
// begun, are filed together.

import (
	"context"
	"fmt"
	"strings"

	"apothecary/internal/confstore"
	"apothecary/internal/ports"
)

// FindByKey is the project's ticket whose source key is key (compared
// without case), if there is one.
func (f *Factory) FindByKey(ctx context.Context, projectID, key string) (confstore.Ticket, bool, error) {
	all, err := f.Conf.ListTickets(ctx)
	if err != nil {
		return confstore.Ticket{}, false, err
	}
	for _, t := range all {
		if t.ProjectID == projectID && strings.EqualFold(t.SourceKey, key) {
			return t, true, nil
		}
	}
	return confstore.Ticket{}, false, nil
}

// CreateForMR makes the ticket of a merge request: titled with it and its
// author, its description and a line naming it as the goal, its URL as the
// key. automationID records the automation that made it ("" = a person).
func (f *Factory) CreateForMR(ctx context.Context, projectID, automationID string, mr ports.MRInfo) (confstore.Ticket, error) {
	goal := strings.TrimSpace(mr.Body)
	if goal != "" {
		goal += "\n\n"
	}
	goal += fmt.Sprintf("Merge request %s by %s: %s → %s.", mr.Ref.URL, mr.Author, mr.Source, mr.Target)
	t, err := f.Conf.CreateTicket(ctx, confstore.Ticket{
		ProjectID: projectID, Title: fmt.Sprintf("%s (!%d by %s)", mr.Title, mr.Ref.IID, mr.Author), Goal: goal,
		SourceKind: confstore.TicketKindMR, SourceKey: mr.Ref.URL, SourceURL: mr.Ref.URL, AutomationID: automationID,
	})
	if err != nil {
		return confstore.Ticket{}, err
	}
	f.log().Info("ticket made for a merge request", "ticket", t.ID, "mr", mr.Ref.URL, "automation", automationID)
	return t, nil
}

// ForMR is the merge request's ticket — whoever made it — or a new one;
// created says which.
func (f *Factory) ForMR(ctx context.Context, projectID string, mr ports.MRInfo) (t confstore.Ticket, created bool, err error) {
	t, found, err := f.FindByKey(ctx, projectID, mr.Ref.URL)
	if err != nil || found {
		return t, false, err
	}
	t, err = f.CreateForMR(ctx, projectID, "", mr)
	return t, err == nil, err
}
