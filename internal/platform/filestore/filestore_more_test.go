package filestore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":             "report.pdf",
		"../../etc/passwd":       "passwd",
		"/abs/path/a.txt":        "a.txt",
		`C:\Users\me\a.txt`:      "C--Users-me-a.txt",
		`quo"te.txt`:             "quo-te.txt",
		"new\r\nline.txt":        "new--line.txt",
		`a<b>c|d?e*f:g.txt`:      "a-b-c-d-e-f-g.txt",
		"":                       "file",
		".":                      "file",
		"..":                     "file",
		"...":                    "file",
		"   ":                    "file",
		".hidden":                "hidden",
		"trailing.":              "trailing",
		"日本語.md":                 "日本語.md",
		strings.Repeat("a", 200): strings.Repeat("a", 128),
	} {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
	// A long name keeps its end, where the extension is.
	long := SafeName(strings.Repeat("a", 200) + ".tar.gz")
	if len(long) != 128 || !strings.HasSuffix(long, ".tar.gz") {
		t.Errorf("long name = %q", long)
	}
}

// The stored filename goes into a Postgres text column and a response header.
func TestSafeNameStaysValidUTF8(t *testing.T) {
	if got := SafeName(strings.Repeat("é", 100) + "x.txt"); !utf8.ValidString(got) {
		t.Errorf("SafeName produced invalid UTF-8: %q", got)
	}
}

func TestDetectMime(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	png := write("p", []byte("\x89PNG\r\n\x1a\n"))
	opaque := write("o", []byte{0, 1, 2, 3, 4})

	for _, c := range []struct{ name, path, filename, declared, want string }{
		{"a specific declaration wins", opaque, "a.bin", "application/pdf; q=1", "application/pdf"},
		{"octet-stream is no declaration", png, "a", "application/octet-stream", "image/png"},
		{"nothing declared: sniff", png, "a", "", "image/png"},
		{"unsniffable: the extension", opaque, "a.json", "", "application/json"},
		{"unsniffable, no extension", opaque, "a", "", "application/octet-stream"},
		{"unknown extension", opaque, "a.nosuchext", "", "application/octet-stream"},
		{"unreadable file: the extension", filepath.Join(dir, "missing"), "a.json", "", "application/json"},
	} {
		if got := detectMime(c.path, c.filename, c.declared); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDetectMimeBytes(t *testing.T) {
	if got := detectMimeBytes([]byte("{}"), "a.json"); got != "application/json" {
		t.Errorf("by extension = %q", got)
	}
	if got := detectMimeBytes([]byte("plain words"), "a.nosuchext"); got != "text/plain" {
		t.Errorf("by content = %q", got)
	}
	if got := detectMimeBytes([]byte("plain words"), "noext"); got != "text/plain" {
		t.Errorf("no extension = %q", got)
	}
	big := bytes.Repeat([]byte("x"), 2048)
	if got := detectMimeBytes(big, "noext"); got != "text/plain" {
		t.Errorf("more than the sniff window = %q", got)
	}
}

func TestMimeAllowlist(t *testing.T) {
	allow := mimeAllowFunc([]string{" text/* ", "image/PNG", ""})
	for m, want := range map[string]bool{
		"text/plain": true, "TEXT/HTML; charset=x": true, "image/png": true, "IMAGE/PNG": true,
		"image/gif": false, "textual/plain": false, "": false, "application/pdf": false,
	} {
		if got := allow(m); got != want {
			t.Errorf("allow(%q) = %v, want %v", m, got, want)
		}
	}
}

func TestLimitsMirrorThePolicy(t *testing.T) {
	a := NewAttachments(NewMem(), NewBlobs(t.TempDir()), 42, []string{"text/*"})
	l := a.Limits()
	if l.MaxBytes != 42 || l.MaxFiles != MaxUploadFiles || len(l.MimeAllow) != 1 || l.MimeAllow[0] != "text/*" {
		t.Errorf("limits = %+v", l)
	}
}

func TestValidSHA(t *testing.T) {
	good := strings.Repeat("a1", 32)
	for sha, want := range map[string]bool{
		good: true, "": false, "abc": false, strings.Repeat("A", 64): false,
		strings.Repeat("g", 64): false, "../" + good[3:]: false,
	} {
		if got := validSHA(sha); got != want {
			t.Errorf("validSHA(%q) = %v, want %v", sha, got, want)
		}
	}
}

// A reference that is not a sha never reaches the filesystem.
func TestOpenRefusesABadReference(t *testing.T) {
	b := NewBlobs(t.TempDir())
	for _, sha := range []string{"", "../../etc/passwd", strings.Repeat("Z", 64)} {
		if _, err := b.Open(sha); !errors.Is(err, ErrContentGone) {
			t.Errorf("Open(%q) = %v", sha, err)
		}
	}
	if _, err := b.Bytes(strings.Repeat("0", 64)); !errors.Is(err, ErrContentGone) {
		t.Errorf("Bytes of nothing = %v", err)
	}
}

func TestPutIsIdempotent(t *testing.T) {
	b := NewBlobs(t.TempDir())
	a, err := b.Put([]byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.Put([]byte("same"))
	if err != nil || a != c {
		t.Errorf("second put = %s, %v", c, err)
	}
	if got, err := b.Bytes(a); err != nil || string(got) != "same" {
		t.Errorf("read back = %q, %v", got, err)
	}
}

// A store rooted somewhere that cannot exist fails loudly rather than losing
// the bytes.
func TestWritesFailOnAnUnusableRoot(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	b := NewBlobs(file) // <file>/blobs cannot be made
	if _, err := b.Put([]byte("x")); err == nil {
		t.Error("Put succeeded under a file")
	}
	if _, _, err := b.PutStream(strings.NewReader("x"), 10); err == nil {
		t.Error("PutStream succeeded under a file")
	}
	if _, err := b.Sweep(nil); err == nil {
		t.Error("Sweep of an unreadable root reported nothing")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("the client went away") }

// A stream that dies leaves no temp file behind.
func TestPutStreamLeavesNothingWhenTheReaderFails(t *testing.T) {
	b := NewBlobs(t.TempDir())
	if _, _, err := b.PutStream(failingReader{}, 10); err == nil || !strings.Contains(err.Error(), "went away") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := b.PutStream(strings.NewReader("too large"), 3); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the cap = %v", err)
	}
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
}

// Exactly the cap fits: the cap is what the policy says, not one byte under.
func TestPutStreamBoundary(t *testing.T) {
	b := NewBlobs(t.TempDir())
	if _, n, err := b.PutStream(strings.NewReader("1234"), 4); err != nil || n != 4 {
		t.Errorf("at the cap = %d, %v", n, err)
	}
	// Twice: the second is the dedup path, and reports the same size.
	if _, n, err := b.PutStream(strings.NewReader("1234"), 4); err != nil || n != 4 {
		t.Errorf("dedup = %d, %v", n, err)
	}
}

func TestSweepAgesOutStrayTempFiles(t *testing.T) {
	b := NewBlobs(t.TempDir())
	if _, err := b.Put([]byte("x")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * sweepGrace)
	stale, fresh, other := filepath.Join(b.dir, "put-old"), filepath.Join(b.dir, "put-new"), filepath.Join(b.dir, "unrelated")
	for _, p := range []string{stale, fresh, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{stale, other} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Sweep(map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temp file survived: %v", err)
	}
	for _, p := range []string{fresh, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s went: %v", p, err)
		}
	}
}

func TestSweepOfNothing(t *testing.T) {
	b := NewBlobs(t.TempDir()) // <dir>/blobs was never made
	if n, err := b.Sweep(nil); n != 0 || err != nil {
		t.Errorf("sweep = %d, %v", n, err)
	}
}

// brokenDB fails what the stores write; reads reach the Mem.
type brokenDB struct {
	*Mem
	err error
}

func (b brokenDB) InsertAttachment(context.Context, Attachment) error { return b.err }
func (b brokenDB) InsertSnapshot(context.Context, Snapshot) error     { return b.err }
func (b brokenDB) UnclaimedAttachmentIDsBefore(context.Context, string, time.Time) ([]string, error) {
	return nil, b.err
}
func (b brokenDB) DeleteAttachments(context.Context, []string) error { return b.err }

func TestStoreFailuresPropagate(t *testing.T) {
	boom := errors.New("db down")
	ctx := context.Background()
	blobs := NewBlobs(t.TempDir())
	att := NewAttachments(brokenDB{NewMem(), boom}, blobs, 1<<20, []string{"text/*"})
	if _, err := att.Save(ctx, "u", "a.txt", "text/plain", strings.NewReader("x")); !errors.Is(err, boom) {
		t.Errorf("Save = %v", err)
	}
	if _, err := att.GCOrphans(ctx, "", time.Hour); !errors.Is(err, boom) {
		t.Errorf("GCOrphans list = %v", err)
	}
	snaps := NewSnapshots(brokenDB{NewMem(), boom}, blobs, 1<<20)
	_, err := snaps.Capture(ctx, Snapshot{RunID: "r", StageID: "s", Kind: KindLog}, func(s string) string { return s }, []byte("x"))
	if !errors.Is(err, boom) {
		t.Errorf("Capture = %v", err)
	}
}

// deleteFails lets the list through and fails the delete: nothing is reported
// as collected.
type deleteFails struct {
	*Mem
	err error
}

func (d deleteFails) DeleteAttachments(context.Context, []string) error { return d.err }

func TestGCOrphansReportsAFailedDelete(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("db down")
	mem := NewMem()
	att := NewAttachments(deleteFails{mem, boom}, NewBlobs(t.TempDir()), 1<<20, []string{"text/*"})
	if _, err := att.Save(ctx, "u", "a.txt", "text/plain", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	att.now = func() time.Time { return time.Now().Add(time.Hour * 24) }
	if n, err := att.GCOrphans(ctx, "u", time.Hour); n != 0 || !errors.Is(err, boom) {
		t.Errorf("GCOrphans = %d, %v", n, err)
	}
}

func TestClaimNeedsExactlyOneOwner(t *testing.T) {
	ctx := context.Background()
	m := NewMem()
	if err := m.ClaimAttachments(ctx, "u", []string{"x"}, "", "", ""); err == nil {
		t.Error("a claim with no owner was accepted")
	}
	if err := m.ClaimAttachments(ctx, "u", []string{"x"}, "t", "r", ""); err == nil {
		t.Error("a claim with two owners was accepted")
	}
	att := NewAttachments(m, NewBlobs(t.TempDir()), 1<<20, []string{"text/*"})
	if err := att.ClaimForTicket(ctx, "u", nil, "t"); err != nil {
		t.Errorf("no ids for a ticket = %v", err)
	}
	if err := att.ClaimForRun(ctx, "u", nil, "r", ""); err != nil {
		t.Errorf("no ids for a run = %v", err)
	}
}

// The lifecycle of an upload's rows, by owner: bytes come back, a run's rows
// go with it, the ping link holds.
func TestAttachmentOwnersAndBytes(t *testing.T) {
	ctx := context.Background()
	att, _, mem, _ := newStores(t)
	save := func(name, body string) Attachment {
		a, err := att.Save(ctx, "u", name, "text/plain", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	goal, answer, ticket := save("goal.txt", "goal"), save("answer.txt", "answer"), save("ticket.txt", "ticket")
	if err := att.ClaimForRun(ctx, "u", []string{goal.ID}, "r1", ""); err != nil {
		t.Fatal(err)
	}
	if err := att.ClaimForRun(ctx, "u", []string{answer.ID}, "r1", "p1"); err != nil {
		t.Fatal(err)
	}
	if err := att.ClaimForTicket(ctx, "u", []string{ticket.ID}, "t1"); err != nil {
		t.Fatal(err)
	}

	data, a, err := att.Bytes(ctx, goal.ID)
	if err != nil || string(data) != "goal" || a.ID != goal.ID {
		t.Errorf("Bytes = %q %+v %v", data, a, err)
	}
	if _, _, err := att.Bytes(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Bytes of nothing = %v", err)
	}
	if _, _, err := att.Open(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Open of nothing = %v", err)
	}
	f, a, err := att.Open(ctx, answer.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if string(got) != "answer" || a.PingID != "p1" {
		t.Errorf("Open = %q %+v", got, a)
	}

	if rows, _ := att.ForPing(ctx, "p1"); len(rows) != 1 || rows[0].ID != answer.ID {
		t.Errorf("ForPing = %+v", rows)
	}
	if rows, _ := att.ForRun(ctx, "r1"); len(rows) != 2 {
		t.Errorf("ForRun = %+v", rows)
	}
	if rows, _ := att.ForTicket(ctx, "t1"); len(rows) != 1 {
		t.Errorf("ForTicket = %+v", rows)
	}
	// An empty owner matches nothing, or every unclaimed row would be "theirs".
	if rows, _ := mem.AttachmentsForRun(ctx, ""); len(rows) != 0 {
		t.Errorf("empty run = %+v", rows)
	}
	if rows, _ := mem.AttachmentsForTicket(ctx, ""); len(rows) != 0 {
		t.Errorf("empty ticket = %+v", rows)
	}
	if rows, _ := mem.AttachmentsForPing(ctx, ""); len(rows) != 0 {
		t.Errorf("empty ping = %+v", rows)
	}

	if err := att.DeleteForRun(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if rows, _ := att.ForRun(ctx, "r1"); len(rows) != 0 {
		t.Errorf("after delete = %+v", rows)
	}
	if _, err := att.Get(ctx, ticket.ID); err != nil {
		t.Errorf("the ticket's row went with the run's: %v", err)
	}
}

func TestSnapshotReads(t *testing.T) {
	ctx := context.Background()
	_, snaps, mem, blobs := newStores(t)
	id := func(s string) string { return s }
	if _, err := snaps.Capture(ctx, Snapshot{RunID: "r", StageID: "s", Kind: KindLog}, nil, nil); err == nil {
		t.Error("a capture with no scrub was accepted")
	}
	a, err := snaps.Capture(ctx, Snapshot{RunID: "r", StageID: "s", Kind: KindPingFile, PingID: "p", Path: "a.md"}, id, []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Filename != "file" {
		t.Errorf("default filename = %q", a.Filename)
	}
	b, err := snaps.Capture(ctx, Snapshot{RunID: "r", StageID: "s", Iteration: 1, Kind: KindPingFile, PingID: "p", Path: "b.md", Filename: "b.md"}, id, []byte("two"))
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := snaps.Open(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Open = %v", err)
	}
	if _, _, err := snaps.Bytes(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Bytes = %v", err)
	}
	if rows, _ := snaps.ForPing(ctx, "p"); len(rows) != 2 {
		t.Errorf("ForPing = %+v", rows)
	}
	if rows, _ := mem.SnapshotsForPing(ctx, ""); len(rows) != 0 {
		t.Errorf("empty ping = %+v", rows)
	}
	// Ordered by stage, then attempt.
	rows, err := snaps.ForRun(ctx, "r")
	if err != nil || len(rows) != 2 || rows[0].ID != a.ID || rows[1].ID != b.ID {
		t.Errorf("ForRun = %+v, %v", rows, err)
	}

	// The record outlives the bytes.
	if err := os.Remove(blobs.path(a.SHA256)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := snaps.Open(ctx, a.ID); !errors.Is(err, ErrContentGone) {
		t.Errorf("Open after the sweep = %v", err)
	}
	if _, _, err := snaps.Bytes(ctx, a.ID); !errors.Is(err, ErrContentGone) {
		t.Errorf("Bytes after the sweep = %v", err)
	}

	if err := mem.TruncateSnapshots(ctx); err != nil {
		t.Fatal(err)
	}
	if rows, _ := snaps.ForRun(ctx, "r"); len(rows) != 0 {
		t.Errorf("after truncate = %+v", rows)
	}
}

func TestNewIDsAreDistinctHex(t *testing.T) {
	a, b := newID(), newID()
	if len(a) != 32 || a == b {
		t.Errorf("ids = %q %q", a, b)
	}
}
