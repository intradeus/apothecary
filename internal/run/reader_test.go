package run

import (
	"context"
	"errors"
	"strings"
	"testing"

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
