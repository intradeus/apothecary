package pgstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/adapters/pgstore/pgtest"
	"apothecary/internal/engine/ask"
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

// shipped is the versions this binary ships, in order.
func shipped(t *testing.T) []int {
	t.Helper()
	migs, err := migrations(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, m := range migs {
		out = append(out, m.version)
	}
	return out
}

func TestMigrateFreshAndIdempotent(t *testing.T) {
	dsn := pgtest.Empty(t)
	s := openDSN(t, dsn)
	want := shipped(t)
	if want[0] != 1 {
		t.Fatalf("shipped = %v, want the init first", want)
	}
	if got := ledger(t, s); !slices.Equal(got, want) {
		t.Fatalf("ledger = %v, want every shipped migration %v", got, want)
	}
	s.Close()
	if got := ledger(t, openDSN(t, dsn)); !slices.Equal(got, want) {
		t.Fatalf("ledger after reopen = %v, want %v", got, want)
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

// A later release's migration applies once, on top of what shipped.
func TestMigrateAppliesALaterMigrationOnce(t *testing.T) {
	dsn := pgtest.Empty(t)
	openDSN(t, dsn).Close()
	have := shipped(t)
	next := have[len(have)-1] + 1
	src := withMigrations(t, map[string]string{fmt.Sprintf("%04d_widgets.sql", next): `CREATE TABLE widgets (id int);`})
	want := append(slices.Clone(have), next)
	s := openSrc(t, dsn, src)
	if got := ledger(t, s); !slices.Equal(got, want) {
		t.Fatalf("ledger = %v, want %v", got, want)
	}
	s.Close()
	if got := ledger(t, openSrc(t, dsn, src)); !slices.Equal(got, want) {
		t.Fatalf("ledger after reopen = %v", got)
	}
}

// 0002_token_idle: a token without an inactivity window is stored as NULL,
// what every token made before the column has (the round trip itself is the
// auth contract's).
func TestTokenIdleSecondsColumn(t *testing.T) {
	s := openDSN(t, pgtest.Empty(t))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := s.CreateUser(ctx, auth.User{ID: "u1", Handle: "u1", Email: "u1@x", Name: "u1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateToken(ctx, auth.Token{ID: "t1", UserID: "u1", Name: "ui", Hash: "h1", Prefix: "apo_1", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var null bool
	if err := s.pool.QueryRow(ctx, `SELECT idle_seconds IS NULL FROM api_tokens WHERE id='t1'`).Scan(&null); err != nil || !null {
		t.Errorf("no window stored as NULL = %v, %v", null, err)
	}
}

// upTo is the shipped migrations up to version last: what an older release
// shipped.
func upTo(t *testing.T, last int) fs.FS {
	t.Helper()
	src := fstest.MapFS{}
	migs, err := migrations(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migs {
		if m.version > last {
			continue
		}
		data, err := fs.ReadFile(migrationsFS, "migrations/"+m.name)
		if err != nil {
			t.Fatal(err)
		}
		src["migrations/"+m.name] = &fstest.MapFile{Data: data}
	}
	return src
}

// 0003_ask_threads: what a run's Ask pane held before becomes its first
// thread — a run with a thread row and messages, one with messages only (a
// first answer that failed before its thread was saved), one with a thread
// and no messages — and every message names it, its seq untouched.
func TestAskThreadsMigration(t *testing.T) {
	dsn := pgtest.Empty(t)
	ctx := context.Background()
	old := openSrc(t, dsn, upTo(t, 2))
	if _, err := old.pool.Exec(ctx, `
		INSERT INTO ask_threads (run_id, session_id, model, updated_at) VALUES
		  ('ra', 's1', 'fake/one', '2026-01-03T00:00:00Z'),
		  ('rc', '', 'fake/three', '2026-03-03T00:00:00Z');
		INSERT INTO ask_messages (run_id, seq, id, role, text, at) VALUES
		  ('ra', 1, 'a1', 'question', 'why?', '2026-01-01T10:05:00Z'),
		  ('ra', 2, 'a2', 'answer', 'because', '2026-01-01T10:00:00Z'),
		  ('rb', 1, 'b1', 'question', 'how?', '2026-02-01T00:00:00Z'),
		  ('rb', 2, 'b2', 'answer', '', '2026-02-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s := openDSN(t, dsn)
	day := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		run, session, model string
		created, updated    time.Time
		msgs                []string
	}{
		{"ra", "s1", "fake/one", day("2026-01-01T10:00:00Z"), day("2026-01-03T00:00:00Z"), []string{"a1", "a2"}},
		{"rb", "", "", day("2026-02-01T00:00:00Z"), day("2026-02-02T00:00:00Z"), []string{"b1", "b2"}},
		{"rc", "", "fake/three", day("2026-03-03T00:00:00Z"), day("2026-03-03T00:00:00Z"), nil},
	} {
		ths, err := s.AskThreads(ctx, c.run)
		if err != nil || len(ths) != 1 {
			t.Fatalf("%s threads = %+v, %v", c.run, ths, err)
		}
		th := ths[0]
		if th.ID == "" || th.SessionID != c.session || th.Model != c.model || !th.CreatedAt.Equal(c.created) || !th.UpdatedAt.Equal(c.updated) {
			t.Errorf("%s thread = %+v", c.run, th)
		}
		msgs, err := s.AskMessages(ctx, c.run, 0)
		if err != nil || len(msgs) != len(c.msgs) {
			t.Fatalf("%s messages = %+v, %v", c.run, msgs, err)
		}
		for i, m := range msgs {
			if m.ID != c.msgs[i] || m.Seq != int64(i+1) || m.ThreadID != th.ID {
				t.Errorf("%s message %d = %+v", c.run, i, m)
			}
		}
	}
	// The run's next thread and message sit beside the migrated ones.
	if err := s.SaveAskThread(ctx, ask.Thread{RunID: "ra", ID: "next"}); err != nil {
		t.Fatal(err)
	}
	if m, err := s.AppendAskMessage(ctx, ask.Message{RunID: "ra", ThreadID: "next", Role: ask.RoleQuestion}); err != nil || m.Seq != 3 {
		t.Errorf("next message = %+v, %v", m, err)
	}
	if ths, err := s.AskThreads(ctx, "ra"); err != nil || len(ths) != 2 || ths[1].ID != "next" {
		t.Errorf("threads = %+v, %v", ths, err)
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
