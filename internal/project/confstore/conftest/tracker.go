package conftest

import (
	"slices"
	"testing"

	platformsecrets "apothecary/internal/platform/secrets"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

// service is the config service over a store, for the rules the Service
// keeps on top of the rows (the internal tracker, records of remote issues,
// a workshop's ticket source).
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

// remoteTracker is a remote ticketsource integration of user's, made through
// the service.
func remoteTracker(t *testing.T, svc *confstore.Service, user, name string) confstore.Integration {
	t.Helper()
	in, err := svc.CreateIntegration(ctx, confstore.Integration{UserID: user, Port: confstore.PortTicketSource, Kind: "jira", Name: name})
	ok(t, err)
	return in
}

// workshop creates a project of user's through the service, with what it
// cannot go without; sourceID "" leaves the ticket source to the default.
func workshop(t *testing.T, svc *confstore.Service, user, name, sourceID string) (confstore.Project, error) {
	t.Helper()
	refs := map[string]string{}
	for _, port := range []string{confstore.PortRepository, confstore.PortProvisioner, confstore.PortHarness} {
		in, err := svc.CreateIntegration(ctx, confstore.Integration{UserID: user, Port: port, Kind: "fake", Name: port + " of " + name})
		ok(t, err)
		refs[port] = in.ID
	}
	return svc.CreateProject(ctx, confstore.Project{UserID: user, Name: name,
		RepoAccountID: refs[confstore.PortRepository], ProvisionerID: refs[confstore.PortProvisioner],
		HarnessIDs: []string{refs[confstore.PortHarness]}, TicketSourceID: sourceID,
		Repo: confstore.RepoConfig{URL: "https://example.test/g/" + name + ".git"}, DefaultFlow: template()})
}

func trackers(t *testing.T, open opener) {
	t.Run("statuses and types round-trip, in position order", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		statuses := []confstore.TrackerStatus{
			{ID: "st2", IntegrationID: "i9", RemoteID: "2", Name: "Done", Position: 0, IsDone: true},
			{ID: "st1", IntegrationID: "i9", RemoteID: "1", Name: "To Do", Position: 1},
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

	t.Run("a remote id is held by one status", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		twice := []confstore.TrackerStatus{
			{ID: "st1", RemoteID: "1", Name: "To Do"},
			{ID: "st2", RemoteID: "1", Name: "Done", Position: 1},
		}
		is(t, "store", s.ReplaceTrackerStatuses(ctx, "i9", twice, nil), confstore.ErrConflict)
		same(t, "after the refusal", statusIDs(t, s, "i9"), []string{"st1", "st2"})
	})

	t.Run("a listed status takes the name only", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		got, err := s.EnsureTrackerStatus(ctx, confstore.TrackerStatus{ID: "new", IntegrationID: "i9", RemoteID: "2", Name: "Closed", Position: 7})
		ok(t, err)
		same(t, "stored", got, confstore.TrackerStatus{ID: "st2", IntegrationID: "i9", RemoteID: "2", Name: "Closed", Position: 1, IsDone: true})
	})

	t.Run("a status in use goes only with a replacement", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		k := ticket("k1", "i9", "u1", 1)
		k.StatusID = "st1"
		ok(t, s.UpsertTicket(ctx, k, confstore.Change{Kind: confstore.AuthorUser}))
		keep := []confstore.TrackerStatus{{ID: "st2", RemoteID: "2", Name: "Done", IsDone: true}}
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
		// The migrations seed the same ids: keep them in step.
		p := in.ID + "-"
		same(t, "seeded statuses", statuses, []confstore.TrackerStatus{
			{ID: p + "todo", IntegrationID: in.ID, RemoteID: p + "todo", Name: "To do", Position: 0},
			{ID: p + "in_progress", IntegrationID: in.ID, RemoteID: p + "in_progress", Name: "In progress", Position: 1},
			{ID: p + "in_review", IntegrationID: in.ID, RemoteID: p + "in_review", Name: "In review", Position: 2},
			{ID: p + "done", IntegrationID: in.ID, RemoteID: p + "done", Name: "Done", Position: 3, IsDone: true},
			{ID: p + "blocked", IntegrationID: in.ID, RemoteID: p + "blocked", Name: "Blocked", Position: 4},
		})
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

	t.Run("only the internal tracker's statuses are edited", func(t *testing.T) {
		s := seeded(t, open)
		svc := service(t, s)
		remote := remoteTracker(t, svc, "u1", "jira")
		_, err := svc.PutTrackerStatuses(ctx, remote.ID, []confstore.TrackerStatus{{Name: "Open"}}, nil)
		is(t, "remote", err, confstore.ErrInvalid)
		same(t, "nothing stored", statusIDs(t, s, remote.ID), []string{})
		in, err := svc.InternalTracker(ctx, "u1")
		ok(t, err)
		list, err := svc.TrackerStatuses(ctx, in.ID)
		ok(t, err)
		list = append(list[:1], confstore.TrackerStatus{Name: "Parked", IsDone: true})
		saved, err := svc.PutTrackerStatuses(ctx, in.ID, list, map[string]string{})
		ok(t, err)
		same(t, "names", ids(saved, func(r confstore.TrackerStatus) string { return r.Name }), []string{"To do", "Parked"})
		same(t, "a new row is its own remote", saved[1].RemoteID, saved[1].ID)
	})

	t.Run("a remote tracker's types take only their change type", func(t *testing.T) {
		s := seeded(t, open)
		svc := service(t, s)
		remote := remoteTracker(t, svc, "u1", "jira")
		cur, err := svc.EnsureRemoteTypes(ctx, remote.ID, []trackerport.RemoteType{{ID: "10", Name: "Bug"}, {ID: "11", Name: "Story"}})
		ok(t, err)
		edit := slices.Clone(cur)
		edit[0].ChangeType, edit[0].Name = "hotfix", "Renamed"
		saved, err := svc.PutTrackerTypes(ctx, remote.ID, edit, nil)
		ok(t, err)
		same(t, "names stay the tracker's", ids(saved, func(r confstore.TrackerType) string { return r.Name }), []string{"Bug", "Story"})
		same(t, "change types", ids(saved, func(r confstore.TrackerType) string { return string(r.ChangeType) }),
			[]string{"hotfix", string(cur[1].ChangeType)})

		_, err = svc.PutTrackerTypes(ctx, remote.ID, edit, map[string]string{cur[1].ID: cur[0].ID})
		is(t, "a deletion", err, confstore.ErrInvalid)
		_, err = svc.PutTrackerTypes(ctx, remote.ID, edit[:1], nil)
		is(t, "one left out", err, confstore.ErrInvalid)
		_, err = svc.PutTrackerTypes(ctx, remote.ID, []confstore.TrackerType{edit[0], edit[0]}, nil)
		is(t, "one sent twice", err, confstore.ErrInvalid)
		stranger := edit[1]
		stranger.ID = "not-stored"
		_, err = svc.PutTrackerTypes(ctx, remote.ID, []confstore.TrackerType{edit[0], stranger}, nil)
		is(t, "one not stored", err, confstore.ErrInvalid)
		edit[1].ChangeType = "refactorish"
		_, err = svc.PutTrackerTypes(ctx, remote.ID, edit, nil)
		is(t, "an unknown change type", err, confstore.ErrInvalid)
		_, err = svc.PutTrackerTypes(ctx, "ghost", nil, nil)
		if err == nil {
			t.Error("a missing integration's types were saved")
		}
	})

	t.Run("the internal tracker's types are the user's", func(t *testing.T) {
		s := seeded(t, open)
		svc := service(t, s)
		in, err := svc.InternalTracker(ctx, "u1")
		ok(t, err)
		cur, err := svc.TrackerTypes(ctx, in.ID)
		ok(t, err)
		if len(cur) < 2 {
			t.Fatalf("seeded types = %+v", cur)
		}
		keep := cur[0]
		keep.Name = " Feature "
		replace := map[string]string{}
		for _, gone := range cur[1:] {
			replace[gone.ID] = keep.ID
		}
		saved, err := svc.PutTrackerTypes(ctx, in.ID,
			[]confstore.TrackerType{{Name: "Spike", ChangeType: trackerport.ChangeChore}, keep}, replace)
		ok(t, err)
		same(t, "names", ids(saved, func(r confstore.TrackerType) string { return r.Name }), []string{"Spike", "Feature"})
		same(t, "a new row is its own remote", saved[0].RemoteID, saved[0].ID)
		same(t, "the kept one", saved[1].ID, keep.ID)

		spike := saved[0]
		for _, c := range []struct {
			name    string
			rows    []confstore.TrackerType
			replace map[string]string
		}{
			{"no name", []confstore.TrackerType{{Name: " ", ChangeType: trackerport.ChangeFix}}, nil},
			{"a name twice", []confstore.TrackerType{spike, {Name: "spike", ChangeType: trackerport.ChangeFix}}, nil},
			{"another tracker's id", []confstore.TrackerType{{ID: "ghost", Name: "Ghost", ChangeType: trackerport.ChangeFix}}, nil},
			{"one listed twice", []confstore.TrackerType{spike, {ID: spike.ID, Name: "Other", ChangeType: trackerport.ChangeFix}}, nil},
			{"a replacement for a kept one", []confstore.TrackerType{spike}, map[string]string{spike.ID: spike.ID}},
			{"a replacement that goes too", []confstore.TrackerType{spike}, map[string]string{keep.ID: keep.ID}},
		} {
			_, err := svc.PutTrackerTypes(ctx, in.ID, c.rows, c.replace)
			is(t, c.name, err, confstore.ErrInvalid)
		}
		got, err := svc.TrackerTypes(ctx, in.ID)
		ok(t, err)
		same(t, "after the refusals", ids(got, func(r confstore.TrackerType) string { return r.Name }), []string{"Spike", "Feature"})
	})

	t.Run("an internal ticket is its tracker's own issue", func(t *testing.T) {
		s := seeded(t, open)
		svc := service(t, s)
		in, err := svc.InternalTracker(ctx, "u1")
		ok(t, err)
		k, err := svc.CreateTicket(ctx, confstore.Ticket{UserID: "u1", Title: " Fix it ", ChangeType: "fix",
			SourceID: "ghost", SourceKind: "jira", SourceKey: "X-1", RemoteID: "r", SourceURL: "u", SourceType: "Bug"}, confstore.Change{Kind: confstore.AuthorUser})
		ok(t, err)
		k = s.ticket(k.ID)
		same(t, "source", []string{k.SourceID, k.SourceKind, k.RemoteID, k.SourceKey, k.SourceURL, k.SourceType},
			[]string{in.ID, confstore.KindInternal, k.ID, "", "", ""})
		same(t, "first status", k.StatusID, in.ID+"-todo")
		same(t, "title", k.Title, "Fix it")
		same(t, "key", k.TrackerKey(), k.ID)
		if !k.Internal() {
			t.Errorf("an internal ticket reads as remote: %+v", k)
		}
		ok(t, svc.SetTicketStatus(ctx, k.ID, in.ID+"-done", confstore.ChangedByUser))
		same(t, "moved", s.ticket(k.ID).StatusID, in.ID+"-done")
	})

	t.Run("a remote issue's record is made once and follows the issue", func(t *testing.T) {
		s := seeded(t, open)
		svc := service(t, s)
		remote := remoteTracker(t, svc, "u1", "jira")
		seen := confstore.TicketSeen{RemoteID: "10001", Key: "PROJ-1", URL: "https://example.test/1", Title: "Crash",
			Goal: "It crashes", Type: "Bug", TypeRemoteID: "10", Labels: []string{"a"}}
		k, created, err := svc.RecordTicket(ctx, remote.ID, seen, "auto1")
		ok(t, err)
		same(t, "created", created, true)
		same(t, "stored", s.ticket(k.ID), k)
		same(t, "provenance", []string{k.UserID, k.SourceID, k.SourceKind, k.RemoteID, k.SourceKey, k.AutomationID, k.StatusID},
			[]string{"u1", remote.ID, "jira", "10001", "PROJ-1", "auto1", ""})
		types, err := svc.TrackerTypes(ctx, remote.ID)
		ok(t, err)
		same(t, "its type is listed", ids(types, func(r confstore.TrackerType) string { return r.ID }), []string{k.TypeID})

		seen.Key, seen.Title = "MOVED-9", "Crash, moved"
		again, created, err := svc.RecordTicket(ctx, remote.ID, seen, "auto2")
		ok(t, err)
		same(t, "created again", created, false)
		same(t, "the same record", again.ID, k.ID)
		same(t, "the key followed", again.SourceKey, "MOVED-9")
		same(t, "the title followed", again.Title, "Crash, moved")
		same(t, "the automation stays", again.AutomationID, "auto1")
		same(t, "one record", len(records(t, svc, remote.ID, "10001")), 1)

		seen.Title = "Seen again"
		upd, err := svc.SeeTicket(ctx, k.ID, seen)
		ok(t, err)
		same(t, "seen", upd.Title, "Seen again")
		byRemote, found, err := svc.TicketByRemote(ctx, remote.ID, "10001")
		ok(t, err)
		same(t, "by remote", found, true)
		same(t, "by remote row", byRemote, upd)
		_, found, err = svc.TicketByRemote(ctx, remote.ID, "nope")
		ok(t, err)
		same(t, "none", found, false)

		in, err := svc.InternalTracker(ctx, "u1")
		ok(t, err)
		_, _, err = svc.RecordTicket(ctx, in.ID, seen, "")
		is(t, "internal source", err, confstore.ErrInvalid)
		_, _, err = svc.RecordTicket(ctx, remote.ID, confstore.TicketSeen{Title: "no id"}, "")
		is(t, "no remote id", err, confstore.ErrInvalid)
	})

	t.Run("a record changes only its base branch here", func(t *testing.T) {
		s := seeded(t, open)
		svc := service(t, s)
		remote := remoteTracker(t, svc, "u1", "jira")
		k, _, err := svc.RecordTicket(ctx, remote.ID, confstore.TicketSeen{RemoteID: "1", Key: "P-1", Title: "Remote"}, "")
		ok(t, err)
		upd, err := svc.UpdateTicket(ctx, k.ID, confstore.Ticket{Title: "Mine", Goal: "g", BaseBranch: " feat/x ", Labels: []string{"l"}}, confstore.Change{Kind: confstore.AuthorUser})
		ok(t, err)
		same(t, "base branch", upd.BaseBranch, "feat/x")
		same(t, "the rest", []string{upd.Title, upd.Goal}, []string{"Remote", ""})
		same(t, "stored", s.ticket(k.ID).BaseBranch, "feat/x")
		is(t, "status", svc.SetTicketStatus(ctx, k.ID, "any", confstore.ChangedByUser), confstore.ErrInvalid)
		_, err = svc.SetTicketLabels(ctx, k.ID, []string{"x"}, false, confstore.Change{Kind: confstore.AuthorUser})
		is(t, "labels", err, confstore.ErrInvalid)
		is(t, "link", svc.AddTicketLink(ctx, k.ID, trackerport.TicketLink{URL: "https://example.test"}), confstore.ErrInvalid)
		_, _, err = svc.AddTicketComment(ctx, confstore.TicketComment{TicketID: k.ID, Body: "hi", Origin: confstore.CommentUser})
		is(t, "comment", err, confstore.ErrInvalid)
		_, err = svc.TicketComments(ctx, k.ID)
		is(t, "comments", err, confstore.ErrInvalid)
	})

	t.Run("a project's ticket source defaults to the internal tracker", func(t *testing.T) {
		s := seeded(t, open)
		svc := service(t, s)
		in, err := svc.InternalTracker(ctx, "u1")
		ok(t, err)
		p, err := workshop(t, svc, "u1", "web2", "")
		ok(t, err)
		same(t, "default", p.TicketSourceID, in.ID)
		same(t, "stored", s.project(p.ID).TicketSourceID, in.ID)

		remote := remoteTracker(t, svc, "u1", "jira")
		p.TicketSourceID = remote.ID
		p, err = svc.UpdateProject(ctx, p.ID, p)
		ok(t, err)
		same(t, "switched", p.TicketSourceID, remote.ID)
		p.TicketSourceID = ""
		p, err = svc.UpdateProject(ctx, p.ID, p)
		ok(t, err)
		same(t, "an empty one keeps it", p.TicketSourceID, remote.ID)

		theirs := remoteTracker(t, svc, "u2", "jira")
		_, err = workshop(t, svc, "u1", "api1", theirs.ID)
		is(t, "another user's", err, confstore.ErrInvalid)
		_, err = workshop(t, svc, "u1", "api2", p.RepoAccountID)
		is(t, "not a ticket source", err, confstore.ErrInvalid)
		_, err = workshop(t, svc, "u1", "api3", "ghost")
		is(t, "missing", err, confstore.ErrInvalid)

		err = svc.DeleteIntegration(ctx, remote.ID)
		is(t, "in use", err, confstore.ErrReferenced)
		names(t, "in use", err, "web2")
	})
}

func records(t *testing.T, svc *confstore.Service, sourceID string, remoteIDs ...string) map[string]confstore.Ticket {
	t.Helper()
	m, err := svc.TicketsByRemote(ctx, sourceID, remoteIDs)
	ok(t, err)
	return m
}
