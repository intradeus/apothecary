package confstore

import (
	"context"
	"slices"
	"strings"

	"apothecary/internal/ports/trackerport"
)

// TicketUnlink is one ticket's move off a ticketsource integration being
// deleted, onto its owner's internal tracker (DeleteIntegrationUnlinking).
type TicketUnlink struct {
	TicketID   string
	StatusID   string // "" = none (the internal tracker has no statuses)
	TypeID     string // "" = none
	ChangeType trackerport.ChangeType
}

// DeleteIntegrationUnlinking deletes a ticketsource integration whose tickets
// still mirror its issues (docs/ticket-sync-plan.md § Deleting an
// integration): in one transaction, every such ticket is unlinked — its
// source and remote columns cleared — and moved to its owner's internal
// tracker, then the integration goes (its status and type lists with it).
// Each ticket's status there is the one of the same name (case aside), else
// the done step's when its status was Done, else the todo step's; its type is
// the first of its change type. Comments stay, as remote history. Returns how
// many tickets were unlinked. The internal tracker is refused (ErrInvalid); an
// integration with no tickets is simply deleted.
func (s *Service) DeleteIntegrationUnlinking(ctx context.Context, id string) (int, error) {
	in, err := s.db.GetIntegration(ctx, id)
	if err != nil {
		return 0, err
	}
	if in.Kind == KindInternal {
		return 0, invalidf("integration %s: the internal tracker is built in and cannot be deleted", in.Name)
	}
	list, err := s.db.ListTicketsBySource(ctx, id, false)
	if err != nil {
		return 0, err
	}
	if len(list) == 0 {
		return 0, s.db.DeleteIntegration(ctx, id)
	}
	statuses, err := s.db.ListTrackerStatuses(ctx, id)
	if err != nil {
		return 0, err
	}
	type internalLists struct {
		statuses []TrackerStatus
		types    []TrackerType
	}
	byOwner := map[string]internalLists{}
	moves := make([]TicketUnlink, 0, len(list))
	for _, t := range list {
		lists, ok := byOwner[t.UserID]
		if !ok {
			tracker, err := s.InternalTracker(ctx, t.UserID)
			if err != nil {
				return 0, err
			}
			if lists.statuses, err = s.db.ListTrackerStatuses(ctx, tracker.ID); err != nil {
				return 0, err
			}
			if lists.types, err = s.db.ListTrackerTypes(ctx, tracker.ID); err != nil {
				return 0, err
			}
			byOwner[t.UserID] = lists
		}
		mv := TicketUnlink{TicketID: t.ID, ChangeType: normalChange(t.ChangeType)}
		var cur *TrackerStatus
		if i := slices.IndexFunc(statuses, func(st TrackerStatus) bool { return st.ID == t.StatusID }); i >= 0 {
			cur = &statuses[i]
		}
		mv.StatusID = unlinkedStatus(cur, lists.statuses)
		if i := slices.IndexFunc(lists.types, func(ty TrackerType) bool { return ty.ChangeType == mv.ChangeType }); i >= 0 {
			mv.TypeID = lists.types[i].ID
		} else if len(lists.types) > 0 {
			mv.TypeID, mv.ChangeType = lists.types[0].ID, lists.types[0].ChangeType
		}
		moves = append(moves, mv)
	}
	if err := s.db.DeleteTicketSource(ctx, id, moves, s.now().UTC()); err != nil {
		return 0, err
	}
	return len(moves), nil
}

// unlinkedStatus is the internal status an unlinked ticket takes: the one
// named like its remote status, else the done step's for a Done one, else the
// todo step's, else the first.
func unlinkedStatus(cur *TrackerStatus, internal []TrackerStatus) string {
	if len(internal) == 0 {
		return ""
	}
	if cur != nil {
		if i := slices.IndexFunc(internal, func(st TrackerStatus) bool { return strings.EqualFold(st.Name, cur.Name) }); i >= 0 {
			return internal[i].ID
		}
	}
	step := trackerport.StepTodo
	if cur != nil && cur.IsDone {
		step = trackerport.StepDone
	}
	if i := slices.IndexFunc(internal, func(st TrackerStatus) bool { return st.FlowStep == step }); i >= 0 {
		return internal[i].ID
	}
	return internal[0].ID
}
