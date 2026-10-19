package pgstore

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
	// A pre-squash database: the init schema under a ledger of
	// 1..preSquashHead.
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
