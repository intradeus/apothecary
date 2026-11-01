package conftest

import (
	"errors"
	"slices"
	"testing"
	"time"

	platformsecrets "apothecary/internal/platform/secrets"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

// service is the config service over a store, for the rules the Service
// keeps on top of the rows (the internal tracker, remote-owned fields).
func service(t *testing.T, s store) *confstore.Service {
	t.Helper()
	key, err := platformsecrets.ParseKey(platformsecrets.GenerateKey())
	ok(t, err)
	return confstore.New(s.DB, key)
}

func statusIDs(t *testing.T, s store, integrationID string) []string {
	t.Helper()
	list, err := s.ListTrackerStatuses(ctx, integrationID)
	ok(t, err)
	return ids(list, func(r confstore.TrackerStatus) string { return r.ID })
}

// trackerTicket is a ticket mirroring key on tracker i9, in status.
func trackerTicket(id, key, status string, n int) confstore.Ticket {
	k := ticket(id, "p1", "u1", n)
	k.SourceID, k.SourceKind, k.SourceKey, k.StatusID = "i9", "jira", key, status
	return k
}

func trackers(t *testing.T, open opener) {
	t.Run("statuses and types round-trip, in position order", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		statuses := []confstore.TrackerStatus{
			{ID: "st2", IntegrationID: "i9", RemoteID: "2", Name: "Done", RemoteCategory: trackerport.CategoryDone, Position: 0, IsDone: true, FlowStep: trackerport.StepDone},
			{ID: "st1", IntegrationID: "i9", RemoteID: "1", Name: "To Do", RemoteCategory: trackerport.CategoryNew, Position: 1, FlowStep: trackerport.StepTodo},
			{ID: "st3", IntegrationID: "i9", RemoteID: "3", Name: "Parked", Position: 2},
		}
		ok(t, s.ReplaceTrackerStatuses(ctx, "i9", statuses, nil))
		got, err := s.ListTrackerStatuses(ctx, "i9")
		ok(t, err)
		same(t, "statuses", got, statuses)
		types := []confstore.TrackerType{
			{ID: "ty2", IntegrationID: "i9", RemoteID: "11", Name: "Story", ChangeType: trackerport.ChangeFeat},
			{ID: "ty1", IntegrationID: "i9", RemoteID: "10", Name: "Bug", ChangeType: trackerport.ChangeFix, Position: 1},
		}
		ok(t, s.ReplaceTrackerTypes(ctx, "i9", types, nil))
		gotTypes, err := s.ListTrackerTypes(ctx, "i9")
		ok(t, err)
		same(t, "types", gotTypes, types)
		none, err := s.ListTrackerStatuses(ctx, "i1")
		ok(t, err)
		same(t, "another integration's", len(none), 0)
	})

	t.Run("a flow step is held by one status", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		twice := []confstore.TrackerStatus{
			{ID: "st1", RemoteID: "1", Name: "To Do", FlowStep: trackerport.StepTodo},
			{ID: "st2", RemoteID: "2", Name: "Done", Position: 1, FlowStep: trackerport.StepTodo},
		}
		is(t, "store", s.ReplaceTrackerStatuses(ctx, "i9", twice, nil), confstore.ErrConflict)
		same(t, "after the refusal", statusIDs(t, s, "i9"), []string{"st1", "st2"})
		svc := service(t, s)
		list, err := svc.TrackerStatuses(ctx, "i9")
		ok(t, err)
		list[1].FlowStep = trackerport.StepTodo
		_, _, err = svc.PutTrackerStatuses(ctx, "i9", list, nil)
		is(t, "service", err, confstore.ErrInvalid)
		st, err := s.ListTrackerStatuses(ctx, "i9")
		ok(t, err)
		same(t, "steps kept", []trackerport.FlowStep{st[0].FlowStep, st[1].FlowStep}, []trackerport.FlowStep{trackerport.StepTodo, trackerport.StepDone})
	})

	t.Run("a status in use goes only with a replacement", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		ok(t, s.UpsertTicket(ctx, trackerTicket("k1", "PROJ-1", "st1", 1)))
		keep := []confstore.TrackerStatus{{ID: "st2", RemoteID: "2", Name: "Done", IsDone: true, FlowStep: trackerport.StepDone}}
		is(t, "no replacement", s.ReplaceTrackerStatuses(ctx, "i9", keep, nil), confstore.ErrReferenced)
		same(t, "after the refusal", statusIDs(t, s, "i9"), []string{"st1", "st2"})
		ok(t, s.ReplaceTrackerStatuses(ctx, "i9", keep, map[string]string{"st1": "st2"}))
		same(t, "after", statusIDs(t, s, "i9"), []string{"st2"})
		same(t, "the ticket moved", s.ticket("k1").StatusID, "st2")
	})

	t.Run("the internal tracker is made once, seeded, and stays", func(t *testing.T) {
		s := seeded(t, open)
		svc := service(t, s)
		in, err := svc.InternalTracker(ctx, "u1")
		ok(t, err)
		again, err := svc.InternalTracker(ctx, "u1")
		ok(t, err)
		same(t, "get-or-create", again.ID, in.ID)
		same(t, "kind", in.Kind+"/"+in.Port, confstore.KindInternal+"/"+confstore.PortTicketSource)
		other, err := svc.InternalTracker(ctx, "u2")
		ok(t, err)
		if other.ID == in.ID {
			t.Errorf("u2 shares u1's internal tracker %s", in.ID)
		}
		statuses, err := svc.TrackerStatuses(ctx, in.ID)
		ok(t, err)
		var steps []trackerport.FlowStep
		for _, st := range statuses {
			steps = append(steps, st.FlowStep)
		}
		same(t, "seeded steps", steps, trackerport.FlowSteps())
		types, err := svc.TrackerTypes(ctx, in.ID)
		ok(t, err)
		var cts []trackerport.ChangeType
		for _, ty := range types {
			cts = append(cts, ty.ChangeType)
		}
		for _, ct := range []trackerport.ChangeType{trackerport.ChangeFeat, trackerport.ChangeFix, trackerport.ChangeChore} {
			if !slices.Contains(cts, ct) {
				t.Errorf("seeded types %v lack %s", cts, ct)
			}
		}

		_, err = svc.CreateIntegration(ctx, confstore.Integration{UserID: "u1", Port: confstore.PortTicketSource,
			Kind: confstore.KindInternal, Name: "mine"})
		is(t, "create", err, confstore.ErrInvalid)
		is(t, "delete", svc.DeleteIntegration(ctx, in.ID), confstore.ErrInvalid)
		if _, err := s.GetIntegration(ctx, in.ID); err != nil {
			t.Errorf("the internal tracker is gone: %v", err)
		}
	})

	t.Run("a mirrored ticket refuses edits of what its tracker owns", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		k := fullTicket()
		ok(t, s.UpsertTicket(ctx, k))
		svc := service(t, s)
		for name, edit := range map[string]func(*confstore.Ticket){
			"title":  func(u *confstore.Ticket) { u.Title = "Renamed" },
			"goal":   func(u *confstore.Ticket) { u.Goal = "Other" },
			"status": func(u *confstore.Ticket) { u.StatusID = "st2" },
			"labels": func(u *confstore.Ticket) { u.Labels = []string{"x"} },
		} {
			upd := k
			edit(&upd)
			_, err := svc.UpdateTicket(ctx, k.ID, upd)
			var owned *confstore.RemoteOwnedError
			if !errors.As(err, &owned) || !errors.Is(err, confstore.ErrRemoteOwned) {
				t.Errorf("%s: err = %v, want RemoteOwnedError", name, err)
			}
		}
		same(t, "after the refusals", s.ticket(k.ID), k)
		upd := k
		upd.BaseBranch = "fix/two"
		got, err := svc.UpdateTicket(ctx, k.ID, upd)
		ok(t, err)
		same(t, "base branch", got.BaseBranch, "fix/two")
		same(t, "stored", s.ticket(k.ID).BaseBranch, "fix/two")
	})

	t.Run("the sync polls tickets neither done nor gone", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		ok(t, s.UpsertTicket(ctx, trackerTicket("k1", "PROJ-1", "st1", 1)))
		ok(t, s.UpsertTicket(ctx, trackerTicket("k2", "PROJ-2", "st2", 2)))
		gone := trackerTicket("k3", "PROJ-3", "st1", 3)
		gone.SyncState = confstore.SyncGone
		ok(t, s.UpsertTicket(ctx, gone))
		ok(t, s.UpsertTicket(ctx, trackerTicket("k4", "PROJ-4", "", 4)))
		ok(t, s.UpsertTicket(ctx, ticket("k5", "p1", "u1", 5)))
		svc := service(t, s)
		syncable, err := svc.ListSyncable(ctx, "i9")
		ok(t, err)
		same(t, "syncable", ids(syncable, func(k confstore.Ticket) string { return k.ID }), []string{"k1", "k4"})
		all, err := svc.ListTicketsBySource(ctx, "i9")
		ok(t, err)
		same(t, "all", ids(all, func(k confstore.Ticket) string { return k.ID }), []string{"k1", "k2", "k3", "k4"})
		none, err := svc.ListSyncable(ctx, "")
		ok(t, err)
		same(t, "sourceless", len(none), 0)
	})

	t.Run("an issue is found by owner, tracker and key, without case", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		ok(t, s.UpsertTicket(ctx, fullTicket()))
		svc := service(t, s)
		got, found, err := svc.TicketBySource(ctx, "u1", "i9", "proj-1")
		ok(t, err)
		if !found || got.ID != "k1" {
			t.Errorf("found = %v, %s", found, got.ID)
		}
		for _, miss := range [][3]string{{"u2", "i9", "PROJ-1"}, {"u1", "i8", "PROJ-1"}, {"u1", "i9", "PROJ-2"}, {"u1", "", "PROJ-1"}} {
			if _, found, err := svc.TicketBySource(ctx, miss[0], miss[1], miss[2]); err != nil || found {
				t.Errorf("%v: found = %v (%v)", miss, found, err)
			}
		}
		_, err = s.GetTicketBySource(ctx, "u2", "i9", "PROJ-1")
		is(t, "store miss", err, confstore.ErrNotFound)
	})

	t.Run("deleting a ticket source unlinks its tickets onto the internal tracker", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		ok(t, s.ReplaceTrackerStatuses(ctx, "i9", []confstore.TrackerStatus{
			{ID: "st1", IntegrationID: "i9", RemoteID: "1", Name: "TO DO", FlowStep: trackerport.StepTodo},
			{ID: "st2", IntegrationID: "i9", RemoteID: "2", Name: "Closed", Position: 1, IsDone: true, FlowStep: trackerport.StepDone},
			{ID: "st3", IntegrationID: "i9", RemoteID: "3", Name: "In QA", Position: 2},
		}, nil))
		k1 := trackerTicket("k1", "PROJ-1", "st1", 1)
		k1.TypeID, k1.ChangeType = "ty1", trackerport.ChangeFix
		ok(t, s.UpsertTicket(ctx, k1))
		ok(t, s.UpsertTicket(ctx, trackerTicket("k2", "PROJ-2", "st2", 2)))
		ok(t, s.UpsertTicket(ctx, trackerTicket("k3", "PROJ-3", "st3", 3)))
		ok(t, s.UpsertTicket(ctx, trackerTicket("k4", "PROJ-4", "", 4)))
		_, err := s.InsertTicketComment(ctx, confstore.TicketComment{ID: "c1", TicketID: "k1", RemoteID: "r1", Author: "ann", Body: "hi",
			Origin: confstore.CommentRemote, CreatedAt: time.Unix(10, 0).UTC(), UpdatedAt: time.Unix(10, 0).UTC()})
		ok(t, err)
		svc := service(t, s)
		internal, err := svc.InternalTracker(ctx, "u1")
		ok(t, err)
		statuses, err := svc.TrackerStatuses(ctx, internal.ID)
		ok(t, err)
		step := map[trackerport.FlowStep]string{}
		for _, st := range statuses {
			step[st.FlowStep] = st.ID
		}
		types, err := svc.TrackerTypes(ctx, internal.ID)
		ok(t, err)
		fixType := ""
		for _, ty := range types {
			if ty.ChangeType == trackerport.ChangeFix {
				fixType = ty.ID
			}
		}

		n, err := svc.DeleteIntegrationUnlinking(ctx, "i9")
		ok(t, err)
		same(t, "unlinked", n, 4)
		_, err = s.GetIntegration(ctx, "i9")
		is(t, "integration", err, confstore.ErrNotFound)
		for id, want := range map[string]string{
			"k1": step[trackerport.StepTodo], // "TO DO" is "To do", case aside
			"k2": step[trackerport.StepDone], // a Done status named unlike any
			"k3": step[trackerport.StepTodo], // neither
			"k4": step[trackerport.StepTodo], // never synced
		} {
			k := s.ticket(id)
			if k.StatusID != want || k.SourceID != "" || k.SourceKey != "" || k.SourceKind != "" || k.RemoteID != "" || k.SyncState != "" {
				t.Errorf("%s = status %s (want %s), source %q/%q/%q", id, k.StatusID, want, k.SourceID, k.SourceKind, k.SourceKey)
			}
		}
		if k := s.ticket("k1"); k.TypeID != fixType || k.ChangeType != trackerport.ChangeFix {
			t.Errorf("k1 type = %s %s, want the internal fix type %s", k.TypeID, k.ChangeType, fixType)
		}
		comments, err := s.ListTicketComments(ctx, "k1")
		ok(t, err)
		if len(comments) != 1 || comments[0].Origin != confstore.CommentRemote {
			t.Errorf("comments = %+v, want the remote history kept", comments)
		}
		_, err = svc.DeleteIntegrationUnlinking(ctx, internal.ID)
		is(t, "internal", err, confstore.ErrInvalid)
	})

	t.Run("an unlink naming too few tickets changes nothing", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		ok(t, s.UpsertTicket(ctx, trackerTicket("k1", "PROJ-1", "st1", 1)))
		ok(t, s.UpsertTicket(ctx, trackerTicket("k2", "PROJ-2", "st1", 2)))
		svc := service(t, s)
		internal, err := svc.InternalTracker(ctx, "u1")
		ok(t, err)
		statuses, err := svc.TrackerStatuses(ctx, internal.ID)
		ok(t, err)
		err = s.DeleteTicketSource(ctx, "i9", []confstore.TicketUnlink{{TicketID: "k1", StatusID: statuses[0].ID, ChangeType: trackerport.ChangeFeat}}, time.Unix(20, 0).UTC())
		is(t, "late ticket", err, confstore.ErrConflict)
		if k := s.ticket("k1"); k.SourceID != "i9" || k.StatusID != "st1" {
			t.Errorf("k1 = %s %s, want untouched", k.SourceID, k.StatusID)
		}
		_, err = s.GetIntegration(ctx, "i9")
		ok(t, err)
	})
}
