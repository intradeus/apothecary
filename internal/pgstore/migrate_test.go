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

func TestMigrateFreshAndIdempotent(t *testing.T) {
	dsn := freshSchemaDSN(t)
	s := openDSN(t, dsn)
	if got := ledger(t, s); !slices.Equal(got, []int{1}) {
		t.Fatalf("ledger = %v, want [1]", got)
	}
	s.Close()
	if got := ledger(t, openDSN(t, dsn)); !slices.Equal(got, []int{1}) {
		t.Fatalf("ledger after reopen = %v, want [1]", got)
	}
}

func TestMigrateCollapsesPreSquashLedger(t *testing.T) {
	dsn := freshSchemaDSN(t)
	s := openDSN(t, dsn)
	// The pre-squash chain's end state is the init schema; only the ledger
	// differs.
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO schema_migrations (version) SELECT generate_series(2, $1)`, preSquashHead); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if got := ledger(t, openDSN(t, dsn)); !slices.Equal(got, []int{1}) {
		t.Fatalf("ledger = %v, want [1]", got)
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
