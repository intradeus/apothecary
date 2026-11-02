package confstore

import (
	"context"
	"slices"
	"testing"

	"apothecary/internal/ports/trackerport"
)

type listGrowth struct {
	userID, integrationID, name string
	added                       []string
}

type recListNotices struct{ calls []listGrowth }

func (r *recListNotices) TrackerListsGrew(_ context.Context, userID, integrationID, name string, added []string) {
	r.calls = append(r.calls, listGrowth{userID, integrationID, name, added})
}

// Every status a tracker adds is told to the integration's owner, one notice
// per call, the first fill included; a status already listed is not.
func TestEnsureRemoteStatusesTellsNotices(t *testing.T) {
	svc, _ := newSvc(t)
	rec := &recListNotices{}
	svc.Notices = rec
	ctx := context.Background()
	in, err := svc.CreateIntegration(ctx, Integration{UserID: "u1", Port: PortTicketSource, Kind: "jira", Name: "j"})
	if err != nil {
		t.Fatal(err)
	}
	first := []trackerport.RemoteStatus{
		{ID: "1", Name: "To Do", Category: trackerport.CategoryNew},
		{ID: "4", Name: "Done", Category: trackerport.CategoryDone},
	}
	if _, err := svc.EnsureRemoteStatuses(ctx, in.ID, first); err != nil {
		t.Fatal(err)
	}
	want := []string{`status "To Do": flow step todo`, `status "Done": flow step done, Done`}
	if len(rec.calls) != 1 || rec.calls[0].userID != "u1" || rec.calls[0].integrationID != in.ID ||
		rec.calls[0].name != "j" || !slices.Equal(rec.calls[0].added, want) {
		t.Fatalf("first fill = %+v", rec.calls)
	}
	qa := append(first, trackerport.RemoteStatus{ID: "3", Name: "In QA", Category: trackerport.CategoryIndeterminate})
	if _, err := svc.EnsureRemoteStatuses(ctx, in.ID, qa); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 2 || !slices.Equal(rec.calls[1].added, []string{`status "In QA": no flow step`}) {
		t.Fatalf("one more = %+v", rec.calls)
	}
	if _, err := svc.EnsureRemoteStatuses(ctx, in.ID, qa); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 2 {
		t.Errorf("a repeat told = %+v", rec.calls[2:])
	}
}
