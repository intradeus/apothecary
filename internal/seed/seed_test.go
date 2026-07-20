package seed

import (
	"context"
	"os"
	"testing"

	"factory/internal/confstore"
	"factory/internal/secrets"
)

// The embedded seed MUST stay byte-identical to flows/v1.yaml — one
// canonical v1 flow, two copies, locked together.
func TestEmbeddedSeedMatchesRepoFlow(t *testing.T) {
	repo, err := os.ReadFile("../../flows/v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(repo) != V1YAML() {
		t.Fatal("internal/seed/v1.yaml drifted from flows/v1.yaml — copy it over")
	}
}

func TestSeedIsIdempotentAndRespectsEdits(t *testing.T) {
	key, _ := secrets.ParseKey(secrets.GenerateKey())
	svc := confstore.New(confstore.NewMem(), key)
	ctx := context.Background()

	if err := Flows(ctx, svc); err != nil {
		t.Fatal(err)
	}
	row, err := svc.GetFlow(ctx, "v1")
	if err != nil {
		t.Fatal(err)
	}

	// A user edit must survive re-seeding (seed only fills an empty table).
	edited := row.YAML + "# edited\n"
	if _, err := svc.SaveFlow(ctx, "v1", edited); err != nil {
		t.Fatal(err)
	}
	if err := Flows(ctx, svc); err != nil {
		t.Fatal(err)
	}
	row2, _ := svc.GetFlow(ctx, "v1")
	if row2.YAML != edited {
		t.Fatal("seeding overwrote a user-edited flow")
	}
	// Both versions remain retrievable (content-addressed history).
	if _, err := svc.FlowVersion(ctx, row.Hash); err != nil {
		t.Errorf("original version lost: %v", err)
	}
	if _, err := svc.FlowVersion(ctx, row2.Hash); err != nil {
		t.Errorf("edited version missing: %v", err)
	}
}
