package confstore

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestProjectSecretsSealedNamedAndGoneWithTheProject(t *testing.T) {
	svc, mem := newSvc(t)
	p := seedProject(t, svc)
	ctx := context.Background()
	if _, err := svc.SetProjectSecret(ctx, p.ID, "figma-token", "figd_supersecret"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetProjectSecret(ctx, p.ID, "api_key", "k-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetProjectSecret(ctx, p.ID, "api_key", "k-2"); err != nil { // replace
		t.Fatal(err)
	}
	rows, _ := mem.ListProjectSecrets(ctx, p.ID)
	for _, r := range rows {
		if strings.Contains(string(r.Sealed), "supersecret") || strings.Contains(string(r.Sealed), "k-2") {
			t.Fatalf("secret %s is stored in plaintext", r.Name)
		}
	}
	list, err := svc.ListProjectSecrets(ctx, p.ID)
	if err != nil || len(list) != 2 || list[0].Name != "api_key" || list[1].Name != "figma-token" || list[0].UpdatedAt.IsZero() {
		t.Fatalf("list = %+v, %v", list, err)
	}
	open, err := svc.ProjectSecrets(ctx, p.ID)
	if err != nil || open["api_key"] != "k-2" || open["figma-token"] != "figd_supersecret" {
		t.Fatalf("opened = %v, %v", open, err)
	}
	if err := svc.DeleteProjectSecret(ctx, p.ID, "api_key"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteProjectSecret(ctx, p.ID, "api_key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
	if err := svc.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if rows, _ := mem.ListProjectSecrets(ctx, p.ID); len(rows) != 0 {
		t.Fatalf("secrets outlived their project: %v", rows)
	}
}

func TestProjectSecretRefusals(t *testing.T) {
	svc, _ := newSvc(t)
	p := seedProject(t, svc)
	ctx := context.Background()
	for _, c := range []struct{ name, value, want string }{
		{"Figma", "x", "lowercase slug"},
		{"1st", "x", "lowercase slug"},
		{"ok", "", "value is required"},
		{"ok", maskPrefix + "abcd", "masked placeholder"},
	} {
		if _, err := svc.SetProjectSecret(ctx, p.ID, c.name, c.value); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("SetProjectSecret(%q, %q) = %v, want ErrInvalid mentioning %q", c.name, c.value, err, c.want)
		}
	}
	if _, err := svc.SetProjectSecret(ctx, "nope", "ok", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project = %v, want ErrNotFound", err)
	}
}
