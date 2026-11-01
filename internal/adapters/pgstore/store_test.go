package pgstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"apothecary/internal/adapters/pgstore/pgtest"
	"apothecary/internal/platform/secrets"
	"apothecary/internal/ports/egressport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

type (
	auditRow    = egressport.EgressAuditRow
	auditFilter = egressport.EgressAuditFilter
)

// openRaw is a plain connection on dsn, for reaching a schema Open refuses.
func openRaw(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func TestValidateDSN(t *testing.T) {
	for dsn, ok := range map[string]bool{
		"postgres://u:p@h/db":    true,
		"host=/var/run dbname=x": true,
		"":                       false,
		"mysql://u@h/db":         false,
		"sqlite:///tmp/x.db":     false,
	} {
		if err := ValidateDSN(dsn); (err == nil) != ok {
			t.Errorf("ValidateDSN(%q) = %v, want ok=%v", dsn, err, ok)
		}
	}
}

func TestOpenSurfacesConnectionFailures(t *testing.T) {
	if _, err := Open(context.Background(), "postgres://bad port"); err == nil || !strings.Contains(err.Error(), "postgres connect") {
		t.Errorf("unparsable DSN = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Port 1 on loopback refuses straight away.
	if _, err := Open(ctx, "postgres://u:p@127.0.0.1:1/db?connect_timeout=1"); err == nil {
		t.Error("Open against nothing succeeded")
	}
}

func TestSquashedOnlyMatchesTheWholeChain(t *testing.T) {
	whole := map[int]bool{}
	for v := 1; v <= preSquashHead; v++ {
		whole[v] = true
	}
	if !squashed(whole) {
		t.Error("the whole chain must collapse")
	}
	short := map[int]bool{}
	for v := 1; v < preSquashHead; v++ {
		short[v] = true
	}
	if squashed(short) {
		t.Error("a chain applied partway must not collapse")
	}
	holed := map[int]bool{}
	for v := 2; v <= preSquashHead+1; v++ {
		holed[v] = true
	}
	if squashed(holed) {
		t.Error("the right count with the wrong versions must not collapse")
	}
	if squashed(nil) {
		t.Error("an empty ledger must not collapse")
	}
}

func TestMigrationsAreOrderedAndUnique(t *testing.T) {
	migs, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(migs); i++ {
		if migs[i-1].version >= migs[i].version {
			t.Fatalf("out of order: %+v before %+v", migs[i-1], migs[i])
		}
	}
}

// A migration that fails rolls back and is not recorded, so the next boot
// retries it instead of running against half a schema.
func TestMigrateFailureIsNotRecorded(t *testing.T) {
	dsn := pgtest.Empty(t)
	ctx := context.Background()
	s := openDSN(t, dsn)
	if _, err := s.pool.Exec(ctx, `DROP TABLE ssh_keys; CREATE TABLE ssh_keys (nope int); DELETE FROM schema_migrations WHERE version = 20`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	_, err := Open(ctx, dsn)
	if err == nil || !strings.Contains(err.Error(), "0020_ssh_keys") {
		t.Fatalf("Open = %v, want a failure naming the migration", err)
	}
	// Repair the obstacle: the migration applies on the next boot.
	repair := openRaw(t, dsn)
	if _, err := repair.Exec(ctx, `DROP TABLE ssh_keys`); err != nil {
		t.Fatal(err)
	}
	s2 := openDSN(t, dsn)
	if err := s2.pool.QueryRow(ctx, `SELECT 1 FROM schema_migrations WHERE version = 20`).Scan(new(int)); err != nil {
		t.Errorf("retried migration not recorded: %v", err)
	}
}

func TestMigrateSurfacesAnUnreadableLedger(t *testing.T) {
	dsn := pgtest.Empty(t)
	ctx := context.Background()
	conn := openRaw(t, dsn)
	if _, err := conn.Exec(ctx, `CREATE TABLE schema_migrations (nope int)`); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, dsn); err == nil || !strings.Contains(err.Error(), "read migrations table") {
		t.Fatalf("Open = %v", err)
	}
}

// The projection rebuild after a migration is part of Open: a log it cannot
// replay fails the boot instead of serving stale read models.
func TestOpenFailsWhenThePostMigrationRebuildFails(t *testing.T) {
	dsn := pgtest.Empty(t)
	ctx := context.Background()
	s := openDSN(t, dsn)
	if _, err := s.Append(ctx, "r1", &event.RunCreated{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE events SET payload='[]'; DELETE FROM schema_migrations WHERE version = 20`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(ctx, dsn); err == nil || !strings.Contains(err.Error(), "rebuild projections after migrating") {
		t.Fatalf("Open = %v", err)
	}
}

func TestAppendEmptyRunID(t *testing.T) {
	s := open(t)
	if _, err := s.Append(context.Background(), "", &event.RunCreated{}); err == nil {
		t.Fatal("append with no run id succeeded")
	}
	if ids, _ := s.RunIDs(context.Background()); len(ids) != 0 {
		t.Errorf("run ids = %v", ids)
	}
}

// unmarshalable is a payload json.Marshal refuses.
type unmarshalable struct{ C chan int }

func (unmarshalable) EventType() event.Type { return event.TypeRunCreated }

// A batch is atomic: one payload failing keeps the ones before it off the
// log, leaves the projection alone, and burns no seq.
func TestAppendBatchIsAtomic(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, err := s.Append(ctx, "r1", &event.RunCreated{TicketTitle: "t"}); err != nil {
		t.Fatal(err)
	}
	watch, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := s.Watch(watch, "r1", 1)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]event.Payload{
		"unmarshalable payload":    unmarshalable{},
		"jsonb refuses a NUL byte": &event.RunFailed{Reason: "a\x00b"},
		"projection CHECK fails":   &event.SnapshotCaptured{SnapshotID: "x", Kind: "not-a-kind", Filename: "f", Mime: "m"},
	}
	for name, bad := range cases {
		if _, err := s.Append(ctx, "r1", &event.EnvRequested{}, bad); err == nil {
			t.Errorf("%s: append succeeded", name)
		}
	}
	got, _ := s.Events(ctx, "r1", 0)
	if len(got) != 1 {
		t.Errorf("failed appends left events behind: %+v", got)
	}
	// The next real append takes seq 2, and it is the first thing the
	// subscriber hears: nothing from the failed batches reached it.
	evs, err := s.Append(ctx, "r1", &event.EnvRequested{})
	if err != nil || evs[0].Seq != 2 {
		t.Fatalf("append after failures = %+v, %v", evs, err)
	}
	select {
	case e := <-ch:
		if e.Seq != 2 {
			t.Errorf("subscriber heard seq %d first", e.Seq)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscriber heard nothing")
	}
}

func TestAppendStampsWithTheInjectedClockInUTC(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("X", 3600))
	s.Clock = func() time.Time { return at }
	evs, err := s.Append(ctx, "r1", &event.RunCreated{})
	if err != nil {
		t.Fatal(err)
	}
	if !evs[0].TS.Equal(at) || evs[0].TS.Location() != time.UTC {
		t.Errorf("ts = %v", evs[0].TS)
	}
	got, _ := s.Events(ctx, "r1", 0)
	if !got[0].TS.Equal(at) {
		t.Errorf("stored ts = %v", got[0].TS)
	}
}

func TestRunViewAndRunByEnv(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, ok, err := s.RunView(ctx, "nope"); ok || err != nil {
		t.Errorf("missing run = %v, %v", ok, err)
	}
	clock := testNow
	s.Clock = func() time.Time { clock = clock.Add(time.Minute); return clock }
	for _, id := range []string{"r1", "r2"} {
		if _, err := s.Append(ctx, id, &event.RunCreated{TicketTitle: id, UserID: "u1"}, &event.EnvRequested{}, &event.EnvReady{EnvID: "env-shared"}); err != nil {
			t.Fatal(err)
		}
	}
	v, ok, err := s.RunView(ctx, "r1")
	if err != nil || !ok || v.RunID != "r1" || v.TicketTitle != "r1" || v.UserID != "u1" || v.EnvID != "env-shared" {
		t.Fatalf("RunView = %+v, %v, %v", v, ok, err)
	}
	// A rebuild race can leave two on one workspace: the newest wins.
	if v, ok, err := s.RunByEnv(ctx, "env-shared"); err != nil || !ok || v.RunID != "r2" {
		t.Errorf("RunByEnv = %+v, %v, %v", v, ok, err)
	}
	if _, ok, err := s.RunByEnv(ctx, "env-none"); ok || err != nil {
		t.Errorf("unknown env = %v, %v", ok, err)
	}
	// A blank env id must not match every run without a workspace.
	if _, err := s.Append(ctx, "r3", &event.RunCreated{}); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := s.RunByEnv(ctx, ""); ok || err != nil || v.RunID != "" {
		t.Errorf("blank env = %+v, %v, %v", v, ok, err)
	}
}

// The column is the authority when the JSON has no owner: a hand-run
// assignment holds without a rebuild.
func TestRunOwnerFallsBackToTheColumn(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, err := s.Append(ctx, "r1", &event.RunCreated{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE runs SET user_id='hand' WHERE run_id='r1'`); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := s.RunView(ctx, "r1"); err != nil || !ok || v.UserID != "hand" {
		t.Errorf("view = %+v, %v, %v", v, ok, err)
	}
	if views, _ := s.RunViews(ctx); len(views) != 1 || views[0].UserID != "hand" {
		t.Errorf("views = %+v", views)
	}
}

func TestRunViewsNewestFirst(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	clock := testNow
	s.Clock = func() time.Time { clock = clock.Add(time.Minute); return clock }
	for _, id := range []string{"r1", "r2", "r3"} {
		if _, err := s.Append(ctx, id, &event.RunCreated{}); err != nil {
			t.Fatal(err)
		}
	}
	views, err := s.RunViews(ctx)
	if err != nil || len(views) != 3 || views[0].RunID != "r3" || views[2].RunID != "r1" {
		t.Errorf("views = %+v, %v", views, err)
	}
}

// A subscriber that stops reading loses its stream, never the appender: the
// overflow closes it so it re-syncs from a seq, and the log is complete.
func TestWatchOverflowClosesTheStream(t *testing.T) {
	s := open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch, err := s.Watch(ctx, "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	const n = 300 // above the subscriber's 256 buffer
	batch := make([]event.Payload, n)
	for i := range batch {
		batch[i] = &event.EnvRequested{}
	}
	if _, err := s.Append(ctx, "r1", batch...); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for range ch { // must end by itself, before the context does
		seen++
	}
	if ctx.Err() != nil {
		t.Fatal("stream never closed on overflow")
	}
	if seen >= n {
		t.Errorf("read %d events through an overflowed stream", seen)
	}
	// The subscriber is gone, and a re-sync reads what it missed.
	if subs := s.subscribers("r1"); subs != 0 {
		t.Errorf("subscribers after overflow = %d", subs)
	}
	rest, err := s.Events(ctx, "r1", int64(seen))
	if err != nil || len(rest) != n-seen {
		t.Errorf("re-sync got %d, want %d (%v)", len(rest), n-seen, err)
	}
}

func (s *Store) subscribers(runID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs[runID])
}

func TestWatchFailedReplayLeavesNoSubscriber(t *testing.T) {
	s := open(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Watch(ctx, "r1", 0); err == nil {
		t.Fatal("Watch on a dead context succeeded")
	}
	if n := s.subscribers("r1"); n != 0 {
		t.Errorf("subscribers = %d", n)
	}
}

// Cancelling mid-replay ends the stream: nothing keeps sending to a consumer
// that left, and the subscription goes with it.
func TestWatchCancelDuringReplay(t *testing.T) {
	s := open(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := s.Append(context.Background(), "r1", &event.RunCreated{}, &event.EnvRequested{}); err != nil {
		t.Fatal(err)
	}
	ch, err := s.Watch(ctx, "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	<-ch
	cancel()
	for range ch {
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.subscribers("r1") != 0 {
		if time.Now().After(deadline) {
			t.Fatal("subscription outlived the stream")
		}
		time.Sleep(time.Millisecond)
	}
}

// Subscribing comes before the replay, so a commit racing the replay query
// lands in both: seqs at or below the cursor are dropped, once each.
func TestWatchReplayFromAfterSeqSkipsOldEvents(t *testing.T) {
	s := open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.Append(ctx, "r1", &event.RunCreated{}, &event.EnvRequested{}, &event.EnvReady{EnvID: "e"}); err != nil {
		t.Fatal(err)
	}
	ch, err := s.Watch(ctx, "r1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if e := <-ch; e.Seq != 3 {
		t.Errorf("first = %d", e.Seq)
	}
	// Injected directly: what the live path looks like when a commit raced
	// the replay query and landed in both.
	s.mu.Lock()
	sb := s.subs["r1"][0]
	s.mu.Unlock()
	sb.ch <- event.Event{RunID: "r1", Seq: 3}
	sb.ch <- event.Event{RunID: "r1", Seq: 4}
	if e := <-ch; e.Seq != 4 {
		t.Errorf("after a duplicate = %d, want 4", e.Seq)
	}
}

func TestPingListingScopeStatusAndLimit(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	clock := testNow
	s.Clock = func() time.Time { clock = clock.Add(time.Second); return clock }

	if _, err := s.Append(ctx, "r1", &event.RunCreated{UserID: "u1"},
		&event.PingRaised{PingID: "p1", Type: event.PingGate, Question: "ok?"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "r1", &event.PingRaised{PingID: "p2", Type: "blocker", Question: "db?", Link: "https://x.local/mr"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "r2", &event.RunCreated{UserID: "u2"},
		&event.PingRaised{PingID: "p3", Type: "blocker", Question: "theirs"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "r1", &event.PingAnswered{PingID: "p1", Answer: "approve", AttachmentIDs: []string{"a1"}}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		user, status string
		want         []string
	}{
		{"", "", []string{"p3", "p2", "p1"}},
		{"", "all", []string{"p3", "p2", "p1"}},
		{"u1", "", []string{"p2", "p1"}},
		{"u1", "pending", []string{"p2"}},
		{"u1", "answered", []string{"p1"}},
		{"u2", "answered", nil},
		{"u3", "", nil},
		{"", "pending", []string{"p3", "p2"}},
	}
	for _, c := range cases {
		got, err := s.ListPings(ctx, c.user, c.status, 0)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, p := range got {
			ids = append(ids, p.PingID)
		}
		if strings.Join(ids, ",") != strings.Join(c.want, ",") {
			t.Errorf("ListPings(%q, %q) = %v, want %v", c.user, c.status, ids, c.want)
		}
	}
	one, err := s.GetPing(ctx, "p1")
	if err != nil || one.UserID != "u1" || one.AnswerAttachmentIDs[0] != "a1" {
		t.Errorf("answered ping = %+v, %v", one, err)
	}
	if two, _ := s.GetPing(ctx, "p2"); two.Link != "https://x.local/mr" || two.PayloadRef != two.Link {
		t.Errorf("link ping = %+v", two)
	}
	if lim, _ := s.ListPings(ctx, "", "", 2); len(lim) != 2 || lim[0].PingID != "p3" {
		t.Errorf("limit 2 = %+v", lim)
	}
}

func TestPingListLimitClamps(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	batch := []event.Payload{&event.RunCreated{}}
	for i := range 101 {
		batch = append(batch, &event.PingRaised{PingID: "p" + strings.Repeat("x", i), Type: "blocker"})
	}
	if _, err := s.Append(ctx, "r1", batch...); err != nil {
		t.Fatal(err)
	}
	for limit, want := range map[int]int{0: 100, -1: 100, 501: 100, 500: 101, 3: 3} {
		got, err := s.ListPings(ctx, "", "", limit)
		if err != nil || len(got) != want {
			t.Errorf("limit %d = %d rows, %v; want %d", limit, len(got), err, want)
		}
	}
}

// A terminal run withdraws its open pings and only those: an answered one
// stays answered, another run's stays pending.
func TestTerminalRunsWithdrawOpenPings(t *testing.T) {
	cases := map[string]event.Payload{
		"abandoned": &event.RunAbandoned{},
		"failed":    &event.RunFailed{Reason: "x"},
		"archived":  &event.RunArchived{},
		"completed": &event.RunCompleted{},
	}
	for name, term := range cases {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			ctx := context.Background()
			if _, err := s.Append(ctx, "r1", &event.RunCreated{},
				&event.PingRaised{PingID: "open", Type: "blocker"},
				&event.PingRaised{PingID: "done", Type: "blocker"},
				&event.PingAnswered{PingID: "done", Answer: "x"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Append(ctx, "r2", &event.RunCreated{}, &event.PingRaised{PingID: "other", Type: "blocker"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Append(ctx, "r1", term); err != nil {
				t.Fatal(err)
			}
			for id, want := range map[string]string{"open": "withdrawn", "done": "answered", "other": "pending"} {
				if p, err := s.GetPing(ctx, id); err != nil || p.Status != want {
					t.Errorf("%s = %q, %v; want %s", id, p.Status, err, want)
				}
			}
		})
	}
}

// A row written before answer kinds existed reads as what its type implies.
func TestPingAnswerKindFallsBackToTheType(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, err := s.Append(ctx, "r1", &event.RunCreated{}, &event.PingRaised{PingID: "g", Type: event.PingGate}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE pings SET answer_kind=''`); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPing(ctx, "g")
	want := (&event.PingRaised{Type: event.PingGate}).Kind()
	if err != nil || got.AnswerKind != want || want == "" {
		t.Errorf("kind = %q, %v; want %q", got.AnswerKind, err, want)
	}
}

// A second raise of the same id is a no-op: the projection is idempotent, so
// the log can be replayed over itself.
func TestPingRaiseAndReviewAreIdempotent(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	review := []event.ReviewComment{{Path: "a", Body: "first"}}
	if _, err := s.Append(ctx, "r1", &event.RunCreated{},
		&event.PingRaised{PingID: "p", Type: "gate", Question: "one"},
		&event.PingAnswered{PingID: "p", Answer: "reject", Review: review}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "r1",
		&event.PingRaised{PingID: "p", Type: "gate", Question: "two"},
		&event.PingAnswered{PingID: "p", Answer: "reject", Review: []event.ReviewComment{{Path: "a", Body: "second"}}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPing(ctx, "p")
	if err != nil || got.Question != "one" || len(got.Review) != 1 || got.Review[0].Body != "first" {
		t.Errorf("ping = %+v, %v", got, err)
	}
}

func TestProjectionMatchesReduceForPings(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	evs, err := s.Append(ctx, "r1", &event.RunCreated{UserID: "u"},
		&event.PingRaised{PingID: "a", Type: "blocker", Question: "q"},
		&event.PingRaised{PingID: "b", Type: "blocker", Question: "q"},
		&event.PingAnswered{PingID: "a", Answer: "x"},
		&event.RunFailed{Reason: "y"})
	if err != nil {
		t.Fatal(err)
	}
	want := run.ReducePings(evs)
	got, err := s.ListPings(ctx, "", "all", 0)
	if err != nil || len(got) != len(want) {
		t.Fatalf("got %+v, want %+v (%v)", got, want, err)
	}
	status := map[string]string{}
	for _, p := range got {
		status[p.PingID] = p.Status
	}
	for _, p := range want {
		if status[p.PingID] != p.Status {
			t.Errorf("%s: projected %q, reduced %q", p.PingID, status[p.PingID], p.Status)
		}
	}
}

// A rebuild over a log it cannot decode rolls back: the old read models stay.
func TestRebuildRollsBackOnAnUnreadableLog(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	for _, id := range []string{"r1", "r2"} {
		if _, err := s.Append(ctx, id, &event.RunCreated{TicketTitle: id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE events SET payload='[]' WHERE run_id='r2'`); err != nil {
		t.Fatal(err)
	}
	_, err := s.RebuildProjections(ctx)
	if err == nil || !strings.Contains(err.Error(), "run r2 seq 1") {
		t.Fatalf("rebuild = %v", err)
	}
	if views, _ := s.RunViews(ctx); len(views) != 2 {
		t.Errorf("failed rebuild lost the projection: %d rows", len(views))
	}
	// A log row that decodes but will not project fails the same way.
	if _, err := s.pool.Exec(ctx, `UPDATE events SET type='snapshot_captured', payload='{"snapshot_id":"x","kind":"nope"}' WHERE run_id='r2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RebuildProjections(ctx); err == nil || !strings.Contains(err.Error(), "rebuild r2") {
		t.Fatalf("rebuild = %v", err)
	}
	if views, _ := s.RunViews(ctx); len(views) != 2 {
		t.Errorf("failed rebuild lost the projection: %d rows", len(views))
	}
}

func TestChannelsListInOrderWithOwner(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.SaveChannel(ctx, "c1", "u1", "matrix", map[string]any{"a": 1.0}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannel(ctx, "c1", "u2", "slack", map[string]any{}); err == nil {
		t.Error("second channel with one id stored")
	}
	if err := s.SaveChannel(ctx, "c2", "u1", "slack", map[string]any{"x": make(chan int)}); err == nil {
		t.Error("unmarshalable config stored")
	}
	rows, err := s.LoadChannels(ctx)
	if err != nil || len(rows) != 1 || rows[0].UserID != "u1" || rows[0].Config["a"] != 1.0 {
		t.Errorf("rows = %+v, %v", rows, err)
	}
}

// An unknown filter never widens the audit; the limit clamps; the time range
// only ever widens on re-ingest.
func TestEgressAuditFiltersAndBounds(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	if err := s.UpsertEgressAudit(ctx, nil); err != nil {
		t.Errorf("empty upsert = %v", err)
	}
	row := func(run, host, verdict string, last time.Time) (r auditRow) {
		r.Project, r.Run, r.Host, r.Verdict, r.Port, r.First, r.Last = "p1", run, host, verdict, 443, testNow, last
		return r
	}
	rows := []auditRow{
		row("r1", "api.GitHub.com", "allow", testNow.Add(time.Minute)),
		row("r1", "evil.test", "deny", testNow.Add(2*time.Minute)),
	}
	if err := s.UpsertEgressAudit(ctx, rows); err != nil {
		t.Fatal(err)
	}
	// A late re-ingest of older data shrinks neither end, nor the counters.
	again := rows[0]
	again.First, again.Last, again.Count, again.DstIP = testNow.Add(-time.Hour), testNow, 0, "9.9.9.9"
	if err := s.UpsertEgressAudit(ctx, []auditRow{again}); err != nil {
		t.Fatal(err)
	}
	got, err := s.QueryEgressAudit(ctx, auditFilter{Host: "github"})
	if err != nil || len(got) != 1 {
		t.Fatalf("host filter (case-insensitive) = %+v, %v", got, err)
	}
	r := got[0]
	if !r.First.Equal(testNow.Add(-time.Hour)) || !r.Last.Equal(testNow.Add(time.Minute)) || r.DstIP != "9.9.9.9" {
		t.Errorf("re-ingest = %+v", r)
	}
	// Newest activity first.
	all, _ := s.QueryEgressAudit(ctx, auditFilter{})
	if len(all) != 2 || all[0].Host != "evil.test" {
		t.Errorf("order = %+v", all)
	}
	if none, _ := s.QueryEgressAudit(ctx, auditFilter{Project: "p1", Run: "r2"}); len(none) != 0 {
		t.Errorf("other run = %+v", none)
	}
	if lim, _ := s.QueryEgressAudit(ctx, auditFilter{Limit: 1}); len(lim) != 1 {
		t.Errorf("limit 1 = %+v", lim)
	}
	if lim, _ := s.QueryEgressAudit(ctx, auditFilter{Limit: 6000}); len(lim) != 2 {
		t.Errorf("oversized limit = %+v", lim)
	}
}

// What Clean leaves is what Postgres takes: in the payload's jsonb and in the
// columns projected from it, which refuse more than jsonb does.
func TestCleanedPayloadsAreStorable(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	bad := "bad\x00text\xff"
	ps := []event.Payload{
		&event.RunCreated{TicketTitle: bad, TicketGoal: bad, Branch: bad, ProjectID: "p", UserID: "u", FlowName: "f"},
		&event.PingRaised{PingID: "p1", Type: event.PingClarification, Question: bad, Context: bad, StageID: "s"},
		&event.PingAnswered{PingID: "p1", Answer: "accept", Content: map[string]any{"k\x00": bad, "list": []any{bad}}},
		&event.AgentTool{StageID: "s", ToolCallID: "t1", Title: bad, Input: bad, Output: bad},
	}
	if _, err := s.Append(ctx, "dirty", ps[3]); err == nil {
		t.Fatal("Postgres took it unclean: this test proves nothing")
	}
	event.Clean(ps...)
	if _, err := s.Append(ctx, "r1", ps...); err != nil {
		t.Fatal(err)
	}
	evs, err := s.Events(ctx, "r1", 0)
	if err != nil || len(evs) != len(ps) {
		t.Fatalf("events = %d, %v", len(evs), err)
	}
	if tool, ok := evs[3].Payload.(*event.AgentTool); !ok || tool.Output != "bad�text�" {
		t.Errorf("tool = %+v", evs[3].Payload)
	}
}

// What the ticket path leaves is what the tickets table takes.
func TestTicketWithBinaryTextIsStored(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seedIntegration(t, s, "i1", "u1")
	seedProject(t, s, mkProject("p1", "web", "u1", "i1"))
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	svc := confstore.New(s, key)
	tk, err := svc.CreateTicket(ctx, confstore.Ticket{ProjectID: "p1", Title: "t\x00", Goal: "g\xff"})
	if err != nil {
		t.Fatal(err)
	}
	if read, err := svc.GetTicket(ctx, tk.ID); err != nil || read.Title != "t\uFFFD" || read.Goal != "g\uFFFD" {
		t.Errorf("ticket = %+v, %v", read, err)
	}
}
