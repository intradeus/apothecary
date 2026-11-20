package conftest

import (
	"fmt"
	"testing"

	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

// The authors the fixtures write as.
var (
	byU1  = confstore.Change{Kind: confstore.AuthorUser, AuthorID: "u1"}
	byRun = confstore.Change{Kind: confstore.AuthorRun, AuthorID: "r1"}
)

// versions lists a ticket's versions, newest first.
func (s store) versions(id string) []confstore.TicketVersion {
	s.t.Helper()
	list, err := s.ListTicketVersions(ctx, id)
	ok(s.t, err)
	return list
}

func seqs(list []confstore.TicketVersion) []int {
	out := make([]int, len(list))
	for i, v := range list {
		out[i] = v.Seq
	}
	return out
}

// content is what a version holds of a ticket, to compare with the row.
func content(t confstore.Ticket) confstore.TicketVersion {
	return confstore.TicketVersion{TicketID: t.ID, Title: t.Title, Goal: t.Goal, BaseBranch: t.BaseBranch,
		ChangeType: t.ChangeType, TypeID: t.TypeID, Labels: t.Labels}
}

// contentOf is a version without its number, author and time.
func contentOf(v confstore.TicketVersion) confstore.TicketVersion {
	return confstore.TicketVersion{TicketID: v.TicketID, Title: v.Title, Goal: v.Goal, BaseBranch: v.BaseBranch,
		ChangeType: v.ChangeType, TypeID: v.TypeID, Labels: v.Labels}
}

func ticketVersions(t *testing.T, open opener) {
	t.Run("a write that changes the content appends a version, one that does not none", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		k := ticket("k1", "i9", "u1", 1)
		k.StatusID, k.TypeID, k.ChangeType, k.Labels = "st1", "ty1", trackerport.ChangeFix, []string{"a"}
		ok(t, s.UpsertTicket(ctx, k, byU1))
		ok(t, s.UpsertTicket(ctx, k, confstore.Change{Kind: confstore.AuthorUser, AuthorID: "u2"}))
		ok(t, s.SetTicketStatus(ctx, "k1", "st2", confstore.ChangedByUser, at(2)))
		same(t, "after an identical save and a move", seqs(s.versions("k1")), []int{1})
		k.Title, k.UpdatedAt = "renamed", at(3)
		ok(t, s.UpsertTicket(ctx, k, confstore.Change{Kind: confstore.AuthorAssistant, AuthorID: "u2"}))
		list := s.versions("k1")
		same(t, "newest first", seqs(list), []int{2, 1})
		same(t, "v2", list[0], confstore.TicketVersion{TicketID: "k1", Seq: 2, Title: "renamed", ChangeType: trackerport.ChangeFix,
			TypeID: "ty1", Labels: []string{"a"}, Change: confstore.Change{Kind: confstore.AuthorAssistant, AuthorID: "u2"}, CreatedAt: at(3)})
		v1, err := s.GetTicketVersion(ctx, "k1", 1)
		ok(t, err)
		same(t, "v1 by seq", v1, list[1])
		same(t, "v1's author", v1.Change, byU1)
		k.Title = "t"
		ok(t, s.UpsertTicket(ctx, k, confstore.Change{Kind: confstore.AuthorUser, AuthorID: "u1", RestoredFrom: 1}))
		v3, err := s.GetTicketVersion(ctx, "k1", 3)
		ok(t, err)
		same(t, "back to v1's content is a version of its own", contentOf(v3), contentOf(v1))
		same(t, "restored from", v3.RestoredFrom, 1)
	})

	t.Run("labels and what was seen are versions by their author, equal to the row", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		ok(t, s.UpsertTicket(ctx, ticket("k1", "i9", "u1", 1), byU1))
		got, err := s.SetTicketLabels(ctx, "k1", []string{"x", "y"}, false, at(2), byRun)
		ok(t, err)
		same(t, "labels", got, []string{"x", "y"})
		_, err = s.SetTicketLabels(ctx, "k1", []string{"x"}, true, at(3), byU1)
		ok(t, err)
		list := s.versions("k1")
		same(t, "a union adding nothing is no version", seqs(list), []int{2, 1})
		same(t, "the run's", list[0].Change, byRun)
		same(t, "the run's content", contentOf(list[0]), content(s.ticket("k1")))

		sync := confstore.Change{Kind: confstore.AuthorSync, AuthorID: "i9"}
		seen := confstore.TicketSeen{Key: "P-1", Title: "Seen", Goal: "g", Labels: []string{"z"}}
		ok(t, s.SetTicketSeen(ctx, "k1", seen, "ty1", trackerport.ChangeFix, at(4), sync))
		ok(t, s.SetTicketSeen(ctx, "k1", seen, "ty1", trackerport.ChangeFix, at(5), sync))
		list = s.versions("k1")
		same(t, "seen twice the same", seqs(list), []int{3, 2, 1})
		same(t, "the sync's", list[0].Change, sync)
		same(t, "the sync's content", contentOf(list[0]), content(s.ticket("k1")))
		same(t, "at", list[0].CreatedAt, at(4))
	})

	t.Run("missing", func(t *testing.T) {
		s := seeded(t, open)
		_, err := s.ListTicketVersions(ctx, "nope")
		is(t, "list", err, confstore.ErrNotFound)
		_, err = s.GetTicketVersion(ctx, "nope", 1)
		is(t, "get", err, confstore.ErrNotFound)
		ok(t, s.UpsertTicket(ctx, ticket("k1", "i1", "u1", 0), byU1))
		_, err = s.GetTicketVersion(ctx, "k1", 2)
		is(t, "a seq it never had", err, confstore.ErrNotFound)
		_, err = s.SetTicketLabels(ctx, "nope", []string{"x"}, false, at(1), byU1)
		is(t, "labels", err, confstore.ErrNotFound)
	})

	t.Run("they go with their ticket", func(t *testing.T) {
		s := seeded(t, open)
		k := ticket("k1", "i1", "u1", 0)
		ok(t, s.UpsertTicket(ctx, k, byU1))
		k.Title = "two"
		ok(t, s.UpsertTicket(ctx, k, byU1))
		ok(t, s.UpsertTicket(ctx, ticket("k2", "i1", "u1", 0), byU1))
		ok(t, s.DeleteTicket(ctx, "k1"))
		_, err := s.ListTicketVersions(ctx, "k1")
		is(t, "list", err, confstore.ErrNotFound)
		_, err = s.GetTicketVersion(ctx, "k1", 1)
		is(t, "get", err, confstore.ErrNotFound)
		ok(t, s.UpsertTicket(ctx, k, byU1))
		same(t, "a ticket of the same id starts over", seqs(s.versions("k1")), []int{1})
		same(t, "another's stay", seqs(s.versions("k2")), []int{1})
	})

	t.Run("the newest 100 are kept, and a seq is never reused", func(t *testing.T) {
		s := seeded(t, open)
		k := ticket("k1", "i1", "u1", 0)
		for i := range confstore.MaxTicketVersions + 1 {
			k.Title = fmt.Sprintf("t%d", i)
			ok(t, s.UpsertTicket(ctx, k, byU1))
		}
		list := s.versions("k1")
		same(t, "kept", len(list), confstore.MaxTicketVersions)
		same(t, "newest and oldest", []int{list[0].Seq, list[len(list)-1].Seq}, []int{101, 2})
		_, err := s.GetTicketVersion(ctx, "k1", 1)
		is(t, "the oldest is gone", err, confstore.ErrNotFound)
		k.Title = "t0"
		ok(t, s.UpsertTicket(ctx, k, byU1))
		same(t, "next", s.versions("k1")[0].Seq, 102)
	})

	t.Run("a version holds no tracker type in place", func(t *testing.T) {
		s := seeded(t, open)
		trackerLists(t, s)
		k := ticket("k1", "i9", "u1", 0)
		k.TypeID, k.ChangeType = "ty1", trackerport.ChangeFix
		ok(t, s.UpsertTicket(ctx, k, byU1))
		k.TypeID = ""
		ok(t, s.UpsertTicket(ctx, k, byU1))
		ok(t, s.ReplaceTrackerTypes(ctx, "i9", nil, nil))
		v1, err := s.GetTicketVersion(ctx, "k1", 1)
		ok(t, err)
		same(t, "its type, gone from the tracker", v1.TypeID, "ty1")
	})

	t.Run("a person's and an assistant's edits, restored as a new version", func(t *testing.T) {
		s := seeded(t, open)
		svc := service(t, s)
		k, err := svc.CreateTicket(ctx, confstore.Ticket{UserID: "u1", Title: "First", Goal: "one\ntwo", Labels: []string{"a"}}, byU1)
		ok(t, err)
		_, err = svc.UpdateTicket(ctx, k.ID, confstore.Ticket{Title: "First", Goal: "one\ntwo"}, confstore.Change{Kind: confstore.AuthorUser, AuthorID: "u2"})
		ok(t, err)
		same(t, "an identical save", seqs(s.versions(k.ID)), []int{1})
		assisted := confstore.Change{Kind: confstore.AuthorAssistant, AuthorID: "u2"}
		_, err = svc.UpdateTicket(ctx, k.ID, confstore.Ticket{Title: "Second", Goal: "one\nthree", ChangeType: "fix", Labels: []string{}}, assisted)
		ok(t, err)
		list, err := svc.TicketVersions(ctx, k.ID)
		ok(t, err)
		same(t, "two", seqs(list), []int{2, 1})
		same(t, "v2's author", list[0].Change, assisted)
		same(t, "what v2 changed", list[0].Changed, []string{"title", "goal", "change_type", "type", "labels"})
		same(t, "the oldest", []bool{list[0].Oldest, list[1].Oldest}, []bool{false, true})
		same(t, "the oldest changed nothing", len(list[1].Changed), 0)

		restorer := confstore.Change{Kind: confstore.AuthorUser, AuthorID: "u3"}
		got, err := svc.RestoreTicketVersion(ctx, k.ID, 1, restorer)
		ok(t, err)
		same(t, "the ticket is v1's again", content(got), contentOf(list[1]))
		same(t, "stored", content(s.ticket(k.ID)), contentOf(list[1]))
		v3, err := svc.TicketVersion(ctx, k.ID, 3)
		ok(t, err)
		same(t, "v3 is v1's content", contentOf(v3), contentOf(list[1]))
		same(t, "by the restorer, from v1", v3.Change, confstore.Change{Kind: confstore.AuthorUser, AuthorID: "u3", RestoredFrom: 1})
		_, err = svc.RestoreTicketVersion(ctx, k.ID, 3, restorer)
		ok(t, err)
		_, err = svc.RestoreTicketVersion(ctx, k.ID, 1, restorer)
		ok(t, err)
		same(t, "restoring what it holds appends nothing", seqs(s.versions(k.ID)), []int{3, 2, 1})
		_, err = svc.RestoreTicketVersion(ctx, k.ID, 9, restorer)
		is(t, "a seq it never had", err, confstore.ErrNotFound)
		_, err = svc.RestoreTicketVersion(ctx, "nope", 1, restorer)
		is(t, "a missing ticket", err, confstore.ErrNotFound)
		_, err = svc.TicketVersions(ctx, "nope")
		is(t, "a missing ticket's history", err, confstore.ErrNotFound)
	})

	t.Run("a restored type no longer on the tracker gives way to its change type's", func(t *testing.T) {
		s := seeded(t, open)
		svc := service(t, s)
		in, err := svc.InternalTracker(ctx, "u1")
		ok(t, err)
		cur, err := svc.TrackerTypes(ctx, in.ID)
		ok(t, err)
		types, err := svc.PutTrackerTypes(ctx, in.ID, append(cur,
			confstore.TrackerType{Name: "Incident", ChangeType: trackerport.ChangeFix}), nil)
		ok(t, err)
		incident := types[len(types)-1].ID
		k, err := svc.CreateTicket(ctx, confstore.Ticket{UserID: "u1", Title: "Old", TypeID: incident}, byU1)
		ok(t, err)
		_, err = svc.UpdateTicket(ctx, k.ID, confstore.Ticket{Title: "New", ChangeType: "feat"}, byU1)
		ok(t, err)
		_, err = svc.PutTrackerTypes(ctx, in.ID, types[:len(types)-1], nil)
		ok(t, err)
		got, err := svc.RestoreTicketVersion(ctx, k.ID, 1, byU1)
		ok(t, err)
		same(t, "title", got.Title, "Old")
		same(t, "change type", got.ChangeType, trackerport.ChangeFix)
		want := ""
		for _, ty := range types[:len(types)-1] {
			if ty.ChangeType == trackerport.ChangeFix {
				want = ty.ID
				break
			}
		}
		same(t, "the change type's type", got.TypeID, want)
	})

	t.Run("a record's history is its tracker's sync and its base branch, and is not restored", func(t *testing.T) {
		s := seeded(t, open)
		svc := service(t, s)
		remote := remoteTracker(t, svc, "u1", "jira")
		seen := confstore.TicketSeen{RemoteID: "10001", Key: "PROJ-1", Title: "Crash", Goal: "It crashes"}
		k, _, err := svc.RecordTicket(ctx, remote.ID, seen, "")
		ok(t, err)
		_, err = svc.SeeTicket(ctx, k.ID, seen)
		ok(t, err)
		_, _, err = svc.RecordTicket(ctx, remote.ID, seen, "")
		ok(t, err)
		sync := confstore.Change{Kind: confstore.AuthorSync, AuthorID: remote.ID}
		list := s.versions(k.ID)
		same(t, "seen unchanged", seqs(list), []int{1})
		same(t, "made by its tracker", list[0].Change, sync)
		seen.Title = "Crash, renamed"
		_, err = svc.SeeTicket(ctx, k.ID, seen)
		ok(t, err)
		_, err = svc.UpdateTicket(ctx, k.ID, confstore.Ticket{BaseBranch: "fix/crash"}, byU1)
		ok(t, err)
		list = s.versions(k.ID)
		same(t, "authors", []confstore.Change{list[0].Change, list[1].Change}, []confstore.Change{byU1, sync})
		before := s.ticket(k.ID)
		_, err = svc.RestoreTicketVersion(ctx, k.ID, 1, byU1)
		is(t, "restore", err, confstore.ErrInvalid)
		names(t, "restore", err, "jira")
		same(t, "the record", s.ticket(k.ID), before)
		same(t, "its history", seqs(s.versions(k.ID)), []int{3, 2, 1})
		_, err = svc.SeeTicket(ctx, k.ID, seen)
		ok(t, err)
		same(t, "a later sync, nothing to undo", s.ticket(k.ID).Title, "Crash, renamed")
	})
}
