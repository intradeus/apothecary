package pgtest

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The tests run on the Postgres production runs: when deploy/ moves, so must
// Image, or they stop proving anything about it.
func TestImageIsTheOneDeployed(t *testing.T) {
	for _, f := range []string{"docker-compose.yaml", "docker-compose.local.yaml"} {
		data, err := os.ReadFile("../../../../deploy/" + f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "image: "+Image+"\n") {
			t.Errorf("deploy/%s does not run %s", f, Image)
		}
	}
}

func query(t *testing.T, dsn, sql string, out any) error {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	return conn.QueryRow(ctx, sql).Scan(out)
}

func TestSkipsUnlessEnabled(t *testing.T) {
	t.Setenv(EnableVar, "")
	var ran bool
	t.Run("off", func(t *testing.T) {
		defer func() { ran = true }()
		Empty(t)
		t.Error("ran without a server")
	})
	if !ran {
		t.Error("the subtest never ran")
	}
}

// Each test's database is its own, starts empty, and is gone afterwards.
func TestEmptyDatabasesAreSeparate(t *testing.T) {
	a := Empty(t)
	if a == Empty(t) {
		t.Fatal("two tests share a database")
	}
	var dropped string
	t.Run("sub", func(t *testing.T) { dropped = Empty(t) })
	if err := query(t, dropped, "SELECT 1", new(int)); err == nil {
		t.Error("a finished test's database is still there")
	}
	var tables int
	if err := query(t, a, "SELECT count(*) FROM pg_tables WHERE schemaname = 'public'", &tables); err != nil || tables != 0 {
		t.Errorf("tables = %d, %v", tables, err)
	}
}

// A clone holds what migrate wrote, and migrate runs once however many clones.
func TestMigratedClonesTheTemplate(t *testing.T) {
	var migrations int
	migrate := func(dsn string) error {
		migrations++
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		_, err = conn.Exec(ctx, "CREATE TABLE marker (id int)")
		return err
	}
	a, b := Migrated(t, migrate), Migrated(t, migrate)
	if a == b || migrations != 1 {
		t.Fatalf("clones equal=%v, migrations=%d", a == b, migrations)
	}
	var n int
	if err := query(t, a, "SELECT count(*) FROM marker", &n); err != nil {
		t.Error(err)
	}
}

// A failed migration is reported, never a half-built database to clone.
func TestPrepareReportsAFailedMigration(t *testing.T) {
	admin := server(t)
	boom := errors.New("boom")
	if name, err := prepare(admin, func(string) error { return boom }); name != "" || !errors.Is(err, boom) {
		t.Errorf("prepare = %q, %v", name, err)
	}
}
