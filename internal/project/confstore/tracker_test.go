package confstore

import (
	"context"
	"errors"
	"slices"
	"strings"
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

// The instance gets the deployment's provisioner once; a second call, or a
// person's own provisioner, leaves it alone.
func TestEnsureProvisioner(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	if _, err := svc.InstanceProvisioner(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("none yet = %v", err)
	}
	// A person's own provisioner is not the instance's.
	if _, err := svc.CreateIntegration(ctx, Integration{UserID: "u1", Port: PortProvisioner, Kind: "fake", Name: "mine"}); err != nil {
		t.Fatal(err)
	}
	in, err := svc.EnsureProvisioner(ctx, "fake", "Kubernetes")
	if err != nil || in.ID == "" || in.Port != PortProvisioner || in.Kind != "fake" || in.Name != "Kubernetes" || in.UserID != InstanceOwner {
		t.Fatalf("made = %+v, %v", in, err)
	}
	again, err := svc.EnsureProvisioner(ctx, "fake", "Kubernetes")
	if err != nil || again.ID != "" {
		t.Errorf("a second call made %+v, %v", again, err)
	}
	if got, err := svc.InstanceProvisioner(ctx); err != nil || got.ID != in.ID {
		t.Errorf("instance provisioner = %+v, %v", got, err)
	}
	// No one lists it among their own.
	list, _ := svc.ListIntegrations(ctx, "u1")
	if len(list) != 1 || list[0].Name != "mine" {
		t.Errorf("u1 lists %+v", list)
	}
}

// A workshop builds on the instance's provisioner unless it names another
// of its owner's; an edit that names none keeps the one it had.
func TestProjectProvisioner(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	mk := func(owner, port, name string) Integration {
		in, err := svc.CreateIntegration(ctx, Integration{UserID: owner, Port: port, Kind: "fake", Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	repo := mk("u1", PortRepository, "gl")
	harness := mk("u1", PortHarness, "oc")
	draft := Project{
		UserID: "u1", Name: "p1", RepoAccountID: repo.ID, HarnessIDs: []string{harness.ID},
		Repo: RepoConfig{URL: "https://git.test/g/p.git"}, DefaultFlow: "builder",
	}
	if _, err := svc.CreateProject(ctx, draft); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "System → Provisioner") {
		t.Fatalf("no instance provisioner = %v", err)
	}
	inst, err := svc.EnsureProvisioner(ctx, "fake", "Kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.CreateProject(ctx, draft)
	if err != nil || p.ProvisionerID != inst.ID {
		t.Fatalf("created on %q, %v; want the instance's %q", p.ProvisionerID, err, inst.ID)
	}

	own := mk("u1", PortProvisioner, "vm")
	p.ProvisionerID = own.ID
	if p, err = svc.UpdateProject(ctx, p.ID, p); err != nil || p.ProvisionerID != own.ID {
		t.Fatalf("own provisioner = %q, %v", p.ProvisionerID, err)
	}
	p.ProvisionerID = ""
	if p, err = svc.UpdateProject(ctx, p.ID, p); err != nil || p.ProvisionerID != own.ID {
		t.Errorf("an edit naming none kept %q, %v", p.ProvisionerID, err)
	}
	p.ProvisionerID = mk("u2", PortProvisioner, "theirs").ID
	if _, err := svc.UpdateProject(ctx, p.ID, p); !errors.Is(err, ErrInvalid) {
		t.Errorf("someone else's provisioner = %v", err)
	}
}
