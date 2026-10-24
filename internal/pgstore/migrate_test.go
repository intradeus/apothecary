package pgstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"apothecary/internal/event"
	"apothecary/internal/run"
)

// freshSchemaDSN returns TEST_POSTGRES_DSN pointed at a new, empty schema
// (dropped on cleanup), so migrate runs against a blank database — open()'s
// shared schema is already migrated.
func freshSchemaDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping Postgres store tests")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("mig_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		_ = conn.Close(ctx)
	})
	switch {
	case strings.Contains(dsn, "://") && strings.Contains(dsn, "?"):
		return dsn + "&search_path=" + schema
	case strings.Contains(dsn, "://"):
		return dsn + "?search_path=" + schema
	default:
		return dsn + " search_path=" + schema
	}
}

func openDSN(t *testing.T, dsn string) *Store {
	t.Helper()
	s, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func ledger(t *testing.T, s *Store) []int {
	t.Helper()
	applied, err := s.appliedMigrations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for v := range applied {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// shipped is the ledger a fresh database ends up with: every embedded
// migration, in order.
func shipped(t *testing.T) []int {
	t.Helper()
	migs, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]int, 0, len(migs))
	for _, m := range migs {
		out = append(out, m.version)
	}
	return out
}

func TestMigrateFreshAndIdempotent(t *testing.T) {
	dsn := freshSchemaDSN(t)
	s := openDSN(t, dsn)
	if got, want := ledger(t, s), shipped(t); !slices.Equal(got, want) {
		t.Fatalf("ledger = %v, want %v", got, want)
	}
	s.Close()
	if got, want := ledger(t, openDSN(t, dsn)), shipped(t); !slices.Equal(got, want) {
		t.Fatalf("ledger after reopen = %v, want %v", got, want)
	}
}

func TestMigrateCollapsesPreSquashLedger(t *testing.T) {
	dsn := freshSchemaDSN(t)
	s := openDSN(t, dsn)
	// A pre-squash database: exactly the init schema (nothing a later
	// migration added) under a ledger of 1..preSquashHead.
	if _, err := s.pool.Exec(context.Background(), `ALTER TABLE runs DROP COLUMN view, DROP COLUMN dormant, DROP COLUMN rebuilding;
		DELETE FROM schema_migrations WHERE version > 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO schema_migrations (version) SELECT generate_series(2, $1)`, preSquashHead); err != nil {
		t.Fatal(err)
	}
	s.Close()
	// The ledger collapses to {1}, then whatever post-squash migrations exist apply.
	if got, want := ledger(t, openDSN(t, dsn)), shipped(t); !slices.Equal(got, want) {
		t.Fatalf("ledger = %v, want %v", got, want)
	}
}

func TestMigrateRefusesUnknownVersion(t *testing.T) {
	dsn := freshSchemaDSN(t)
	s := openDSN(t, dsn)
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO schema_migrations (version) VALUES (999)`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(context.Background(), dsn); err == nil || !strings.Contains(err.Error(), "999") {
		t.Fatalf("Open = %v, want refusal naming migration 999", err)
	}
}

// TestInitMigrationIsFrozen: 0001_init.sql is the squash of the whole
// pre-squash chain, and every database out there — collapsed or created
// since — has applied version 1. An edit to the file reaches NONE of them (the
// runner never re-runs an applied version), so the daemon boots against a
// schema the code no longer matches; runs.view did exactly that once. A
// schema change is a new migrations/NNNN_name.sql, written to be a no-op
// where a fresh database already has the result.
func TestInitMigrationIsFrozen(t *testing.T) {
	data, err := migrationsFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	const frozen = "fe4b5c7327c81f833fef5b66490ab28db7360e4cd2b2c3e9cf55c5645f35e6cc"
	if got := hex.EncodeToString(sha256sum(data)); got != frozen {
		t.Fatalf("0001_init.sql changed (sha256 %s, frozen %s): databases that already applied version 1 will never see the edit — put the change in a new migrations/NNNN_name.sql instead", got, frozen)
	}
}

func sha256sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

// A database stuck where an in-place edit of 0001 left it — ledger {1}, the
// runs table without the projection columns the edit added, runs on the log —
// catches up at the next boot: the catch-up migration adds the columns and
// the projections are rebuilt from events, so every existing run has its view
// again. (The shape the local stack was in when its daemon died on
// `column "view" does not exist`.)
func TestMigrateCatchesUpAnInitEditedInPlace(t *testing.T) {
	dsn := freshSchemaDSN(t)
	ctx := context.Background()
	s := openDSN(t, dsn)
	if _, err := s.Append(ctx, "r1", &event.RunCreated{TicketTitle: "T", FlowName: "f"}, &event.EnvRequested{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `ALTER TABLE runs DROP COLUMN view, DROP COLUMN dormant, DROP COLUMN rebuilding;
		DELETE FROM schema_migrations WHERE version > 1`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2 := openDSN(t, dsn)
	if got, want := ledger(t, s2), shipped(t); !slices.Equal(got, want) {
		t.Fatalf("ledger = %v, want %v", got, want)
	}
	views, err := s2.RunViews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].RunID != "r1" || views[0].TicketTitle != "T" || views[0].State != run.Provisioning {
		t.Fatalf("views after catch-up = %+v, want r1 rebuilt from its events", views)
	}
	// Idempotent: a reopen applies nothing and rebuilds nothing.
	s2.Close()
	if got, want := ledger(t, openDSN(t, dsn)), shipped(t); !slices.Equal(got, want) {
		t.Fatalf("ledger after reopen = %v, want %v", got, want)
	}
}

// The built-in flow "default" became "builder" (0007): a project that ran the
// built-in follows the rename, its binding and automation with it; a project
// with its own flow named "default" keeps everything pointing at that copy.
func TestMigrateRenamesTheBuiltinFlowReferences(t *testing.T) {
	dsn := freshSchemaDSN(t)
	ctx := context.Background()
	s := openDSN(t, dsn)
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO integrations (id, port, kind, name, created_at, updated_at) VALUES
			('r', 'repository', 'fake', 'r', now(), now()), ('v', 'provisioner', 'fake', 'v', now(), now());
		INSERT INTO projects (id, name, repo_account_id, provisioner_id, repo, default_flow, created_at, updated_at) VALUES
			('p1', 'ran-the-builtin', 'r', 'v', '{}', 'default', now(), now()),
			('p2', 'has-its-own', 'r', 'v', '{}', 'default', now(), now());
		INSERT INTO flows (project_id, name, yaml, hash, updated_at) VALUES ('p2', 'default', 'name: default', 'h', now());
		INSERT INTO flow_bindings (project_id, flow_name, params, updated_at) VALUES
			('p1', 'default', '{}', now()), ('p2', 'default', '{}', now());
		INSERT INTO automations (id, project_id, kind, name, flow, created_at, updated_at) VALUES
			('a1', 'p1', 'cron', 'a1', 'default', now(), now()), ('a2', 'p2', 'cron', 'a2', 'default', now(), now());
		DELETE FROM schema_migrations WHERE version = 7`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2 := openDSN(t, dsn)
	for _, q := range []struct{ sql, p1, p2 string }{
		{`SELECT default_flow FROM projects WHERE id = $1`, "builder", "default"},
		{`SELECT flow_name FROM flow_bindings WHERE project_id = $1`, "builder", "default"},
		{`SELECT flow FROM automations WHERE project_id = $1`, "builder", "default"},
	} {
		for id, want := range map[string]string{"p1": q.p1, "p2": q.p2} {
			var got string
			if err := s2.pool.QueryRow(ctx, q.sql, id).Scan(&got); err != nil || got != want {
				t.Errorf("%s [%s] = %q (%v), want %q", q.sql, id, got, err, want)
			}
		}
	}
}
