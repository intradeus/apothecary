// Package filetest is the filestore port contract (AttachmentDB and
// SnapshotDB), run against every implementation: Mem in filestore's own
// tests, Postgres in pgstore's. Production runs only Postgres, so what it
// does is what this asserts: a Mem that differs makes the unit tier prove the
// wrong thing.
package filetest

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"apothecary/internal/platform/filestore"
)

// DB is both ports: one store implements them.
type DB interface {
	filestore.AttachmentDB
	filestore.SnapshotDB
}

var t0 = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// asRead is a time the way a timestamptz column hands it back: whole
// microseconds, truncated rather than rounded, in the process's zone.
func asRead(at time.Time) time.Time { return at.Truncate(time.Microsecond).Local() }

func upload(id, user string, at time.Time) filestore.Attachment {
	return filestore.Attachment{ID: id, UserID: user, Filename: id + ".txt", Mime: "text/plain", Size: 3, SHA256: "sha-" + id, CreatedAt: at}
}

func logOf(id, run, stage string, iteration int, at time.Time) filestore.Snapshot {
	return filestore.Snapshot{ID: id, RunID: run, Kind: filestore.KindLog, StageID: stage, Iteration: iteration,
		Filename: "f.log", Mime: "text/plain", Size: 1, SHA256: "sha-" + id, CreatedAt: at}
}

func pingFile(id, run, ping string, at time.Time) filestore.Snapshot {
	return filestore.Snapshot{ID: id, RunID: run, Kind: filestore.KindPingFile, StageID: "plan", PingID: ping, Path: "a/f.md",
		Filename: "f.md", Mime: "text/markdown", Size: 1, SHA256: "sha-" + id, CreatedAt: at}
}

func insertAll(t *testing.T, db DB, rows ...filestore.Attachment) {
	t.Helper()
	for _, a := range rows {
		if err := db.InsertAttachment(context.Background(), a); err != nil {
			t.Fatalf("insert %s: %v", a.ID, err)
		}
	}
}

func captureAll(t *testing.T, db DB, rows ...filestore.Snapshot) {
	t.Helper()
	for _, s := range rows {
		if err := db.InsertSnapshot(context.Background(), s); err != nil {
			t.Fatalf("insert %s: %v", s.ID, err)
		}
	}
}

func get(t *testing.T, db DB, id string) filestore.Attachment {
	t.Helper()
	a, err := db.GetAttachment(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return a
}

func attachmentIDs(rows []filestore.Attachment, err error) []string {
	if err != nil {
		return []string{"error: " + err.Error()}
	}
	out := []string{}
	for _, a := range rows {
		out = append(out, a.ID)
	}
	return out
}

func snapshotIDs(rows []filestore.Snapshot, err error) []string {
	if err != nil {
		return []string{"error: " + err.Error()}
	}
	out := []string{}
	for _, s := range rows {
		out = append(out, s.ID)
	}
	return out
}

// RunContract exercises one implementation. open must return a fresh, empty
// store.
//
// seedTicket makes a ticket row exist: attachments.ticket_id is a foreign key
// in Postgres and the port has no way to make the parent. Mem has no tickets
// table, so it passes nil; the one case only that key can fail then does not
// run, which leaves a claim onto a missing ticket visible to Postgres alone.
func RunContract(t *testing.T, open func(t *testing.T) DB, seedTicket func(t *testing.T, db DB, id string)) {
	t.Helper()
	ctx := context.Background()
	ticket := func(t *testing.T, db DB, id string) {
		t.Helper()
		if seedTicket != nil {
			seedTicket(t, db, id)
		}
	}

	t.Run("attachment roundtrip", func(t *testing.T) {
		db := open(t)
		ticket(t, db, "t1")
		in := filestore.Attachment{ID: "a1", UserID: "u1", Filename: "spec.md", Mime: "text/markdown", Size: 1 << 40, SHA256: "ab12", CreatedAt: t0}
		onTicket := upload("a2", "u1", t0)
		onTicket.TicketID = "t1"
		onRun := upload("a3", "u1", t0)
		onRun.RunID = "r1"
		onPing := upload("a4", "u1", t0)
		onPing.RunID, onPing.PingID = "r1", "p1"
		insertAll(t, db, in, onTicket, onRun, onPing)
		for _, want := range []filestore.Attachment{in, onTicket, onRun, onPing} {
			want.CreatedAt = asRead(want.CreatedAt)
			if got := get(t, db, want.ID); !reflect.DeepEqual(got, want) {
				t.Errorf("roundtrip = %+v, want %+v", got, want)
			}
		}
		if got := get(t, db, "a1"); !got.Unclaimed() {
			t.Errorf("a fresh upload reads as claimed: %+v", got)
		}
		if got := get(t, db, "a3"); got.Unclaimed() {
			t.Errorf("a row inserted with a run reads as unclaimed: %+v", got)
		}
		// No owner column is required: an upload with nobody behind it stands.
		insertAll(t, db, upload("a5", "", t0))
		if got := get(t, db, "a5"); got.UserID != "" {
			t.Errorf("empty owner = %q", got.UserID)
		}
	})

	t.Run("attachment timestamps are stored in microseconds", func(t *testing.T) {
		db := open(t)
		at := time.Date(2026, 3, 4, 7, 6, 7, 999_999_999, time.FixedZone("elsewhere", 2*60*60))
		insertAll(t, db, upload("a1", "u1", at))
		got := get(t, db, "a1").CreatedAt
		if want := asRead(at); !reflect.DeepEqual(got, want) {
			t.Errorf("created_at = %v, want %v", got, want)
		}
		insertAll(t, db, upload("a2", "u1", time.Time{}))
		if got := get(t, db, "a2").CreatedAt; !got.IsZero() {
			t.Errorf("zero time = %v", got)
		}
	})

	t.Run("attachment insert refuses what the schema does", func(t *testing.T) {
		db := open(t)
		ticket(t, db, "t1")
		first := upload("a1", "u1", t0)
		insertAll(t, db, first)
		dup := upload("a1", "u2", t0.Add(time.Hour))
		dup.Filename = "other.txt"
		if err := db.InsertAttachment(ctx, dup); err == nil {
			t.Error("a duplicate id was stored")
		}
		if got := get(t, db, "a1"); got.UserID != "u1" || got.Filename != first.Filename {
			t.Errorf("duplicate insert overwrote the row: %+v", got)
		}
		twoOwners := upload("a2", "u1", t0)
		twoOwners.TicketID, twoOwners.RunID = "t1", "r1"
		if err := db.InsertAttachment(ctx, twoOwners); err == nil {
			t.Error("a row with a ticket and a run was stored")
		}
		noRun := upload("a3", "u1", t0)
		noRun.PingID = "p1"
		if err := db.InsertAttachment(ctx, noRun); err == nil {
			t.Error("a ping file without a run was stored")
		}
		pingOnTicket := upload("a4", "u1", t0)
		pingOnTicket.TicketID, pingOnTicket.PingID = "t1", "p1"
		if err := db.InsertAttachment(ctx, pingOnTicket); err == nil {
			t.Error("a ping file on a ticket was stored")
		}
		for _, id := range []string{"a2", "a3", "a4"} {
			if _, err := db.GetAttachment(ctx, id); !errors.Is(err, filestore.ErrNotFound) {
				t.Errorf("refused %s = %v", id, err)
			}
		}
	})

	// What a text column cannot hold never gets in. Lookups with such text are
	// not here: Postgres answers with a raw encoding error, not ErrNotFound.
	t.Run("text columns refuse NUL and invalid UTF-8", func(t *testing.T) {
		db := open(t)
		for _, bad := range []string{"a\x00b", "a\xffb"} {
			a := upload("a1", "u1", t0)
			a.Filename = bad
			if err := db.InsertAttachment(ctx, a); err == nil {
				t.Errorf("attachment filename %q stored", bad)
			}
			s := logOf("s1", "r1", "b", 0, t0)
			s.Filename = bad
			if err := db.InsertSnapshot(ctx, s); err == nil {
				t.Errorf("snapshot filename %q stored", bad)
			}
		}
		if _, err := db.GetAttachment(ctx, "a1"); !errors.Is(err, filestore.ErrNotFound) {
			t.Errorf("a refused attachment is readable: %v", err)
		}
		if _, err := db.GetSnapshot(ctx, "s1"); !errors.Is(err, filestore.ErrNotFound) {
			t.Errorf("a refused snapshot is readable: %v", err)
		}
	})

	t.Run("attachment not found", func(t *testing.T) {
		db := open(t)
		if _, err := db.GetAttachment(ctx, "nope"); !errors.Is(err, filestore.ErrNotFound) {
			t.Errorf("missing = %v, want ErrNotFound", err)
		}
		// A reader of nothing gets nil, not an empty slice.
		if rows, err := db.AttachmentsForTicket(ctx, "nope"); rows != nil || err != nil {
			t.Errorf("for ticket = %#v, %v", rows, err)
		}
		if rows, err := db.AttachmentsForRun(ctx, "nope"); rows != nil || err != nil {
			t.Errorf("for run = %#v, %v", rows, err)
		}
		if rows, err := db.AttachmentsForPing(ctx, "nope"); rows != nil || err != nil {
			t.Errorf("for ping = %#v, %v", rows, err)
		}
	})

	t.Run("a claim names exactly one owner", func(t *testing.T) {
		db := open(t)
		ticket(t, db, "t1")
		insertAll(t, db, upload("a1", "u1", t0))
		for _, c := range [][3]string{{"", "", ""}, {"t1", "r1", ""}, {"", "", "p1"}, {"t1", "r1", "p1"}} {
			if err := db.ClaimAttachments(ctx, "u1", []string{"a1"}, c[0], c[1], c[2]); err == nil {
				t.Errorf("claim ticket=%q run=%q ping=%q accepted", c[0], c[1], c[2])
			}
			// The same refusal with nothing to claim: the arguments are wrong
			// whatever they match.
			if err := db.ClaimAttachments(ctx, "u1", nil, c[0], c[1], c[2]); err == nil {
				t.Errorf("empty claim ticket=%q run=%q ping=%q accepted", c[0], c[1], c[2])
			}
		}
		if got := get(t, db, "a1"); !got.Unclaimed() || got.PingID != "" {
			t.Errorf("refused claim wrote: %+v", got)
		}
	})

	t.Run("a claim is the uploader's and one shot", func(t *testing.T) {
		db := open(t)
		ticket(t, db, "t1")
		ticket(t, db, "t2")
		insertAll(t, db, upload("mine", "u1", t0), upload("theirs", "u2", t0), upload("taken", "u1", t0), upload("ticketed", "u1", t0), upload("nobodys", "", t0))
		if err := db.ClaimAttachments(ctx, "u1", []string{"taken"}, "", "r0", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.ClaimAttachments(ctx, "u1", []string{"ticketed"}, "t1", "", ""); err != nil {
			t.Fatal(err)
		}
		// One list of everything a caller might re-send: only the uploader's
		// still-unclaimed row moves, and nothing complains.
		ids := []string{"mine", "mine", "theirs", "taken", "ticketed", "ghost", "nobodys"}
		if err := db.ClaimAttachments(ctx, "u1", ids, "", "r1", "p1"); err != nil {
			t.Fatalf("mixed claim = %v", err)
		}
		if got := get(t, db, "mine"); got.RunID != "r1" || got.PingID != "p1" || got.TicketID != "" {
			t.Errorf("mine = %+v", got)
		}
		if got := get(t, db, "theirs"); !got.Unclaimed() {
			t.Errorf("someone else's upload was taken: %+v", got)
		}
		if got := get(t, db, "taken"); got.RunID != "r0" || got.PingID != "" {
			t.Errorf("a claimed row was re-parented: %+v", got)
		}
		if got := get(t, db, "ticketed"); got.TicketID != "t1" || got.RunID != "" || got.PingID != "" {
			t.Errorf("a ticket's row was re-parented: %+v", got)
		}
		if got := get(t, db, "nobodys"); !got.Unclaimed() {
			t.Errorf("an owner-less row went to a user: %+v", got)
		}
		// Re-claiming onto a ticket, or adding a ping afterwards, is no way in.
		if err := db.ClaimAttachments(ctx, "u1", []string{"mine"}, "t2", "", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.ClaimAttachments(ctx, "u1", []string{"taken"}, "", "r0", "p9"); err != nil {
			t.Fatal(err)
		}
		if got := get(t, db, "mine"); got.TicketID != "" || got.RunID != "r1" {
			t.Errorf("claimed twice: %+v", got)
		}
		if got := get(t, db, "taken"); got.PingID != "" {
			t.Errorf("a ping was added to a claimed row: %+v", got)
		}
		// "" is a user like any other in a claim, not a wildcard: it reaches
		// only owner-less rows.
		if err := db.ClaimAttachments(ctx, "", []string{"theirs", "nobodys"}, "", "r2", ""); err != nil {
			t.Fatal(err)
		}
		if got := get(t, db, "theirs"); !got.Unclaimed() {
			t.Errorf("the empty user took a row: %+v", got)
		}
		if got := get(t, db, "nobodys"); got.RunID != "r2" {
			t.Errorf("owner-less row = %+v", got)
		}
		for _, ids := range [][]string{nil, {}} {
			if err := db.ClaimAttachments(ctx, "u1", ids, "", "r1", ""); err != nil {
				t.Errorf("claim of no ids (%#v) = %v", ids, err)
			}
		}
	})

	t.Run("a ticket claim cannot carry a ping", func(t *testing.T) {
		db := open(t)
		ticket(t, db, "t1")
		insertAll(t, db, upload("a1", "u1", t0), upload("a2", "u1", t0), upload("a3", "u2", t0))
		// A ping rides a run. The schema says so row by row, all or nothing...
		if err := db.ClaimAttachments(ctx, "u1", []string{"a1", "a2"}, "t1", "", "p1"); err == nil {
			t.Error("a claim put a ping on a ticket")
		}
		for _, id := range []string{"a1", "a2"} {
			if got := get(t, db, id); !got.Unclaimed() || got.PingID != "" {
				t.Errorf("refused claim wrote %s: %+v", id, got)
			}
		}
		// ...so one that matches no row has nothing to refuse.
		if err := db.ClaimAttachments(ctx, "u1", []string{"a3", "ghost"}, "t1", "", "p1"); err != nil {
			t.Errorf("claim matching nothing = %v", err)
		}
	})

	// Only the foreign key can fail this; Mem has no tickets to check against.
	if seedTicket != nil {
		t.Run("a claim onto a missing ticket is refused", func(t *testing.T) {
			db := open(t)
			insertAll(t, db, upload("a1", "u1", t0), upload("a2", "u1", t0))
			if err := db.ClaimAttachments(ctx, "u1", []string{"a1", "a2"}, "ghost", "", ""); err == nil {
				t.Error("claim onto a missing ticket succeeded")
			}
			for _, id := range []string{"a1", "a2"} {
				if got := get(t, db, id); !got.Unclaimed() {
					t.Errorf("refused claim wrote %s: %+v", id, got)
				}
			}
			// The key is checked on the rows the UPDATE touched: none, no check.
			if err := db.ClaimAttachments(ctx, "u2", []string{"a1"}, "ghost", "", ""); err != nil {
				t.Errorf("claim matching nothing = %v", err)
			}
		})
	}

	t.Run("attachment readers: scope and order", func(t *testing.T) {
		db := open(t)
		ticket(t, db, "t1")
		ticket(t, db, "t2")
		// Inserted out of order: created_at, then id on a tie.
		insertAll(t, db, upload("c", "u1", t0.Add(time.Minute)), upload("b", "u1", t0), upload("a", "u1", t0),
			upload("d", "u1", t0.Add(-time.Minute)), upload("other-run", "u1", t0), upload("other-ticket", "u1", t0), upload("free", "u1", t0))
		if err := db.ClaimAttachments(ctx, "u1", []string{"a", "b"}, "", "r1", "p1"); err != nil {
			t.Fatal(err)
		}
		if err := db.ClaimAttachments(ctx, "u1", []string{"c"}, "", "r1", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.ClaimAttachments(ctx, "u1", []string{"d"}, "", "r1", "p2"); err != nil {
			t.Fatal(err)
		}
		if err := db.ClaimAttachments(ctx, "u1", []string{"other-run"}, "", "r2", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.ClaimAttachments(ctx, "u1", []string{"other-ticket"}, "t2", "", ""); err != nil {
			t.Fatal(err)
		}
		// A ping's files are the run's too.
		if got := attachmentIDs(db.AttachmentsForRun(ctx, "r1")); !slices.Equal(got, []string{"d", "a", "b", "c"}) {
			t.Errorf("for run = %v", got)
		}
		if got := attachmentIDs(db.AttachmentsForPing(ctx, "p1")); !slices.Equal(got, []string{"a", "b"}) {
			t.Errorf("for ping = %v", got)
		}
		if got := attachmentIDs(db.AttachmentsForTicket(ctx, "t2")); !slices.Equal(got, []string{"other-ticket"}) {
			t.Errorf("for ticket = %v", got)
		}
		if rows, err := db.AttachmentsForTicket(ctx, "t1"); rows != nil || err != nil {
			t.Errorf("ticket with nothing = %#v, %v", rows, err)
		}
		// "" names nothing: unclaimed rows are nobody's run, ticket or ping.
		if rows, err := db.AttachmentsForRun(ctx, ""); rows != nil || err != nil {
			t.Errorf(`for run "" = %#v, %v`, rows, err)
		}
		if rows, err := db.AttachmentsForTicket(ctx, ""); rows != nil || err != nil {
			t.Errorf(`for ticket "" = %#v, %v`, rows, err)
		}
		if rows, err := db.AttachmentsForPing(ctx, ""); rows != nil || err != nil {
			t.Errorf(`for ping "" = %#v, %v`, rows, err)
		}
	})

	t.Run("attachment deletes take what they name", func(t *testing.T) {
		db := open(t)
		ticket(t, db, "t1")
		ticket(t, db, "t2")
		insertAll(t, db, upload("goal", "u1", t0), upload("answer", "u1", t0), upload("other", "u1", t0),
			upload("tick", "u1", t0), upload("tock", "u1", t0), upload("free", "u1", t0))
		for id, owner := range map[string][3]string{
			"goal": {"", "r1", ""}, "answer": {"", "r1", "p1"}, "other": {"", "r2", ""}, "tick": {"t1", "", ""}, "tock": {"t2", "", ""},
		} {
			if err := db.ClaimAttachments(ctx, "u1", []string{id}, owner[0], owner[1], owner[2]); err != nil {
				t.Fatal(err)
			}
		}
		left := func() []string {
			t.Helper()
			var out []string
			for _, id := range []string{"goal", "answer", "other", "tick", "tock", "free"} {
				if _, err := db.GetAttachment(ctx, id); err == nil {
					out = append(out, id)
				} else if !errors.Is(err, filestore.ErrNotFound) {
					t.Fatal(err)
				}
			}
			return out
		}
		// "" and an unknown owner match nothing, least of all unclaimed rows.
		for _, id := range []string{"", "ghost"} {
			if err := db.DeleteAttachmentsForRun(ctx, id); err != nil {
				t.Errorf("delete for run %q = %v", id, err)
			}
			if err := db.DeleteAttachmentsForTicket(ctx, id); err != nil {
				t.Errorf("delete for ticket %q = %v", id, err)
			}
		}
		if got := left(); len(got) != 6 {
			t.Fatalf("deleting nothing took rows: %v", got)
		}
		// A run takes its ping files, and only its own.
		if err := db.DeleteAttachmentsForRun(ctx, "r1"); err != nil {
			t.Fatal(err)
		}
		if got := left(); !slices.Equal(got, []string{"other", "tick", "tock", "free"}) {
			t.Errorf("after run delete = %v", got)
		}
		if rows, _ := db.AttachmentsForPing(ctx, "p1"); rows != nil {
			t.Errorf("ping files survived their run: %+v", rows)
		}
		if err := db.DeleteAttachmentsForTicket(ctx, "t1"); err != nil {
			t.Fatal(err)
		}
		if got := left(); !slices.Equal(got, []string{"other", "tock", "free"}) {
			t.Errorf("after ticket delete = %v", got)
		}
		// By id: unknown ids are ignored, nothing listed is nothing done, and
		// a row goes whatever owns it.
		for _, ids := range [][]string{nil, {}} {
			if err := db.DeleteAttachments(ctx, ids); err != nil {
				t.Errorf("delete of no ids (%#v) = %v", ids, err)
			}
		}
		if err := db.DeleteAttachments(ctx, []string{"other", "free", "ghost"}); err != nil {
			t.Fatal(err)
		}
		if got := left(); !slices.Equal(got, []string{"tock"}) {
			t.Errorf("after delete by id = %v", got)
		}
	})

	t.Run("orphan listing", func(t *testing.T) {
		db := open(t)
		ticket(t, db, "t1")
		insertAll(t, db, upload("old-u1", "u1", t0), upload("old-u2", "u2", t0), upload("young-u1", "u1", t0.Add(2*time.Hour)),
			upload("old-run", "u1", t0), upload("old-ticket", "u1", t0), upload("old-nobody", "", t0))
		if err := db.ClaimAttachments(ctx, "u1", []string{"old-run"}, "", "r1", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.ClaimAttachments(ctx, "u1", []string{"old-ticket"}, "t1", "", ""); err != nil {
			t.Fatal(err)
		}
		// The SQL has no ORDER BY, so the suite sorts: order is not promised.
		orphans := func(user string, cutoff time.Time) []string {
			t.Helper()
			ids, err := db.UnclaimedAttachmentIDsBefore(ctx, user, cutoff)
			if err != nil {
				t.Fatal(err)
			}
			slices.Sort(ids)
			return ids
		}
		cutoff := t0.Add(time.Hour)
		// "" is everyone's, unlike in a claim.
		if got := orphans("", cutoff); !slices.Equal(got, []string{"old-nobody", "old-u1", "old-u2"}) {
			t.Errorf("everyone's = %v", got)
		}
		if got := orphans("u1", cutoff); !slices.Equal(got, []string{"old-u1"}) {
			t.Errorf("u1's = %v", got)
		}
		if got := orphans("", t0.Add(3*time.Hour)); !slices.Equal(got, []string{"old-nobody", "old-u1", "old-u2", "young-u1"}) {
			t.Errorf("later cutoff = %v", got)
		}
		// The cutoff is exclusive.
		if got := orphans("", t0); got != nil {
			t.Errorf("at the cutoff = %#v", got)
		}
		if got := orphans("", t0.Add(time.Microsecond)); len(got) != 3 {
			t.Errorf("a microsecond past = %v", got)
		}
		// Both sides of the comparison are in microseconds: nanoseconds apart
		// is the same instant, so not yet before it.
		if got := orphans("", t0.Add(999*time.Nanosecond)); got != nil {
			t.Errorf("under a microsecond past = %#v", got)
		}
		if got := orphans("ghost", cutoff); got != nil {
			t.Errorf("a user with nothing = %#v", got)
		}
		late := upload("nanos", "u3", t0.Add(100*time.Nanosecond))
		insertAll(t, db, late)
		if got := orphans("u3", t0.Add(900*time.Nanosecond)); got != nil {
			t.Errorf("within one microsecond = %#v", got)
		}
	})

	t.Run("snapshot roundtrip, each kind", func(t *testing.T) {
		db := open(t)
		output := filestore.Snapshot{ID: "s1", RunID: "r1", Kind: filestore.KindOutput, StageID: "plan", Iteration: 3,
			Name: "plan", Path: "artifacts/plan.md", Filename: "plan.md", Mime: "text/markdown", Size: 1 << 40, SHA256: "ab", Truncated: true, CreatedAt: t0}
		log := logOf("s2", "r1", "_provision", 0, t0)
		ping := pingFile("s3", "r1", "p1", t0)
		captureAll(t, db, output, log, ping)
		for _, want := range []filestore.Snapshot{output, log, ping} {
			got, err := db.GetSnapshot(ctx, want.ID)
			want.CreatedAt = asRead(want.CreatedAt)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("roundtrip = %+v, %v, want %+v", got, err, want)
			}
		}
	})

	t.Run("snapshot timestamps are stored in microseconds", func(t *testing.T) {
		db := open(t)
		at := time.Date(2026, 3, 4, 7, 6, 7, 999_999_999, time.FixedZone("elsewhere", 2*60*60))
		captureAll(t, db, logOf("s1", "r1", "b", 0, at))
		got, err := db.GetSnapshot(ctx, "s1")
		if want := asRead(at); err != nil || !reflect.DeepEqual(got.CreatedAt, want) {
			t.Errorf("created_at = %v, %v, want %v", got.CreatedAt, err, want)
		}
	})

	t.Run("snapshots are insert-only", func(t *testing.T) {
		db := open(t)
		captureAll(t, db, logOf("s1", "r1", "b", 0, t0))
		again := logOf("s1", "r2", "c", 4, t0.Add(time.Hour))
		if err := db.InsertSnapshot(ctx, again); err == nil {
			t.Error("a duplicate id was stored")
		}
		if got, err := db.GetSnapshot(ctx, "s1"); err != nil || got.RunID != "r1" || got.StageID != "b" {
			t.Errorf("duplicate insert overwrote the row: %+v, %v", got, err)
		}
	})

	t.Run("snapshot attribution is the schema's", func(t *testing.T) {
		db := open(t)
		good := filestore.Snapshot{ID: "bad", RunID: "r1", StageID: "b", Filename: "f", Mime: "m", SHA256: "aa", CreatedAt: t0}
		for name, s := range map[string]func(*filestore.Snapshot){
			"no kind":             func(s *filestore.Snapshot) {},
			"unknown kind":        func(s *filestore.Snapshot) { s.Kind = "bogus" },
			"output without name": func(s *filestore.Snapshot) { s.Kind, s.Path = filestore.KindOutput, "p" },
			"output without path": func(s *filestore.Snapshot) { s.Kind, s.Name = filestore.KindOutput, "n" },
			"output with ping":    func(s *filestore.Snapshot) { s.Kind, s.Name, s.Path, s.PingID = filestore.KindOutput, "n", "p", "x" },
			"log with name":       func(s *filestore.Snapshot) { s.Kind, s.Name = filestore.KindLog, "n" },
			"log with path":       func(s *filestore.Snapshot) { s.Kind, s.Path = filestore.KindLog, "p" },
			"log with ping":       func(s *filestore.Snapshot) { s.Kind, s.PingID = filestore.KindLog, "x" },
			"ping file, no ping":  func(s *filestore.Snapshot) { s.Kind, s.Path = filestore.KindPingFile, "p" },
			"ping file, no path":  func(s *filestore.Snapshot) { s.Kind, s.PingID = filestore.KindPingFile, "x" },
			"ping file with name": func(s *filestore.Snapshot) { s.Kind, s.PingID, s.Path, s.Name = filestore.KindPingFile, "x", "p", "n" },
		} {
			bad := good
			s(&bad)
			if err := db.InsertSnapshot(ctx, bad); err == nil {
				t.Errorf("%s: stored", name)
			}
		}
		if _, err := db.GetSnapshot(ctx, "bad"); !errors.Is(err, filestore.ErrNotFound) {
			t.Errorf("a refused row is readable: %v", err)
		}
	})

	t.Run("snapshot not found", func(t *testing.T) {
		db := open(t)
		if _, err := db.GetSnapshot(ctx, "nope"); !errors.Is(err, filestore.ErrNotFound) {
			t.Errorf("missing = %v, want ErrNotFound", err)
		}
		if rows, err := db.SnapshotsForRun(ctx, "nope"); rows != nil || err != nil {
			t.Errorf("for run = %#v, %v", rows, err)
		}
		if rows, err := db.SnapshotsForPing(ctx, "nope"); rows != nil || err != nil {
			t.Errorf("for ping = %#v, %v", rows, err)
		}
	})

	t.Run("snapshots for a run: stage, iteration, time, id", func(t *testing.T) {
		db := open(t)
		captureAll(t, db,
			logOf("z", "r1", "plan", 2, t0),
			logOf("y", "r1", "plan", 10, t0), // numeric: 10 sorts after 2
			logOf("x", "r1", "build", 1, t0.Add(time.Hour)),
			logOf("c", "r1", "plan", 1, t0.Add(time.Minute)),
			logOf("b", "r1", "plan", 1, t0),
			logOf("a", "r1", "plan", 1, t0),
			logOf("elsewhere", "r2", "plan", 1, t0),
		)
		want := []string{"x", "a", "b", "c", "z", "y"}
		if got := snapshotIDs(db.SnapshotsForRun(ctx, "r1")); !slices.Equal(got, want) {
			t.Errorf("for run = %v, want %v", got, want)
		}
	})

	// stage_id sorts by the database's collation, en_US.utf8 on the image
	// every deployment and pgtest run: punctuation does not weigh, so the
	// real _provision stage lands among the p's, not first as in byte order.
	// Ids that differ only in punctuation (plan-2, plan2) are left out: their
	// order is glibc's position rule, which Mem does not reproduce.
	t.Run("snapshot stages sort by collation", func(t *testing.T) {
		db := open(t)
		stages := []string{"test", "review_3", "_provision", "review-2", "plan2", "plan", "fix-tests", "build", "fixup"}
		for i, stage := range stages {
			captureAll(t, db, logOf("s"+stage, "r1", stage, 0, t0.Add(time.Duration(i)*time.Second)))
		}
		var got []string
		rows, err := db.SnapshotsForRun(ctx, "r1")
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range rows {
			got = append(got, s.StageID)
		}
		want := []string{"build", "fix-tests", "fixup", "plan", "plan2", "_provision", "review-2", "review_3", "test"}
		if !slices.Equal(got, want) {
			t.Errorf("stages = %v, want %v", got, want)
		}
	})

	t.Run("snapshots for a ping: time, id, across runs", func(t *testing.T) {
		db := open(t)
		captureAll(t, db,
			pingFile("c", "r1", "p1", t0.Add(time.Minute)),
			pingFile("b", "r2", "p1", t0),
			pingFile("a", "r1", "p1", t0),
			pingFile("elsewhere", "r1", "p2", t0),
			logOf("log", "r1", "plan", 0, t0),
		)
		if got := snapshotIDs(db.SnapshotsForPing(ctx, "p1")); !slices.Equal(got, []string{"a", "b", "c"}) {
			t.Errorf("for ping = %v", got)
		}
		// Rows without a ping are nobody's: "" finds none of them.
		if rows, err := db.SnapshotsForPing(ctx, ""); rows != nil || err != nil {
			t.Errorf(`for ping "" = %#v, %v`, rows, err)
		}
	})
}
