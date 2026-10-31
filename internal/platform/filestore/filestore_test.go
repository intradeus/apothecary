package filestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStores(t *testing.T) (*Attachments, *Snapshots, *Mem, *Blobs) {
	t.Helper()
	mem := NewMem()
	blobs := NewBlobs(t.TempDir())
	att := NewAttachments(mem, blobs, 1<<20, []string{"text/*", "image/png"})
	snaps := NewSnapshots(mem, blobs, 1<<20)
	return att, snaps, mem, blobs
}

var ident = func(s string) string { return s }

// --- attachments ------------------------------------------------------------

func TestSaveLandsUnclaimedAndDedups(t *testing.T) {
	att, _, _, blobs := newStores(t)
	ctx := context.Background()

	a1, err := att.Save(ctx, "", "spec.md", "text/markdown", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if !a1.Unclaimed() || a1.Size != 5 || a1.SHA256 == "" {
		t.Fatalf("a1 = %+v", a1)
	}
	// Identical content from a second upload: new row, SAME blob.
	a2, err := att.Save(ctx, "", "copy.md", "text/markdown", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if a2.ID == a1.ID {
		t.Error("rows must be distinct")
	}
	if a2.SHA256 != a1.SHA256 {
		t.Errorf("dedup broken: %s vs %s", a1.SHA256, a2.SHA256)
	}
	if n := countBlobs(t, blobs); n != 1 {
		t.Errorf("blob count = %d, want 1 (content-addressed)", n)
	}
	data, err := blobs.Bytes(a1.SHA256)
	if err != nil || string(data) != "hello" {
		t.Fatalf("blob bytes = %q, %v", data, err)
	}
}

func TestSavePolicies(t *testing.T) {
	att, _, _, _ := newStores(t)
	ctx := context.Background()

	if _, err := att.Save(ctx, "", "big.txt", "text/plain",
		strings.NewReader(strings.Repeat("x", 1<<20+1))); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize err = %v, want ErrTooLarge", err)
	}
	if _, err := att.Save(ctx, "", "app.zip", "application/zip",
		strings.NewReader("PK...")); !errors.Is(err, ErrMimeType) {
		t.Errorf("off-list err = %v, want ErrMimeType", err)
	}
}

func TestClaimIsOneShot(t *testing.T) {
	att, _, _, _ := newStores(t)
	ctx := context.Background()

	a, err := att.Save(ctx, "", "spec.md", "text/markdown", strings.NewReader("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if err := att.ClaimForTicket(ctx, []string{a.ID}, "tick-1"); err != nil {
		t.Fatal(err)
	}
	// A later claim (a run trying to steal the ticket's file) is a no-op.
	if err := att.ClaimForRun(ctx, []string{a.ID}, "run-1", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := att.Get(ctx, a.ID)
	if got.TicketID != "tick-1" || got.RunID != "" {
		t.Fatalf("attachment re-parented: %+v", got)
	}

	// Answer claim carries run+ping together.
	b, _ := att.Save(ctx, "", "note.txt", "text/plain", strings.NewReader("n"))
	if err := att.ClaimForRun(ctx, []string{b.ID}, "run-1", "ping-1"); err != nil {
		t.Fatal(err)
	}
	byPing, _ := att.ForPing(ctx, "ping-1")
	if len(byPing) != 1 || byPing[0].ID != b.ID {
		t.Fatalf("ForPing = %+v", byPing)
	}
	byRun, _ := att.ForRun(ctx, "run-1")
	if len(byRun) != 1 {
		t.Fatalf("ForRun = %+v", byRun)
	}
}

func TestDeleteForTicketAndSweepReclaimBlobs(t *testing.T) {
	att, _, mem, blobs := newStores(t)
	ctx := context.Background()

	a, _ := att.Save(ctx, "", "doc.md", "text/markdown", strings.NewReader("ticket file"))
	if err := att.ClaimForTicket(ctx, []string{a.ID}, "tick-1"); err != nil {
		t.Fatal(err)
	}
	// The old world's leak: ticket deleted, blob immortal. Now: rows go...
	if err := att.DeleteForTicket(ctx, "tick-1"); err != nil {
		t.Fatal(err)
	}
	if rows, _ := att.ForTicket(ctx, "tick-1"); len(rows) != 0 {
		t.Fatalf("rows survived ticket delete: %+v", rows)
	}
	// ...and the sweep reclaims the now-unreferenced blob (aged past grace).
	ageBlobs(t, blobs)
	live := liveSHAs(mem)
	removed, err := blobs.Sweep(live)
	if err != nil || removed != 1 {
		t.Fatalf("sweep removed %d (%v), want 1", removed, err)
	}
	if _, err := blobs.Bytes(a.SHA256); !errors.Is(err, ErrContentGone) {
		t.Errorf("swept blob read err = %v, want ErrContentGone", err)
	}
}

func TestSweepKeepsReferencedAndFreshBlobs(t *testing.T) {
	att, _, mem, blobs := newStores(t)
	ctx := context.Background()

	kept, _ := att.Save(ctx, "", "kept.md", "text/markdown", strings.NewReader("kept"))
	_ = att.ClaimForTicket(ctx, []string{kept.ID}, "tick-1")

	// An unreferenced blob (rejected upload) — but FRESH: the grace window
	// protects in-flight writes, so the first sweep must leave it.
	if _, err := att.Save(ctx, "", "app.zip", "application/zip", strings.NewReader("zipzip")); !errors.Is(err, ErrMimeType) {
		t.Fatal("setup: want a rejected upload leaving a stray blob")
	}
	if removed, _ := blobs.Sweep(liveSHAs(mem)); removed != 0 {
		t.Fatalf("fresh blobs swept: %d", removed)
	}
	// Aged past grace, the stray goes and the referenced one stays.
	ageBlobs(t, blobs)
	removed, err := blobs.Sweep(liveSHAs(mem))
	if err != nil || removed != 1 {
		t.Fatalf("aged sweep removed %d (%v), want 1", removed, err)
	}
	if _, err := blobs.Bytes(kept.SHA256); err != nil {
		t.Errorf("referenced blob swept: %v", err)
	}
}

func TestGCOrphans(t *testing.T) {
	att, _, _, _ := newStores(t)
	ctx := context.Background()

	old, _ := att.Save(ctx, "", "old.txt", "text/plain", strings.NewReader("old"))
	claimed, _ := att.Save(ctx, "", "claimed.txt", "text/plain", strings.NewReader("c"))
	_ = att.ClaimForRun(ctx, []string{claimed.ID}, "run-1", "")

	// Age both rows, then GC: only the unclaimed one goes.
	att.now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	n, err := att.GCOrphans(ctx, "", 24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("gc = %d (%v), want 1", n, err)
	}
	if _, err := att.Get(ctx, old.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("orphan survived: %v", err)
	}
	if _, err := att.Get(ctx, claimed.ID); err != nil {
		t.Errorf("claimed row swept: %v", err)
	}
}

func TestOpenVerifiesContent(t *testing.T) {
	att, _, _, blobs := newStores(t)
	ctx := context.Background()

	a, _ := att.Save(ctx, "", "doc.txt", "text/plain", strings.NewReader("integrity"))
	f, got, err := att.Open(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	n, _ := f.Read(buf)
	f.Close()
	if string(buf[:n]) != "integrity" || got.Filename != "doc.txt" {
		t.Fatalf("read %q, meta %+v", buf[:n], got)
	}

	// Corrupt the blob in place: mismatched bytes must never be served.
	if err := os.WriteFile(blobs.path(a.SHA256), []byte("tampered!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := att.Open(ctx, a.ID); !errors.Is(err, ErrContentCorrupt) {
		t.Errorf("corrupt open err = %v, want ErrContentCorrupt", err)
	}
}

// --- snapshots --------------------------------------------------------------

func TestCaptureKinds(t *testing.T) {
	_, snaps, _, _ := newStores(t)
	ctx := context.Background()

	out, err := snaps.Capture(ctx, Snapshot{
		RunID: "r1", Kind: KindOutput, StageID: "plan", Iteration: 1,
		Name: "plan", Path: "artifacts/plan.md", Filename: "plan.md",
	}, ident, []byte("# plan"))
	if err != nil {
		t.Fatal(err)
	}
	if out.SHA256 == "" || out.Size != 6 || out.Truncated {
		t.Fatalf("output snap = %+v", out)
	}

	if _, err := snaps.Capture(ctx, Snapshot{
		RunID: "r1", Kind: KindLog, StageID: "plan", Iteration: 1, Filename: "plan-1.log",
	}, ident, []byte("log line")); err != nil {
		t.Fatal(err)
	}

	pf, err := snaps.Capture(ctx, Snapshot{
		RunID: "r1", Kind: KindPingFile, StageID: "plan", PingID: "ping-1",
		Path: "artifacts/dbs.md", Filename: "dbs.md",
	}, ident, []byte("as reviewed"))
	if err != nil {
		t.Fatal(err)
	}
	byPing, _ := snaps.ForPing(ctx, "ping-1")
	if len(byPing) != 1 || byPing[0].ID != pf.ID || byPing[0].Path != "artifacts/dbs.md" {
		t.Fatalf("ForPing = %+v", byPing)
	}

	byRun, _ := snaps.ForRun(ctx, "r1")
	if len(byRun) != 3 {
		t.Fatalf("ForRun = %d rows", len(byRun))
	}
}

// Incoherent attributions fail at the constructor, mirroring the DB CHECKs.
func TestCaptureRefusesIncoherentAttribution(t *testing.T) {
	_, snaps, _, _ := newStores(t)
	ctx := context.Background()
	for name, snap := range map[string]Snapshot{
		"output without role":  {RunID: "r", Kind: KindOutput, StageID: "s", Path: "a/b"},
		"output with ping":     {RunID: "r", Kind: KindOutput, StageID: "s", Name: "n", Path: "a/b", PingID: "p"},
		"log with path":        {RunID: "r", Kind: KindLog, StageID: "s", Path: "a/b"},
		"ping file without id": {RunID: "r", Kind: KindPingFile, StageID: "s", Path: "a/b"},
		"ping file with role":  {RunID: "r", Kind: KindPingFile, StageID: "s", PingID: "p", Path: "a/b", Name: "n"},
		"unknown kind":         {RunID: "r", Kind: "weird", StageID: "s"},
		"no stage":             {RunID: "r", Kind: KindLog},
	} {
		if _, err := snaps.Capture(ctx, snap, ident, []byte("x")); err == nil {
			t.Errorf("%s: captured without error", name)
		}
	}
}

// Scrubbing is constitutive: the scrub function is required and applied.
func TestCaptureScrubs(t *testing.T) {
	_, snaps, _, _ := newStores(t)
	ctx := context.Background()

	if _, err := snaps.Capture(ctx, Snapshot{
		RunID: "r", Kind: KindLog, StageID: "s", Iteration: 1,
	}, nil, []byte("x")); err == nil {
		t.Fatal("nil scrub accepted")
	}
	scrub := func(s string) string { return strings.ReplaceAll(s, "sk-secret", "[REDACTED]") }
	snap, err := snaps.Capture(ctx, Snapshot{
		RunID: "r", Kind: KindLog, StageID: "s", Iteration: 1, Filename: "s-1.log",
	}, scrub, []byte("token sk-secret leaked"))
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := snaps.Bytes(ctx, snap.ID)
	if err != nil || strings.Contains(string(data), "sk-secret") {
		t.Fatalf("stored %q (%v), want redacted", data, err)
	}
}

func TestCaptureTruncatesWithMarkerAndFlag(t *testing.T) {
	mem := NewMem()
	blobs := NewBlobs(t.TempDir())
	snaps := NewSnapshots(mem, blobs, 100)

	long := strings.Repeat("y", 500) + "TAIL"
	snap, err := snaps.Capture(context.Background(), Snapshot{
		RunID: "r", Kind: KindLog, StageID: "s", Iteration: 1, Filename: "s.log",
	}, ident, []byte(long))
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Truncated {
		t.Error("Truncated flag not set")
	}
	if snap.Size > 100 {
		t.Errorf("size %d exceeds cap", snap.Size)
	}
	data, _, _ := snaps.Bytes(context.Background(), snap.ID)
	if !strings.HasPrefix(string(data), truncationMarker) || !strings.HasSuffix(string(data), "TAIL") {
		t.Errorf("truncation kept %q", data)
	}
}

// Identical re-captures (an unchanged plan.md across retries) share one blob.
func TestCaptureDedupsAcrossAttempts(t *testing.T) {
	_, snaps, _, blobs := newStores(t)
	ctx := context.Background()
	for iter := 1; iter <= 3; iter++ {
		if _, err := snaps.Capture(ctx, Snapshot{
			RunID: "r", Kind: KindOutput, StageID: "plan", Iteration: iter,
			Name: "plan", Path: "artifacts/plan.md", Filename: "plan.md",
		}, ident, []byte("same plan every time")); err != nil {
			t.Fatal(err)
		}
	}
	if n := countBlobs(t, blobs); n != 1 {
		t.Errorf("blob count = %d, want 1", n)
	}
	rows, _ := snaps.ForRun(ctx, "r")
	if len(rows) != 3 {
		t.Errorf("row count = %d, want 3 (each attempt is a record)", len(rows))
	}
}

// Retention deletes bytes, never records: after the sweep the row still
// answers with metadata and the read reports content-gone.
func TestSnapshotContentExpiry(t *testing.T) {
	_, snaps, mem, blobs := newStores(t)
	ctx := context.Background()

	snap, _ := snaps.Capture(ctx, Snapshot{
		RunID: "r", Kind: KindLog, StageID: "s", Iteration: 1, Filename: "s.log",
	}, ident, []byte("old log"))
	ageBlobs(t, blobs)

	// Retention window excludes this snapshot: its sha drops out of the live
	// set (attachments keep theirs unconditionally).
	live := liveSHAsWithCutoff(mem, time.Now().Add(time.Hour))
	if _, err := blobs.Sweep(live); err != nil {
		t.Fatal(err)
	}
	got, err := snaps.Get(ctx, snap.ID)
	if err != nil || got.SHA256 != snap.SHA256 || got.Size != snap.Size {
		t.Fatalf("metadata lost: %+v (%v)", got, err)
	}
	if _, _, err := snaps.Open(ctx, snap.ID); !errors.Is(err, ErrContentGone) {
		t.Errorf("expired open err = %v, want ErrContentGone", err)
	}
}

// --- helpers ----------------------------------------------------------------

func countBlobs(t *testing.T, b *Blobs) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(b.dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasPrefix(d.Name(), "put-") {
			n++
		}
		return nil
	})
	return n
}

// ageBlobs pushes every blob's mtime past the sweep grace window.
func ageBlobs(t *testing.T, b *Blobs) {
	t.Helper()
	past := time.Now().Add(-2 * sweepGrace)
	_ = filepath.WalkDir(b.dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			_ = os.Chtimes(path, past, past)
		}
		return nil
	})
}

// liveSHAs mirrors pgstore.LiveBlobSHAs over the Mem: attachments ∪ snapshots.
func liveSHAs(m *Mem) map[string]bool { return liveSHAsWithCutoff(m, time.Time{}) }

func liveSHAsWithCutoff(m *Mem, keepSnapshotsAfter time.Time) map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	live := map[string]bool{}
	for _, a := range m.attachments {
		live[a.SHA256] = true
	}
	for _, s := range m.snapshots {
		if !s.CreatedAt.Before(keepSnapshotsAfter) {
			live[s.SHA256] = true
		}
	}
	return live
}
