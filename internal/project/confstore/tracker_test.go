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

// Every type a tracker adds is told to the integration's owner, one notice
// per call, the first fill included; a type already listed is not.
func TestEnsureRemoteTypesTellsNotices(t *testing.T) {
	svc, _ := newSvc(t)
	rec := &recListNotices{}
	svc.Notices = rec
	ctx := context.Background()
	in, err := svc.CreateIntegration(ctx, Integration{UserID: "u1", Port: PortTicketSource, Kind: "jira", Name: "j"})
	if err != nil {
		t.Fatal(err)
	}
	first := []trackerport.RemoteType{{ID: "1", Name: "Bug"}, {ID: "2", Name: "Story"}}
	if _, err := svc.EnsureRemoteTypes(ctx, in.ID, first); err != nil {
		t.Fatal(err)
	}
	want := []string{`type "Bug": change type fix (guessed)`, `type "Story": change type feat (guessed)`}
	if len(rec.calls) != 1 || rec.calls[0].userID != "u1" || rec.calls[0].integrationID != in.ID ||
		rec.calls[0].name != "j" || !slices.Equal(rec.calls[0].added, want) {
		t.Fatalf("first fill = %+v", rec.calls)
	}
	more := append(first, trackerport.RemoteType{ID: "3", Name: "Chore"})
	if _, err := svc.EnsureRemoteTypes(ctx, in.ID, more); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 2 || !slices.Equal(rec.calls[1].added, []string{`type "Chore": change type chore (guessed)`}) {
		t.Fatalf("one more = %+v", rec.calls)
	}
	if _, err := svc.EnsureRemoteTypes(ctx, in.ID, more); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 2 {
		t.Errorf("a repeat told = %+v", rec.calls[2:])
	}
}

// A new account gets the deployment's provisioner once; one it has already
// (made by hand, or a second call) is kept.
func TestEnsureProvisioner(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	in, err := svc.EnsureProvisioner(ctx, "u1", "fake", "Kubernetes")
	if err != nil || in.ID == "" || in.Port != PortProvisioner || in.Kind != "fake" || in.Name != "Kubernetes" || in.UserID != "u1" {
		t.Fatalf("made = %+v, %v", in, err)
	}
	again, err := svc.EnsureProvisioner(ctx, "u1", "fake", "Kubernetes")
	if err != nil || again.ID != "" {
		t.Errorf("a second call made %+v, %v", again, err)
	}
	// Another user's provisioner is not this user's.
	if other, err := svc.EnsureProvisioner(ctx, "u2", "fake", "Kubernetes"); err != nil || other.ID == "" {
		t.Errorf("u2 = %+v, %v", other, err)
	}
	list, _ := svc.ListIntegrations(ctx, "u1")
	if len(list) != 1 {
		t.Errorf("u1 has %d integrations", len(list))
	}
}
