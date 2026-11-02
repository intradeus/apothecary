// Package noticestest is the notices.Store contract, run against every
// implementation (MemStore in notices' own tests, Postgres in pgstore's). It
// states what Postgres does: production runs nothing else, so a twin that
// differs makes the unit tier prove the wrong thing.
package noticestest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"apothecary/internal/notices"
)

// Users the contract raises notices to; open must return a store where they
// exist.
const (
	U1 = "u1"
	U2 = "u2"
)

// RunContract exercises one Store implementation. open must return a fresh
// store with no notices, in which users U1 and U2 exist.
func RunContract(t *testing.T, open func(t *testing.T) notices.Store) {
	t.Helper()
	t0 := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }
	ctx := context.Background()

	t.Run("a repeat with the same key updates the one row", func(t *testing.T) {
		s := open(t)
		first, inserted := raise(t, s, notices.Notice{ID: "n1", UserID: U1, Kind: "k", Key: "x", Title: "one", Body: "b1", Link: "/a", LastSeenAt: at(0)})
		if !inserted || first.Count != 1 || !first.CreatedAt.Equal(at(0)) || !first.LastSeenAt.Equal(at(0)) {
			t.Fatalf("first = %+v, inserted %v", first, inserted)
		}
		second, inserted := raise(t, s, notices.Notice{ID: "n2", UserID: U1, Kind: "k", Key: "x", Title: "two", LastSeenAt: at(5)})
		if inserted || second.ID != "n1" || second.Count != 2 || second.Title != "two" || second.Body != "" || second.Link != "" ||
			!second.CreatedAt.Equal(at(0)) || !second.LastSeenAt.Equal(at(5)) {
			t.Errorf("second = %+v, inserted %v", second, inserted)
		}
		list := listed(t, s, U1)
		if len(list) != 1 || list[0].ID != "n1" || list[0].Count != 2 || list[0].Title != "two" {
			t.Errorf("list = %+v", list)
		}
	})

	t.Run("an empty key always inserts", func(t *testing.T) {
		s := open(t)
		for i := range 2 {
			if _, inserted := raise(t, s, notices.Notice{ID: fmt.Sprint("n", i), UserID: U1, Kind: "k", Title: "t", LastSeenAt: at(i)}); !inserted {
				t.Errorf("raise %d did not insert", i)
			}
		}
		if err := s.ClearNotices(ctx, "", at(9)); err != nil {
			t.Fatal(err)
		}
		if list := listed(t, s, U1); len(list) != 2 {
			t.Errorf("list = %+v", list)
		}
	})

	t.Run("a key is per user", func(t *testing.T) {
		s := open(t)
		raise(t, s, notices.Notice{ID: "n1", UserID: U1, Kind: "k", Key: "x", Title: "t", LastSeenAt: at(0)})
		if _, inserted := raise(t, s, notices.Notice{ID: "n2", UserID: U2, Kind: "k", Key: "x", Title: "t", LastSeenAt: at(1)}); !inserted {
			t.Error("u2's raise joined u1's notice")
		}
		if l1, l2 := listed(t, s, U1), listed(t, s, U2); len(l1) != 1 || len(l2) != 1 || l1[0].Count != 1 || l2[0].ID != "n2" {
			t.Errorf("u1 = %+v, u2 = %+v", l1, l2)
		}
	})

	t.Run("a clear re-arms the key and keeps the notice", func(t *testing.T) {
		s := open(t)
		raise(t, s, notices.Notice{ID: "n1", UserID: U1, Kind: "k", Key: "x", Title: "t", LastSeenAt: at(0)})
		raise(t, s, notices.Notice{ID: "n2", UserID: U2, Kind: "k", Key: "x", Title: "t", LastSeenAt: at(0)})
		if err := s.ClearNotices(ctx, "x", at(1)); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"n1", "n2"} {
			if n, err := s.GetNotice(ctx, id); err != nil || !n.ClearedAt.Equal(at(1)) {
				t.Errorf("%s after clear = %+v, %v", id, n, err)
			}
		}
		if _, inserted := raise(t, s, notices.Notice{ID: "n3", UserID: U1, Kind: "k", Key: "x", Title: "again", LastSeenAt: at(2)}); !inserted {
			t.Error("a raise after the clear joined the cleared notice")
		}
		list := listed(t, s, U1)
		if len(list) != 2 || list[0].ID != "n3" || list[1].ID != "n1" {
			t.Errorf("list = %+v", list)
		}
		// A second clear leaves the first stamp alone.
		if err := s.ClearNotices(ctx, "x", at(3)); err != nil {
			t.Fatal(err)
		}
		if n, _ := s.GetNotice(ctx, "n1"); !n.ClearedAt.Equal(at(1)) {
			t.Errorf("n1 cleared again: %v", n.ClearedAt)
		}
	})

	t.Run("a dismissed uncleared notice absorbs repeats, unlisted", func(t *testing.T) {
		s := open(t)
		raise(t, s, notices.Notice{ID: "n1", UserID: U1, Kind: "k", Key: "x", Title: "t", LastSeenAt: at(0)})
		if err := s.DismissNotice(ctx, "n1", at(1)); err != nil {
			t.Fatal(err)
		}
		got, inserted := raise(t, s, notices.Notice{ID: "n2", UserID: U1, Kind: "k", Key: "x", Title: "t2", LastSeenAt: at(2)})
		if inserted || got.ID != "n1" || got.Count != 2 || !got.DismissedAt.Equal(at(1)) {
			t.Errorf("repeat = %+v, inserted %v", got, inserted)
		}
		if list := listed(t, s, U1); len(list) != 0 {
			t.Errorf("list = %+v", list)
		}
		// Dismissing again is a no-op that keeps the first stamp.
		if err := s.DismissNotice(ctx, "n1", at(3)); err != nil {
			t.Fatal(err)
		}
		if n, _ := s.GetNotice(ctx, "n1"); !n.DismissedAt.Equal(at(1)) {
			t.Errorf("dismissed again: %v", n.DismissedAt)
		}
	})

	t.Run("the list is newest first, then by id", func(t *testing.T) {
		s := open(t)
		raise(t, s, notices.Notice{ID: "a", UserID: U1, Kind: "k", Key: "a", Title: "t", LastSeenAt: at(1)})
		raise(t, s, notices.Notice{ID: "b", UserID: U1, Kind: "k", Title: "t", LastSeenAt: at(2)})
		raise(t, s, notices.Notice{ID: "c", UserID: U1, Kind: "k", Title: "t", LastSeenAt: at(2)})
		raise(t, s, notices.Notice{ID: "z", UserID: U1, Kind: "k", Key: "a", Title: "t", LastSeenAt: at(3)}) // bumps a
		var ids []string
		for _, n := range listed(t, s, U1) {
			ids = append(ids, n.ID)
		}
		if fmt.Sprint(ids) != "[a c b]" {
			t.Errorf("order = %v", ids)
		}
	})

	t.Run("a missing notice is not found", func(t *testing.T) {
		s := open(t)
		if _, err := s.GetNotice(ctx, "nope"); !errors.Is(err, notices.ErrNotFound) {
			t.Errorf("get = %v", err)
		}
		if err := s.DismissNotice(ctx, "nope", at(0)); !errors.Is(err, notices.ErrNotFound) {
			t.Errorf("dismiss = %v", err)
		}
	})

	t.Run("dismiss all touches only that user", func(t *testing.T) {
		s := open(t)
		raise(t, s, notices.Notice{ID: "n1", UserID: U1, Kind: "k", Title: "t", LastSeenAt: at(0)})
		raise(t, s, notices.Notice{ID: "n2", UserID: U1, Kind: "k", Key: "x", Title: "t", LastSeenAt: at(0)})
		raise(t, s, notices.Notice{ID: "n3", UserID: U2, Kind: "k", Title: "t", LastSeenAt: at(0)})
		if err := s.DismissAllNotices(ctx, U1, at(1)); err != nil {
			t.Fatal(err)
		}
		if l1, l2 := listed(t, s, U1), listed(t, s, U2); len(l1) != 0 || len(l2) != 1 {
			t.Errorf("u1 = %+v, u2 = %+v", l1, l2)
		}
	})

	t.Run("text Postgres refuses is refused", func(t *testing.T) {
		s := open(t)
		for _, title := range []string{"a\x00b", "\xff"} {
			if _, _, err := s.RaiseNotice(ctx, notices.Notice{ID: "n", UserID: U1, Kind: "k", Title: title, LastSeenAt: at(0)}); err == nil {
				t.Errorf("title %q was stored", title)
			}
		}
	})

	t.Run("a dead context is an error everywhere", func(t *testing.T) {
		s := open(t)
		raise(t, s, notices.Notice{ID: "n1", UserID: U1, Kind: "k", Key: "x", Title: "t", LastSeenAt: at(0)})
		dead, cancel := context.WithCancel(ctx)
		cancel()
		calls := map[string]func() error{
			"RaiseNotice": func() error {
				_, _, err := s.RaiseNotice(dead, notices.Notice{ID: "n2", UserID: U1, Kind: "k", Title: "t", LastSeenAt: at(1)})
				return err
			},
			"ClearNotices":      func() error { return s.ClearNotices(dead, "x", at(1)) },
			"ListNotices":       func() error { _, err := s.ListNotices(dead, U1); return err },
			"GetNotice":         func() error { _, err := s.GetNotice(dead, "n1"); return err },
			"DismissNotice":     func() error { return s.DismissNotice(dead, "n1", at(1)) },
			"DismissAllNotices": func() error { return s.DismissAllNotices(dead, U1, at(1)) },
		}
		for name, call := range calls {
			if err := call(); err == nil {
				t.Errorf("%s with a dead context succeeded", name)
			}
		}
		if n, err := s.GetNotice(ctx, "n1"); err != nil || !n.ClearedAt.IsZero() || !n.DismissedAt.IsZero() {
			t.Errorf("n1 after dead calls = %+v, %v", n, err)
		}
	})
}

func raise(t *testing.T, s notices.Store, n notices.Notice) (notices.Notice, bool) {
	t.Helper()
	got, inserted, err := s.RaiseNotice(context.Background(), n)
	if err != nil {
		t.Fatalf("raise %+v: %v", n, err)
	}
	return got, inserted
}

func listed(t *testing.T, s notices.Store, userID string) []notices.Notice {
	t.Helper()
	list, err := s.ListNotices(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return list
}
