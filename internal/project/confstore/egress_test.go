package confstore

import (
	"context"
	"slices"
	"testing"
)

func TestSaveEgressRule(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	p := seedProject(t, svc)
	p.Workspace.ExtraDomains = []string{"keep.example"}
	p.Workspace.BlockedDomains = []string{"b.example"}
	if _, err := svc.UpdateProject(ctx, p.ID, p); err != nil {
		t.Fatal(err)
	}
	lists := func() ([]string, []string) {
		got, err := svc.GetProject(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Workspace.ExtraDomains, got.Workspace.BlockedDomains
	}

	// Allowing lifts a block, normalises the host and never lists it twice.
	if err := svc.SaveEgressRule(ctx, p.ID, true, " A.example ", "b.example", "a.example", ""); err != nil {
		t.Fatal(err)
	}
	if allow, block := lists(); !slices.Equal(allow, []string{"keep.example", "a.example", "b.example"}) || len(block) != 0 {
		t.Fatalf("after allow: allow=%v block=%v", allow, block)
	}

	// Blocking moves it to the other list.
	if err := svc.SaveEgressRule(ctx, p.ID, false, "a.example"); err != nil {
		t.Fatal(err)
	}
	if allow, block := lists(); !slices.Equal(allow, []string{"keep.example", "b.example"}) || !slices.Equal(block, []string{"a.example"}) {
		t.Fatalf("after block: allow=%v block=%v", allow, block)
	}

	// Nothing to change writes nothing; an unknown project says so.
	before, _ := svc.GetProject(ctx, p.ID)
	if err := svc.SaveEgressRule(ctx, p.ID, false, "a.example"); err != nil {
		t.Fatal(err)
	}
	if after, _ := svc.GetProject(ctx, p.ID); !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Error("a no-op save rewrote the project")
	}
	if err := svc.SaveEgressRule(ctx, "nope", true, "x.example"); err == nil {
		t.Error("an unknown project was saved")
	}
}
