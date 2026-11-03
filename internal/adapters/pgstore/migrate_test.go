package pgstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"apothecary/internal/adapters/pgstore/pgtest"
)

func openDSN(t *testing.T, dsn string) *Store {
	t.Helper()
	s, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// openSrc is openDSN applying src's migrations.
func openSrc(t *testing.T, dsn string, src fs.FS) *Store {
	t.Helper()
	s, err := openWith(context.Background(), dsn, src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// withMigrations is the shipped migrations plus extra (file name → SQL): what
// a later release ships.
func withMigrations(t *testing.T, extra map[string]string) fs.FS {
	t.Helper()
	src := fstest.MapFS{}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := fs.ReadFile(migrationsFS, "migrations/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		src["migrations/"+e.Name()] = &fstest.MapFile{Data: data}
	}
	for name, sql := range extra {
		src["migrations/"+name] = &fstest.MapFile{Data: []byte(sql)}
	}
	return src
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
	dsn := pgtest.Empty(t)
	s := openDSN(t, dsn)
	if got := ledger(t, s); !slices.Equal(got, []int{1}) {
		t.Fatalf("ledger = %v, want the init alone", got)
	}
	s.Close()
	if got := ledger(t, openDSN(t, dsn)); !slices.Equal(got, []int{1}) {
		t.Fatalf("ledger after reopen = %v, want the init alone", got)
	}
}

// A database the development chain built holds versions past the init that
// this binary does not ship: it is refused, never run against.
func TestMigrateRefusesUnknownVersion(t *testing.T) {
	dsn := pgtest.Empty(t)
	s := openDSN(t, dsn)
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO schema_migrations (version) VALUES (23)`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(context.Background(), dsn); err == nil || !strings.Contains(err.Error(), "migration 23") {
		t.Fatalf("Open = %v, want refusal naming migration 23", err)
	}
}

// A later release's migration applies once, on top of the init.
func TestMigrateAppliesALaterMigrationOnce(t *testing.T) {
	dsn := pgtest.Empty(t)
	openDSN(t, dsn).Close()
	src := withMigrations(t, map[string]string{"0002_widgets.sql": `CREATE TABLE widgets (id int);`})
	s := openSrc(t, dsn, src)
	if got := ledger(t, s); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("ledger = %v, want the init and 2", got)
	}
	s.Close()
	if got := ledger(t, openSrc(t, dsn, src)); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("ledger after reopen = %v", got)
	}
}

// TestInitMigrationIsFrozen: 0001_init.sql is the v1.0.0 schema, and every
// database out there has applied version 1. An edit to the file reaches NONE
// of them (the runner never re-runs an applied version), so the daemon boots
// against a schema the code no longer matches. A schema change is a new
// migrations/NNNN_name.sql.
func TestInitMigrationIsFrozen(t *testing.T) {
	data, err := migrationsFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	const frozen = "1a182367b2b95a66d724c88e909476a0a7375fc282b4f81be99ea2bb852fb471"
	if got := hex.EncodeToString(sha256sum(data)); got != frozen {
		t.Fatalf("0001_init.sql changed (sha256 %s, frozen %s): databases that already applied version 1 will never see the edit — put the change in a new migrations/NNNN_name.sql instead", got, frozen)
	}
}

func sha256sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}
