package confstore

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"apothecary/internal/account/authz"
)

// Sharing a workshop: a member is one of three roles and never its owner;
// sharing again changes the role and keeps when it was first shared; and
// what authz reads (the owner and one's role, every workshop one reaches)
// comes from the same rows.
func TestMembers(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	p := seedProject(t, svc)

	if _, err := svc.PutMember(ctx, Member{ProjectID: p.ID, UserID: "bob", Role: "owner"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("role owner = %v", err)
	}
	if _, err := svc.PutMember(ctx, Member{ProjectID: p.ID, UserID: p.UserID, Role: MemberViewer}); !errors.Is(err, ErrInvalid) {
		t.Errorf("the owner as a member = %v", err)
	}
	if _, err := svc.PutMember(ctx, Member{ProjectID: "nope", UserID: "bob", Role: MemberViewer}); !errors.Is(err, ErrNotFound) {
		t.Errorf("no project = %v", err)
	}
	first, err := svc.PutMember(ctx, Member{ProjectID: p.ID, UserID: "bob", Role: MemberViewer})
	if err != nil || first.CreatedAt.IsZero() {
		t.Fatalf("share = %+v, %v", first, err)
	}
	again, err := svc.PutMember(ctx, Member{ProjectID: p.ID, UserID: "bob", Role: MemberMaintainer})
	if err != nil || again.Role != MemberMaintainer || !again.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("share again = %+v, %v", again, err)
	}
	if m, err := svc.GetMember(ctx, p.ID, "bob"); err != nil || m.Role != MemberMaintainer {
		t.Errorf("get = %+v, %v", m, err)
	}
	if ms, err := svc.ListMembers(ctx, p.ID); err != nil || len(ms) != 1 {
		t.Errorf("list = %+v, %v", ms, err)
	}

	if owner, role, err := svc.ProjectAccess(ctx, p.ID, "bob"); err != nil || owner != p.UserID || role != MemberMaintainer {
		t.Errorf("access = %q %q %v", owner, role, err)
	}
	if owner, role, err := svc.Workshop(ctx, p.ID, "bob"); err != nil || owner != p.UserID || role != authz.RoleMaintainer {
		t.Errorf("workshop = %q %q %v", owner, role, err)
	}
	if owner, role, err := svc.Workshop(ctx, "nope", "bob"); err != nil || owner != "" || role != "" {
		t.Errorf("a missing workshop = %q %q %v", owner, role, err)
	}
	reach, err := svc.ProjectsOf(ctx, "bob")
	if err != nil || len(reach) != 1 || reach[0].Role != MemberMaintainer {
		t.Errorf("reach = %+v, %v", reach, err)
	}
	ws, err := svc.Workshops(ctx, "bob")
	if want := []authz.Membership{{ProjectID: p.ID, Owner: p.UserID, Role: authz.RoleMaintainer}}; err != nil || !reflect.DeepEqual(ws, want) {
		t.Errorf("workshops = %+v, %v", ws, err)
	}
	if ps, err := svc.ListVisibleProjects(ctx, "", []string{p.ID}); err != nil || len(ps) != 1 {
		t.Errorf("visible = %+v, %v", ps, err)
	}

	if err := svc.DeleteMember(ctx, p.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if ws, err := svc.Workshops(ctx, "bob"); err != nil || len(ws) != 0 {
		t.Errorf("after leaving = %+v, %v", ws, err)
	}
}

// failingReach is a store whose reach and access reads fail.
type failingReach struct {
	DB
	err error
}

func (f failingReach) ProjectsOf(context.Context, string) ([]Reach, error) { return nil, f.err }
func (f failingReach) ProjectAccess(context.Context, string, string) (string, string, error) {
	return "", "", f.err
}

func TestFactsSayWhenTheStoreFails(t *testing.T) {
	boom := errors.New("db down")
	svc := &Service{db: failingReach{DB: NewMem(), err: boom}}
	if _, err := svc.Workshops(context.Background(), "u"); !errors.Is(err, boom) {
		t.Errorf("workshops = %v", err)
	}
	if _, _, err := svc.Workshop(context.Background(), "p", "u"); !errors.Is(err, boom) {
		t.Errorf("workshop = %v", err)
	}
}
