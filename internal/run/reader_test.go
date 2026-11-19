package run

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/run/event"
)

func TestReaderViewAndAll(t *testing.T) {
	ctx := context.Background()
	st := event.NewMemStore()
	if _, err := st.Append(ctx, "r1", &event.RunCreated{ProjectID: "p"}); err != nil {
		t.Fatal(err)
	}
	r := &Reader{Store: st}
	v, err := r.View(ctx, "r1")
	if err != nil || v.RunID != "r1" || v.ProjectID != "p" {
		t.Fatalf("View = %+v, %v", v, err)
	}
	if _, err := r.View(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("View(missing) err = %v", err)
	}
	all, err := r.All(ctx)
	if err != nil || len(all) != 1 || all[0].RunID != "r1" {
		t.Fatalf("All = %+v, %v", all, err)
	}
}

// failingStore refuses what it is told to refuse.
type failingStore struct {
	event.Store
	events, ids error
	emptyRun    string
}

func (s *failingStore) Events(ctx context.Context, runID string, after int64) ([]event.Event, error) {
	if s.events != nil {
		return nil, s.events
	}
	if runID == s.emptyRun {
		return nil, nil
	}
	return s.Store.Events(ctx, runID, after)
}

func (s *failingStore) RunIDs(ctx context.Context) ([]string, error) {
	if s.ids != nil {
		return nil, s.ids
	}
	return s.Store.RunIDs(ctx)
}

func TestReaderReportsWhatTheStoreCouldNotDo(t *testing.T) {
	ctx := context.Background()
	st := event.NewMemStore()
	if _, err := st.Append(ctx, "r1", &event.RunCreated{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Append(ctx, "r2", &event.RunCreated{}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("db down")

	if _, err := (&Reader{Store: &failingStore{Store: st, events: boom}}).View(ctx, "r1"); !errors.Is(err, boom) {
		t.Errorf("View = %v", err)
	}
	if _, err := (&Reader{Store: &failingStore{Store: st, ids: boom}}).All(ctx); !errors.Is(err, boom) {
		t.Errorf("All, listing = %v", err)
	}
	if _, err := (&Reader{Store: &failingStore{Store: st, events: boom}}).All(ctx); !errors.Is(err, boom) {
		t.Errorf("All, reading = %v", err)
	}
	// A run id with no events yet is no run.
	all, err := (&Reader{Store: &failingStore{Store: st, emptyRun: "r2"}}).All(ctx)
	if err != nil || len(all) != 1 || all[0].RunID != "r1" {
		t.Errorf("All = %+v, %v", all, err)
	}
}

// listerStore keeps a projection list of its own: List must use it.
type listerStore struct {
	event.Store
	got ListFilter
}

func (s *listerStore) ListRunViews(_ context.Context, f ListFilter) ([]View, error) {
	s.got = f
	return []View{{RunID: "from-store"}}, nil
}

func TestReaderListScopesToTheOwnerNewestFirst(t *testing.T) {
	ctx := context.Background()
	st := event.NewMemStore()
	for _, r := range []struct {
		id string
		c  event.RunCreated
	}{
		{"a1", event.RunCreated{UserID: "u1", ProjectID: "p1", TicketID: "t1"}},
		{"b1", event.RunCreated{UserID: "u2", ProjectID: "p1", TicketID: "t1"}},
		{"a2", event.RunCreated{UserID: "u1", ProjectID: "p2", TicketID: "t2"}},
		{"a3", event.RunCreated{UserID: "u1", ProjectID: "p1", TicketID: "t2"}},
	} {
		if _, err := st.Append(ctx, r.id, &r.c); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond) // distinct created_at
	}
	ids := func(f ListFilter) []string {
		t.Helper()
		vs, err := (&Reader{Store: st}).List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, v := range vs {
			out = append(out, v.RunID)
		}
		return out
	}
	for _, c := range []struct {
		f    ListFilter
		want []string
	}{
		{ListFilter{Seen: Seen{UserID: "u1"}}, []string{"a3", "a2", "a1"}},
		{ListFilter{Seen: Seen{UserID: "u1"}, ProjectID: "p1"}, []string{"a3", "a1"}},
		{ListFilter{Seen: Seen{UserID: "u1"}, TicketID: "t2"}, []string{"a3", "a2"}},
		{ListFilter{Seen: Seen{UserID: "u2"}}, []string{"b1"}},
		{ListFilter{Seen: Seen{UserID: "nobody"}}, []string{}},
		// A workshop shared with u2: its runs join u2's own, whoever's.
		{ListFilter{Seen: Seen{UserID: "u2", ProjectIDs: []string{"p2"}}}, []string{"a2", "b1"}},
		// A token limited to p2: no own runs by owner, only the workshop's.
		{ListFilter{Seen: Seen{ProjectIDs: []string{"p2"}}}, []string{"a2"}},
		{ListFilter{}, []string{}},
	} {
		if got := ids(c.f); !slices.Equal(got, c.want) {
			t.Errorf("List(%+v) = %v, want %v", c.f, got, c.want)
		}
	}
	if _, err := (&Reader{Store: &failingStore{Store: st, ids: errors.New("db down")}}).List(ctx, ListFilter{Seen: Seen{UserID: "u1"}}); err == nil {
		t.Error("List on a failing store = nil error")
	}
	ls := &listerStore{Store: st}
	want := ListFilter{Seen: Seen{UserID: "u1", ProjectIDs: []string{"q"}}, ProjectID: "p"}
	vs, err := (&Reader{Store: ls}).List(ctx, want)
	if err != nil || len(vs) != 1 || vs[0].RunID != "from-store" || !reflect.DeepEqual(ls.got, want) {
		t.Errorf("List via ViewLister = %+v, %v (filter %+v)", vs, err, ls.got)
	}
}
