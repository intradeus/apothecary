package pgstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/adapters/pgstore/pgtest"
	"apothecary/internal/engine/ask"
	"apothecary/internal/project/confstore"
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

// The committed migrations.sum is what makes two branches that each add a
// migration conflict in git; one that fell behind the directory protects
// nothing. Regenerate with `task migrations:sum`.
func TestCommittedMigrationsSumIsCurrent(t *testing.T) {
	have, err := os.ReadFile(filepath.Join("migrations", "migrations.sum"))
	if err != nil {
		t.Fatalf("%v — run `task migrations:sum`", err)
	}
	want, err := MigrationsSum()
	if err != nil {
		t.Fatal(err)
	}
	if string(have) != string(want) {
		t.Error("migrations/migrations.sum is out of date with the migrations — run `task migrations:sum`")
	}
}

// The total line is the one every change to the migrations rewrites: adding
// one, renaming or renumbering it, editing it. Two branches doing any of
// these both change that line, which is the conflict.
func TestMigrationsSumTotalChangesWithEveryMigration(t *testing.T) {
	total := func(src fs.FS) string {
		t.Helper()
		sum, err := migrationsSum(src)
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.Lines(string(sum)) {
			if strings.HasPrefix(line, "total ") {
				return line
			}
		}
		t.Fatalf("no total line in %s", sum)
		return ""
	}
	base := total(withMigrations(t, nil))
	seen := map[string]string{base: "the shipped migrations"}
	for what, extra := range map[string]map[string]string{
		"one added":            {"0099_widgets.sql": "CREATE TABLE widgets (id int);"},
		"another added":        {"0099_gadgets.sql": "CREATE TABLE gadgets (id int);"},
		"one renumbered":       {"0100_widgets.sql": "CREATE TABLE widgets (id int);"},
		"one edited":           {"0099_widgets.sql": "CREATE TABLE widgets (id bigint);"},
		"a shipped one edited": {"0001_init.sql": "-- squashed again"},
	} {
		got := total(withMigrations(t, extra))
		if prev, dup := seen[got]; dup {
			t.Errorf("%s has the same total as %s", what, prev)
		}
		seen[got] = what
	}
	if _, err := migrationsSum(fstest.MapFS{"migrations/widgets.sql": {}}); err == nil {
		t.Error("an unnumbered migration summed")
	}
}

// A database that applied a migration under one number refuses a binary that
// ships another file under it: the branch-renumbering case, where the
// database would otherwise skip the new file and run its own migration again.
func TestMigrateRefusesARenumberedMigration(t *testing.T) {
	dsn := pgtest.Empty(t)
	ctx := context.Background()
	openSrc(t, dsn, withMigrations(t, map[string]string{"0099_widgets.sql": `CREATE TABLE widgets (id int);`})).Close()
	renumbered := withMigrations(t, map[string]string{
		"0099_gadgets.sql": `CREATE TABLE gadgets (id int);`,
		"0100_widgets.sql": `CREATE TABLE widgets (id int);`,
	})
	_, err := openWith(ctx, dsn, renumbered)
	if err == nil || !strings.Contains(err.Error(), "applied 0099_widgets.sql as migration 99, where this apothecaryd ships 0099_gadgets.sql") {
		t.Fatalf("Open = %v, want a refusal naming both files", err)
	}
	var gadgets *string
	if err := openRaw(t, dsn).QueryRow(ctx, `SELECT to_regclass('gadgets')::text`).Scan(&gadgets); err != nil || gadgets != nil {
		t.Errorf("a refused database ran a pending migration: %v, %v", gadgets, err)
	}
}

// An applied migration whose file changed afterwards is refused: the edit
// never reached this database, so its schema is not the one the code expects.
func TestMigrateRefusesAnEditedMigration(t *testing.T) {
	dsn := pgtest.Empty(t)
	openSrc(t, dsn, withMigrations(t, map[string]string{"0099_widgets.sql": `CREATE TABLE widgets (id int);`})).Close()
	_, err := openWith(context.Background(), dsn, withMigrations(t, map[string]string{"0099_widgets.sql": `CREATE TABLE widgets (id bigint);`}))
	if err == nil || !strings.Contains(err.Error(), "migration 0099_widgets.sql changed after this database applied it") {
		t.Fatalf("Open = %v, want a refusal naming the edited file", err)
	}
}

// A ledger from before names and checksums were kept takes the shipped
// files' at the next boot, and is held to them from then on.
func TestMigrateAdoptsALedgerWithoutChecksums(t *testing.T) {
	dsn := pgtest.Empty(t)
	ctx := context.Background()
	openDSN(t, dsn).Close()
	if _, err := openRaw(t, dsn).Exec(ctx, `ALTER TABLE schema_migrations DROP COLUMN name, DROP COLUMN checksum`); err != nil {
		t.Fatal(err)
	}
	s := openDSN(t, dsn)
	var name, sum string
	if err := s.pool.QueryRow(ctx, `SELECT name, checksum FROM schema_migrations WHERE version = 1`).Scan(&name, &sum); err != nil {
		t.Fatal(err)
	}
	initSQL, err := migrationsFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if name != "0001_init.sql" || sum != checksum(initSQL) {
		t.Errorf("adopted = %s %s", name, sum)
	}
	var unnamed int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE name IS NULL OR checksum IS NULL`).Scan(&unnamed); err != nil || unnamed != 0 {
		t.Errorf("rows left without a name or checksum = %d, %v", unnamed, err)
	}
	s.Close()
	if _, err := openWith(ctx, dsn, withMigrations(t, map[string]string{"0001_init.sql": `-- squashed again`})); err == nil || !strings.Contains(err.Error(), "0001_init.sql changed") {
		t.Fatalf("Open = %v, want the adopted checksum held", err)
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

// 0006_live_tickets: each workshop gets one ticket source — the single
// remote tracker its tickets and tracker_query automations name, else the
// owner's internal tracker (made, with its stock lists, when missing) — and
// the mirror goes: a tracker_query automation polling another tracker is
// disabled saying why, so is a cron automation pinned to a ticket of another
// source than its workshop's, sourceless tickets become the internal
// tracker's, remote records get a remote id unique per source (placeholders
// for an empty one and for later duplicates), and remote statuses, flow
// steps and remote comments are dropped.
func TestLiveTicketsMigration(t *testing.T) {
	dsn := pgtest.Empty(t)
	ctx := context.Background()
	old := openSrc(t, dsn, upTo(t, 5))
	if _, err := old.pool.Exec(ctx, `
		INSERT INTO integrations (id, port, kind, name, created_at, updated_at, user_id) VALUES
		  ('repo1', 'repository', 'github', 'gh', now(), now(), 'u1'),
		  ('jira1', 'ticketsource', 'jira', 'Apothecary tickets', now(), now(), 'u1'),
		  ('gh1', 'ticketsource', 'github_issues', 'GH issues', now(), now(), 'u1'),
		  ('repo2', 'repository', 'github', 'gh', now(), now(), 'u2'),
		  ('internal-u2', 'ticketsource', 'internal', 'Apothecary tickets', now(), now(), 'u2');
		INSERT INTO tracker_statuses (id, integration_id, remote_id, name, remote_category, position, is_done, flow_step) VALUES
		  ('js1', 'jira1', '10', 'Open', 'new', 0, false, 'todo'),
		  ('internal-u2-todo', 'internal-u2', 'internal-u2-todo', 'To do', 'new', 0, false, 'todo');
		INSERT INTO projects (id, name, repo_account_id, provisioner_id, repo, default_flow, created_at, updated_at, user_id) VALUES
		  ('p-single', 'single', 'repo1', 'repo1', '{}', 'builder', now(), now(), 'u1'),
		  ('p-two', 'two', 'repo1', 'repo1', '{}', 'builder', now(), now(), 'u1'),
		  ('p-none', 'none', 'repo1', 'repo1', '{}', 'builder', now(), now(), 'u1'),
		  ('p-u2', 'theirs', 'repo2', 'repo2', '{}', 'builder', now(), now(), 'u2');
		INSERT INTO tickets (id, project_id, title, source_id, source_kind, source_key, created_at, updated_at, user_id, remote_id, status_id, sync_state) VALUES
		  ('t1', 'p-single', 'synced', 'jira1', 'jira', 'J-1', '2026-01-01T00:00:00Z', now(), 'u1', '100', 'js1', 'ok'),
		  ('t2', 'p-two', 'same issue, moved', 'jira1', 'jira', 'K-1', '2026-01-02T00:00:00Z', now(), 'u1', '100', NULL, 'ok'),
		  ('t3', 'p-two', 'never synced', 'gh1', 'github_issues', '#3', '2026-01-03T00:00:00Z', now(), 'u1', '', NULL, ''),
		  ('t4', 'p-none', 'hand-written', '', '', '', '2026-01-04T00:00:00Z', now(), 'u1', '', NULL, ''),
		  ('t5', 'p-none', 'merge request', '', '', 'https://x.local/mr/1', '2026-01-05T00:00:00Z', now(), 'u1', '', NULL, ''),
		  ('t6', 'p-u2', 'theirs', '', '', '', '2026-01-06T00:00:00Z', now(), 'u2', '', 'internal-u2-todo', ''),
		  ('t8', 'p-none', 'hand-written too', '', '', '', '2026-01-08T00:00:00Z', now(), 'u1', '', NULL, '');
		INSERT INTO ticket_comments (id, ticket_id, remote_id, body_md, created_at, updated_at, origin) VALUES
		  ('c1', 't1', 'r1', 'mirrored', now(), now(), 'remote'),
		  ('c2', 't1', '', 'posted by a run', now(), now(), 'apothecary'),
		  ('c3', 't4', '', 'typed', now(), now(), 'user'),
		  ('c4', 't4', 'r9', 'stray', now(), now(), 'remote');
		INSERT INTO automations (id, project_id, name, kind, source_id, query, schedule, ticket_id, flow, created_at, updated_at) VALUES
		  ('a1', 'p-single', 'poll', 'tracker_query', 'jira1', 'q', '', '', 'builder', now(), now()),
		  ('a2', 'p-two', 'poll', 'tracker_query', 'gh1', 'q', '', '', 'builder', now(), now()),
		  ('a3', 'p-two', 'nightly', 'cron', '', '', '0 3 * * *', '', 'builder', now(), now()),
		  ('a4', 'p-single', 'pinned elsewhere', 'cron', '', '', '0 3 * * *', 't4', 'builder', now(), now()),
		  ('a5', 'p-single', 'pinned here', 'cron', '', '', '0 3 * * *', 't1', 'builder', now(), now()),
		  ('a6', 'p-two', 'pinned, ambiguous', 'cron', '', '', '0 3 * * *', 't3', 'builder', now(), now())`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s := openDSN(t, dsn)
	// u1 had no internal tracker, and called the Jira one by its name.
	in, err := s.GetIntegration(ctx, "internal-u1")
	if err != nil || in.Kind != "internal" || in.Port != confstore.PortTicketSource || in.UserID != "u1" || in.Name != "Apothecary tickets (built in)" {
		t.Fatalf("u1's internal tracker = %+v, %v", in, err)
	}
	sts, _ := s.ListTrackerStatuses(ctx, "internal-u1")
	var names []string
	for _, st := range sts {
		names = append(names, st.Name)
		if st.RemoteID != st.ID || st.IsDone != (st.Name == "Done") {
			t.Errorf("seeded status = %+v", st)
		}
	}
	if !slices.Equal(names, []string{"To do", "In progress", "In review", "Done", "Blocked"}) || sts[0].ID != "internal-u1-todo" {
		t.Errorf("seeded statuses = %+v", sts)
	}
	if tys, _ := s.ListTrackerTypes(ctx, "internal-u1"); len(tys) != 6 || tys[0].ID != "internal-u1-feat" || tys[0].Name != "Feature" {
		t.Errorf("seeded types = %+v", tys)
	}
	// u2's tracker was there: kept as it was.
	if sts, _ := s.ListTrackerStatuses(ctx, "internal-u2"); len(sts) != 1 {
		t.Errorf("u2's statuses = %+v", sts)
	}
	if all, _ := s.ListIntegrations(ctx, "u2"); len(all) != 2 {
		t.Errorf("u2 got a second internal tracker: %+v", all)
	}

	for project, want := range map[string]string{
		"p-single": "jira1",       // one remote tracker, named by its tickets and its automation
		"p-two":    "internal-u1", // two: ambiguous
		"p-none":   "internal-u1", // none
		"p-u2":     "internal-u2",
	} {
		if p, err := s.GetProject(ctx, project); err != nil || p.TicketSourceID != want {
			t.Errorf("%s source = %q, %v; want %s", project, p.TicketSourceID, err, want)
		}
	}

	if a, err := s.GetAutomation(ctx, "a1"); err != nil || !a.Enabled || a.LastError != "" {
		t.Errorf("automation on its workshop's source = %+v, %v", a, err)
	}
	if a, err := s.GetAutomation(ctx, "a2"); err != nil || a.Enabled ||
		!strings.Contains(a.LastError, "GH issues") || !strings.Contains(a.LastError, "Apothecary tickets (built in)") {
		t.Errorf("automation polling another tracker = %+v, %v", a, err)
	}
	if a, err := s.GetAutomation(ctx, "a3"); err != nil || !a.Enabled || a.LastError != "" {
		t.Errorf("cron automation = %+v, %v", a, err)
	}
	// A pinned ticket must be of the workshop's source: a hand-written one
	// is now the internal tracker's, and an ambiguous workshop went there.
	if a, err := s.GetAutomation(ctx, "a4"); err != nil || a.Enabled ||
		!strings.Contains(a.LastError, "ticket t4") || !strings.Contains(a.LastError, "Apothecary tickets (built in)") {
		t.Errorf("cron pinned to an internal ticket on a remote workshop = %+v, %v", a, err)
	}
	if a, err := s.GetAutomation(ctx, "a5"); err != nil || !a.Enabled || a.LastError != "" {
		t.Errorf("cron pinned to its workshop's source = %+v, %v", a, err)
	}
	if a, err := s.GetAutomation(ctx, "a6"); err != nil || a.Enabled ||
		!strings.Contains(a.LastError, "ticket t3") || !strings.Contains(a.LastError, "GH issues") {
		t.Errorf("cron pinned to a remote ticket on an ambiguous workshop = %+v, %v", a, err)
	}

	for _, c := range []struct{ id, source, kind, remote, status string }{
		{"t1", "jira1", "jira", "100", ""},
		{"t2", "jira1", "jira", "dup:t2", ""},           // the newer record of one issue
		{"t3", "gh1", "github_issues", "legacy:t3", ""}, // no remote id kept
		{"t4", "internal-u1", "internal", "t4", ""},
		{"t5", "internal-u1", "internal", "t5", ""},
		{"t6", "internal-u2", "internal", "t6", "internal-u2-todo"},
		{"t8", "internal-u1", "internal", "t8", ""}, // shares t4's empty key
	} {
		tk, err := s.GetTicket(ctx, c.id)
		if err != nil || tk.SourceID != c.source || tk.SourceKind != c.kind || tk.RemoteID != c.remote || tk.StatusID != c.status {
			t.Errorf("%s = %+v, %v; want %+v", c.id, tk, err, c)
		}
	}
	if tk, err := s.GetTicketByRemote(ctx, "jira1", "100"); err != nil || tk.ID != "t1" {
		t.Errorf("the issue's record = %+v, %v", tk, err)
	}
	if err := s.UpsertTicket(ctx, confstore.Ticket{ID: "t7", Title: "again", SourceID: "jira1", SourceKind: "jira", RemoteID: "100",
		CreatedAt: time.Now(), UpdatedAt: time.Now()}); !errors.Is(err, confstore.ErrConflict) {
		t.Errorf("another record of the issue = %v, want ErrConflict", err)
	}

	if sts, _ := s.ListTrackerStatuses(ctx, "jira1"); len(sts) != 0 {
		t.Errorf("remote statuses kept: %+v", sts)
	}
	if cs, _ := s.ListTicketComments(ctx, "t1"); len(cs) != 0 {
		t.Errorf("a remote ticket's comments kept: %+v", cs)
	}
	if cs, _ := s.ListTicketComments(ctx, "t4"); len(cs) != 1 || cs[0].ID != "c3" {
		t.Errorf("internal ticket's comments = %+v", cs)
	}
	var left int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		 WHERE (table_name = 'tickets' AND column_name IN ('project_id', 'remote_updated_at', 'synced_at', 'sync_state', 'sync_error'))
		    OR (table_name = 'tracker_statuses' AND column_name IN ('flow_step', 'remote_category'))
		    OR (table_name = 'ticket_comments' AND column_name = 'remote_id')
		    OR (table_name = 'automations' AND column_name = 'source_id')`).Scan(&left); err != nil || left != 0 {
		t.Errorf("mirror columns left = %d, %v", left, err)
	}
}

// 0007_run_archived_flag: a run archived before the flag read state
// 'archived', its outcome lost. The migration's rebuild replays the log, so
// each archived run gets back how it ended, and the flag besides.
func TestRunArchivedFlagMigration(t *testing.T) {
	dsn := pgtest.Empty(t)
	ctx := context.Background()
	old := openSrc(t, dsn, upTo(t, 6))
	if _, err := old.pool.Exec(ctx, `
		INSERT INTO events (run_id, seq, ts, type, payload) VALUES
		  ('ra', 1, '2026-07-01T10:00:00Z', 'run_created', '{"project_id":"p1","ticket_title":"A"}'),
		  ('ra', 2, '2026-07-01T11:00:00Z', 'run_completed', '{}'),
		  ('ra', 3, '2026-07-02T09:00:00Z', 'run_archived', '{}'),
		  ('rb', 1, '2026-07-01T10:00:00Z', 'run_created', '{"project_id":"p1","ticket_title":"B"}'),
		  ('rb', 2, '2026-07-01T10:30:00Z', 'run_failed', '{"reason":"boom"}'),
		  ('rb', 3, '2026-07-03T09:00:00Z', 'run_archived', '{}'),
		  ('rc', 1, '2026-07-01T10:00:00Z', 'run_created', '{"project_id":"p1","ticket_title":"C"}'),
		  ('rc', 2, '2026-07-01T10:30:00Z', 'run_completed', '{}');
		INSERT INTO runs (run_id, project_id, state, created_at, updated_at) VALUES
		  ('ra', 'p1', 'archived', '2026-07-01T10:00:00Z', '2026-07-02T09:00:00Z'),
		  ('rb', 'p1', 'archived', '2026-07-01T10:00:00Z', '2026-07-03T09:00:00Z'),
		  ('rc', 'p1', 'completed', '2026-07-01T10:00:00Z', '2026-07-01T10:30:00Z')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s := openDSN(t, dsn)
	for _, c := range []struct {
		run, state string
		archived   *time.Time
		success    bool
	}{
		{"ra", "completed", new(time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)), true},
		{"rb", "failed", new(time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC)), false},
		{"rc", "completed", nil, true},
	} {
		var state, factState string
		var archivedAt *time.Time
		var success, archived bool
		if err := s.pool.QueryRow(ctx, `SELECT r.state, r.archived_at, f.state, f.success, f.archived
			FROM runs r JOIN run_facts f USING (run_id) WHERE run_id=$1`, c.run).Scan(
			&state, &archivedAt, &factState, &success, &archived); err != nil {
			t.Fatalf("%s: %v", c.run, err)
		}
		sameTime := archivedAt == nil && c.archived == nil || archivedAt != nil && c.archived != nil && archivedAt.Equal(*c.archived)
		if state != c.state || factState != c.state || !sameTime || success != c.success || archived != (c.archived != nil) {
			t.Errorf("%s: state %s/%s archived_at %v success %v archived %v", c.run, state, factState, archivedAt, success, archived)
		}
	}
}

// 0008_instance_provisioner: the provisioner becomes the instance's. With a
// person's copies only, the oldest is kept, made the instance's, and every
// workshop moves onto it; the other copies go.
func TestInstanceProvisionerMigration(t *testing.T) {
	dsn := pgtest.Empty(t)
	ctx := context.Background()
	old := openSrc(t, dsn, upTo(t, 7))
	if _, err := old.pool.Exec(ctx, `
		INSERT INTO integrations (id, port, kind, name, created_at, updated_at, user_id) VALUES
		  ('repo1', 'repository', 'github', 'gh', now(), now(), 'u1'),
		  ('src1', 'ticketsource', 'internal', 'Apothecary tickets', now(), now(), 'u1'),
		  ('repo2', 'repository', 'github', 'gh', now(), now(), 'u2'),
		  ('src2', 'ticketsource', 'internal', 'Apothecary tickets', now(), now(), 'u2'),
		  ('prov-u2', 'provisioner', 'kubernetes', 'Kubernetes', '2026-01-02T00:00:00Z', now(), 'u2'),
		  ('prov-u1', 'provisioner', 'kubernetes', 'Kubernetes', '2026-01-01T00:00:00Z', now(), 'u1');
		INSERT INTO projects (id, name, repo_account_id, provisioner_id, ticket_source_id, repo, default_flow, created_at, updated_at, user_id) VALUES
		  ('p1', 'mine', 'repo1', 'prov-u1', 'src1', '{}', 'builder', now(), now(), 'u1'),
		  ('p2', 'theirs', 'repo2', 'prov-u2', 'src2', '{}', 'builder', now(), now(), 'u2')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s := openDSN(t, dsn)
	var ids []string
	rows, err := s.pool.Query(ctx, `SELECT id || '@' || user_id FROM integrations WHERE port = 'provisioner'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 || ids[0] != "prov-u1@" {
		t.Errorf("provisioners = %v, want the oldest, the instance's", ids)
	}
	for _, p := range []string{"p1", "p2"} {
		var prov string
		if err := s.pool.QueryRow(ctx, `SELECT provisioner_id FROM projects WHERE id = $1`, p).Scan(&prov); err != nil || prov != "prov-u1" {
			t.Errorf("%s builds on %q, %v", p, prov, err)
		}
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
