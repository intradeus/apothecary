package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"apothecary/internal/engine"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/run/event"
)

func newFilesEnv(t *testing.T) (*env, *testFiles) {
	t.Helper()
	files := newTestFiles(t, 64, []string{"text/*", "image/svg+xml"})
	x := newEnvWith(t, engine.Config{}, files.deps)
	files.serve(x)
	return x, files
}

func (x *env) upload(t *testing.T, body *bytes.Buffer, ctype string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest("POST", x.ts.URL+"/api/attachments", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", ctype)
	res, err := http.DefaultClient.Do(x.authed(req))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, data
}

// form builds a multipart body of n text files.
func form(n int) (*bytes.Buffer, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for i := range n {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename="f%d.txt"`, i))
		h.Set("Content-Type", "text/plain")
		part, _ := mw.CreatePart(h)
		fmt.Fprintf(part, "file %d", i)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestUploadPolicy(t *testing.T) {
	x, _ := newFilesEnv(t)

	// Up to the cap of files, in order.
	buf, ct := form(filestore.MaxUploadFiles)
	res, data := x.upload(t, buf, ct)
	got := decode[Uploaded](t, data)
	if res.StatusCode != http.StatusCreated || len(got.Attachments) != filestore.MaxUploadFiles || got.Attachments[3].Filename != "f3.txt" {
		t.Fatalf("at the cap = %d %s", res.StatusCode, data)
	}
	if got.Attachments[0].UserID != testUser {
		t.Errorf("uploader = %q", got.Attachments[0].UserID)
	}
	buf, ct = form(filestore.MaxUploadFiles + 1)
	if res, data := x.upload(t, buf, ct); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "at most") {
		t.Errorf("past the cap = %d %s", res.StatusCode, data)
	}

	// A non-file field is skipped; with nothing else it is an empty upload.
	var fields bytes.Buffer
	mw := multipart.NewWriter(&fields)
	_ = mw.WriteField("note", "hello")
	_ = mw.Close()
	if res, data := x.upload(t, &fields, mw.FormDataContentType()); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "no files") {
		t.Errorf("no files = %d %s", res.StatusCode, data)
	}
	// A body that is not multipart, and one that breaks off.
	if res, data := x.upload(t, bytes.NewBufferString("{}"), "application/json"); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "multipart") {
		t.Errorf("not multipart = %d %s", res.StatusCode, data)
	}
	if res, data := x.upload(t, bytes.NewBufferString("--x\r\nnot a header\r\n\r\nbody\r\n--x--\r\n"), "multipart/form-data; boundary=x"); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "malformed") {
		t.Errorf("malformed = %d %s", res.StatusCode, data)
	}
	// Over the size and off the list.
	body, ct := multipartBody("big.txt", "text/plain", bytes.Repeat([]byte("x"), 65))
	if res, data := x.upload(t, body, ct); res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("too large = %d %s", res.StatusCode, data)
	}
	body, ct = multipartBody("a.png", "image/png", []byte("x"))
	if res, data := x.upload(t, body, ct); res.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("wrong type = %d %s", res.StatusCode, data)
	}

	res, data = x.do(t, "GET", "/api/attachments/limits", nil, nil)
	if lim := decode[filestore.UploadLimits](t, data); res.StatusCode != http.StatusOK || lim.MaxBytes != 64 || lim.MaxFiles != filestore.MaxUploadFiles {
		t.Errorf("limits = %d %s", res.StatusCode, data)
	}
}

func TestUploadsWithNoStore(t *testing.T) {
	x := newEnv(t, engine.Config{})
	buf, ct := form(1)
	if res, data := x.upload(t, buf, ct); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("upload = %d %s", res.StatusCode, data)
	}
	id := map[string]string{"id": "a"}
	if w := call(x.srv.getAttachment, "GET", "/", "", testUser, id); w.Code != http.StatusNotFound {
		t.Errorf("attachment = %d", w.Code)
	}
	if w := call(x.srv.getSnapshot, "GET", "/", "", testUser, id); w.Code != http.StatusNotFound {
		t.Errorf("snapshot = %d", w.Code)
	}
	if w := call(x.srv.getLog, "GET", "/", "", testUser, map[string]string{"id": "r", "stage": "s"}); w.Code != http.StatusNotFound {
		t.Errorf("log = %d", w.Code)
	}
	if w := call(x.srv.getRunFiles, "GET", "/", "", testUser, id); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"attachments":[]`) {
		t.Errorf("run files = %d %s", w.Code, w.Body)
	}
}

// Bytes authored by an agent or a person must not run on the control-plane
// origin, however they label themselves.
func TestByteRoutesServeSafely(t *testing.T) {
	x, files := newFilesEnv(t)
	x.seedLiveRun(t, "r1")
	ctx := context.Background()

	snap := func(name, mime, body string) string {
		t.Helper()
		sn, err := files.snaps.Capture(ctx, filestore.Snapshot{RunID: "r1", StageID: "s", Kind: filestore.KindOutput, Name: "o", Path: name, Filename: name, Mime: mime},
			func(s string) string { return s }, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return sn.ID
	}
	for name, c := range map[string]struct {
		mime   string
		active bool
	}{
		"page.html": {"text/html; charset=utf-8", true},
		"page.htm":  {"TEXT/HTML", true},
		"pic.svg":   {"image/svg+xml", true},
		"doc.xhtml": {"application/xhtml+xml", true},
		"feed.xml":  {"application/xml", true},
		"note.txt":  {"text/plain", false},
	} {
		res, body := x.do(t, "GET", "/api/snapshots/"+snap(name, c.mime, "<script>1</script>"), nil, nil)
		if res.StatusCode != http.StatusOK || string(body) != "<script>1</script>" {
			t.Errorf("%s = %d %q", name, res.StatusCode, body)
		}
		if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s nosniff = %q", name, got)
		}
		if got := res.Header.Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") || !strings.Contains(got, "sandbox") {
			t.Errorf("%s CSP = %q", name, got)
		}
		disp := res.Header.Get("Content-Disposition")
		if want := "attachment"; c.active != strings.HasPrefix(disp, want) {
			t.Errorf("%s disposition = %q", name, disp)
		}
		if !strings.Contains(disp, `filename="`+name+`"`) {
			t.Errorf("%s filename = %q", name, disp)
		}
	}
	// ?download forces a harmless type to download too.
	res, _ := x.do(t, "GET", "/api/snapshots/"+snap("a.txt", "text/plain", "x")+"?download=1", nil, nil)
	if got := res.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Errorf("forced download = %q", got)
	}
	// Ranges work, since the page previews large logs.
	res, body := x.do(t, "GET", "/api/snapshots/"+snap("b.txt", "text/plain", "0123456789"), nil, map[string]string{"Range": "bytes=2-4"})
	if res.StatusCode != http.StatusPartialContent || string(body) != "234" {
		t.Errorf("range = %d %q", res.StatusCode, body)
	}
}

// A filename a browser will parse comes out of the store unable to break the
// header it lands in.
func TestDownloadHeaderCannotBeInjected(t *testing.T) {
	x, files := newFilesEnv(t)
	a, err := files.att.Save(context.Background(), testUser, "evil\".txt\r\nX-Injected: 1", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	res, body := x.do(t, "GET", "/api/attachments/"+a.ID, nil, nil)
	if res.StatusCode != http.StatusOK || res.Header.Get("X-Injected") != "" {
		t.Fatalf("download = %d %s, %v", res.StatusCode, body, res.Header)
	}
	if got := res.Header.Get("Content-Disposition"); strings.Count(got, `"`) != 2 {
		t.Errorf("disposition = %q", got)
	}
}

// The record outlives the bytes: gone and corrupt are told apart from missing.
func TestByteRoutesWhenTheContentIsLost(t *testing.T) {
	dir := t.TempDir()
	mem := filestore.NewMem()
	blobs := filestore.NewBlobs(dir)
	files := &testFiles{att: filestore.NewAttachments(mem, blobs, 1<<20, []string{"text/*"}), snaps: filestore.NewSnapshots(mem, blobs, 1<<20), mem: mem}
	x := newEnvWith(t, engine.Config{}, files.deps)
	files.serve(x)
	ctx := context.Background()
	a, err := files.att.Save(ctx, testUser, "a.txt", "text/plain", strings.NewReader("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := files.att.Save(ctx, testUser, "b.txt", "text/plain", strings.NewReader("bravo"))
	if err != nil {
		t.Fatal(err)
	}
	blobPath := func(sha string) string { return filepath.Join(dir, "blobs", sha[:2], sha) }
	if err := os.Remove(blobPath(a.SHA256)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blobPath(b.SHA256), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, body := x.do(t, "GET", "/api/attachments/"+a.ID, nil, nil)
	if res.StatusCode != http.StatusGone || !strings.Contains(string(body), "no longer stored") {
		t.Errorf("gone = %d %s", res.StatusCode, body)
	}
	res, body = x.do(t, "GET", "/api/attachments/"+b.ID, nil, nil)
	if res.StatusCode != http.StatusGone || !strings.Contains(string(body), "integrity") || strings.Contains(string(body), "tampered") {
		t.Errorf("corrupt = %d %s", res.StatusCode, body)
	}
	// A row that vanished past the guard.
	if w := call(x.srv.getAttachment, "GET", "/", "", testUser, map[string]string{"id": "nope"}); w.Code != http.StatusNotFound {
		t.Errorf("missing = %d %s", w.Code, w.Body)
	}
	if w := call(x.srv.getSnapshot, "GET", "/", "", testUser, map[string]string{"id": "nope"}); w.Code != http.StatusNotFound {
		t.Errorf("missing snapshot = %d %s", w.Code, w.Body)
	}

	// The stored log is gone but its record stands.
	x.seedLiveRun(t, "r1")
	sn, err := files.snaps.Capture(ctx, filestore.Snapshot{RunID: "r1", StageID: "build", Kind: filestore.KindLog}, func(s string) string { return s }, []byte("log"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blobPath(sn.SHA256)); err != nil {
		t.Fatal(err)
	}
	if res, body := x.do(t, "GET", "/api/runs/r1/logs/build", nil, nil); res.StatusCode != http.StatusGone {
		t.Errorf("gone log = %d %s", res.StatusCode, body)
	}
}

func TestServeFileReportsAnOutage(t *testing.T) {
	w := call(func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, nil, "a", "text/plain", errors.New("disk on fire"))
	}, "GET", "/", "", testUser, nil)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("code = %d %s", w.Code, w.Body)
	}
}

// The log route serves the newest attempt of the stage, tailed.
func TestLogsServeTheLatestAttempt(t *testing.T) {
	x, files := newFilesEnv(t)
	x.seedLiveRun(t, "r1")
	ctx := context.Background()
	for i, text := range []string{"zero", "one", "two"} {
		if _, err := files.snaps.Capture(ctx, filestore.Snapshot{RunID: "r1", StageID: "build", Kind: filestore.KindLog, Iteration: i},
			func(s string) string { return s }, []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := files.snaps.Capture(ctx, filestore.Snapshot{RunID: "r1", StageID: "other", Kind: filestore.KindLog, Iteration: 9},
		func(s string) string { return s }, []byte("not this stage")); err != nil {
		t.Fatal(err)
	}
	res, body := x.do(t, "GET", "/api/runs/r1/logs/build", nil, nil)
	if res.StatusCode != http.StatusOK || string(body) != "two" || res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("log = %d %q %v", res.StatusCode, body, res.Header)
	}
	for q, want := range map[string]string{"?n=2": "wo", "?n=0": "two", "?n=x": "two", "?n=-1": "two", "?n=999": "two"} {
		if _, body := x.do(t, "GET", "/api/runs/r1/logs/build"+q, nil, nil); string(body) != want {
			t.Errorf("%s = %q, want %q", q, body, want)
		}
	}
}

// failingSnapshots fails the list a route starts from.
type failingFileDB struct {
	*filestore.Mem
	err error
}

func (f failingFileDB) SnapshotsForRun(context.Context, string) ([]filestore.Snapshot, error) {
	return nil, f.err
}
func (f failingFileDB) SnapshotsForPing(context.Context, string) ([]filestore.Snapshot, error) {
	return nil, f.err
}
func (f failingFileDB) AttachmentsForRun(context.Context, string) ([]filestore.Attachment, error) {
	return nil, f.err
}
func (f failingFileDB) AttachmentsForTicket(context.Context, string) ([]filestore.Attachment, error) {
	return nil, f.err
}

func TestRunFiles(t *testing.T) {
	x, files := newFilesEnv(t)
	x.seedLiveRun(t, "r1")
	ctx := context.Background()
	a, err := files.att.Save(ctx, testUser, "goal.txt", "text/plain", strings.NewReader("g"))
	if err != nil {
		t.Fatal(err)
	}
	if err := files.att.ClaimForRun(ctx, testUser, []string{a.ID}, "r1", ""); err != nil {
		t.Fatal(err)
	}
	id := func(s string) string { return s }
	log, err := files.snaps.Capture(ctx, filestore.Snapshot{RunID: "r1", StageID: "s", Kind: filestore.KindLog}, id, []byte("l"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.snaps.Capture(ctx, filestore.Snapshot{RunID: "r1", StageID: "s", Kind: filestore.KindPingFile, PingID: "p", Path: "a.md"}, id, []byte("m")); err != nil {
		t.Fatal(err)
	}
	res, body := x.do(t, "GET", "/api/runs/r1/files", nil, nil)
	got := decode[RunFiles](t, body)
	if res.StatusCode != http.StatusOK || len(got.Attachments) != 1 || got.Attachments[0].ID != a.ID {
		t.Fatalf("files = %d %s", res.StatusCode, body)
	}
	// A ping's files render on the ping.
	if len(got.Snapshots) != 1 || got.Snapshots[0].ID != log.ID {
		t.Errorf("snapshots = %+v", got.Snapshots)
	}

	boom := errors.New("db down")
	blobs := filestore.NewBlobs(t.TempDir())
	x.srv.Attachments = filestore.NewAttachments(failingFileDB{files.mem, boom}, blobs, 64, nil)
	if res, body := x.do(t, "GET", "/api/runs/r1/files", nil, nil); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("attachment outage = %d %s", res.StatusCode, body)
	}
	x.srv.Attachments = files.att
	x.srv.Snapshots = filestore.NewSnapshots(failingFileDB{files.mem, boom}, blobs, 64)
	if res, body := x.do(t, "GET", "/api/runs/r1/files", nil, nil); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("snapshot outage = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "GET", "/api/runs/r1/logs/build", nil, nil); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("log outage = %d %s", res.StatusCode, body)
	}
}

// A question's review documents come from what was captured when it was
// raised, each parsed by the parser the engine posts with; what cannot be
// parsed or was not captured lists with the reason.
func TestPingCuration(t *testing.T) {
	x, files := newFilesEnv(t)
	files = &testFiles{att: files.att, mem: files.mem,
		snaps: filestore.NewSnapshots(files.mem, filestore.NewBlobs(t.TempDir()), 1<<20)}
	x.srv.Snapshots = files.snaps
	x.seedLiveRun(t, "r1")
	ctx := context.Background()
	refs := map[string]string{
		"review.yaml":    agentspec.TypeReviewThreads,
		"replies.yaml":   agentspec.TypeReviewReplies,
		"threads.yaml":   agentspec.TypeMRThreads,
		"bad.yaml":       agentspec.TypeReviewThreads,
		"badreply.yaml":  agentspec.TypeReviewReplies,
		"badthread.yaml": agentspec.TypeMRThreads,
	}
	order := []string{"plain.md", "review.yaml", "replies.yaml", "threads.yaml", "bad.yaml", "badreply.yaml", "badthread.yaml", "missing.yaml"}
	types := map[string]string{"missing.yaml": agentspec.TypeReviewThreads}
	for k, v := range refs {
		types[k] = v
	}
	if _, err := x.store.Append(ctx, "r1", &event.PingRaised{
		PingID: "p1", Type: "gate", Question: "ok?", Files: order, FileTypes: types,
	}); err != nil {
		t.Fatal(err)
	}
	body := map[string]string{
		"plain.md":       "# not a review",
		"review.yaml":    "summary: looks fine\nnotes: []\n",
		"replies.yaml":   "[]\n",
		"threads.yaml":   "threads: []\n",
		"bad.yaml":       "notes: 5\n",
		"badreply.yaml":  "- body: no thread\n",
		"badthread.yaml": "threads:\n  - body: no id\n",
	}
	for path, text := range body {
		if _, err := files.snaps.Capture(ctx, filestore.Snapshot{RunID: "r1", StageID: "s", Kind: filestore.KindPingFile, PingID: "p1", Path: path},
			func(s string) string { return s }, []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	res, raw := x.do(t, "GET", "/api/pings/p1/curation", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("curation = %d %s", res.StatusCode, raw)
	}
	got := decode[CurationView](t, raw)
	docs := map[string]CurationDocument{}
	for _, d := range got.Documents {
		docs[d.Path] = d
	}
	if _, ok := docs["plain.md"]; ok {
		t.Error("a plain document was offered for curation")
	}
	if d := docs["review.yaml"]; !d.Captured || d.Error != "" || d.Review == nil || d.Review.Summary != "looks fine" {
		t.Errorf("review = %+v", d)
	}
	if d := docs["replies.yaml"]; !d.Captured || d.Error != "" {
		t.Errorf("replies = %+v", d)
	}
	if got.Threads == nil {
		t.Error("the threads document was not carried")
	}
	for _, path := range []string{"bad.yaml", "badreply.yaml", "badthread.yaml"} {
		if d := docs[path]; !d.Captured || d.Error == "" {
			t.Errorf("%s = %+v, want the parse error", path, d)
		}
	}
	if d := docs["missing.yaml"]; d.Captured || !strings.Contains(d.Error, "not captured") {
		t.Errorf("missing = %+v", d)
	}

	// The list failing leaves every document uncurated, not the page broken.
	x.srv.Snapshots = filestore.NewSnapshots(failingFileDB{files.mem, errors.New("db down")}, filestore.NewBlobs(t.TempDir()), 64)
	res, raw = x.do(t, "GET", "/api/pings/p1/curation", nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(raw), "not captured") {
		t.Errorf("list outage = %d %s", res.StatusCode, raw)
	}
	if res, raw := x.do(t, "GET", "/api/pings/p1/files", nil, nil); res.StatusCode != http.StatusOK || strings.Contains(string(raw), `"captured":true`) {
		t.Errorf("files during an outage = %d %s", res.StatusCode, raw)
	}
	x.srv.Snapshots = nil
	res, raw = x.do(t, "GET", "/api/pings/p1/curation", nil, nil)
	if res.StatusCode != http.StatusOK || strings.Contains(string(raw), `"captured":true`) {
		t.Errorf("no store = %d %s", res.StatusCode, raw)
	}
}

// Bytes gone from under a captured review: it lists with the reason.
func TestPingCurationWhenBytesAreGone(t *testing.T) {
	dir := t.TempDir()
	mem := filestore.NewMem()
	blobs := filestore.NewBlobs(dir)
	snaps := filestore.NewSnapshots(mem, blobs, 1<<20)
	x := newEnv(t, engine.Config{})
	x.srv.Snapshots = snaps
	x.seedLiveRun(t, "r1")
	ctx := context.Background()
	if _, err := x.store.Append(ctx, "r1", &event.PingRaised{
		PingID: "p1", Type: "gate", Question: "ok?", Files: []string{"r.yaml"},
		FileTypes: map[string]string{"r.yaml": agentspec.TypeReviewThreads},
	}); err != nil {
		t.Fatal(err)
	}
	sn, err := snaps.Capture(ctx, filestore.Snapshot{RunID: "r1", StageID: "s", Kind: filestore.KindPingFile, PingID: "p1", Path: "r.yaml"},
		func(s string) string { return s }, []byte("notes: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "blobs", sn.SHA256[:2], sn.SHA256)); err != nil {
		t.Fatal(err)
	}
	_, raw := x.do(t, "GET", "/api/pings/p1/curation", nil, nil)
	got := decode[CurationView](t, raw)
	if len(got.Documents) != 1 || got.Documents[0].Captured || !strings.Contains(got.Documents[0].Error, "no longer stored") {
		t.Errorf("curation = %s", raw)
	}
}

func TestMimeByExt(t *testing.T) {
	if got := mimeByExt("a.json"); got != "application/json" {
		t.Errorf("json = %q", got)
	}
	if got := mimeByExt("noext"); got != "application/octet-stream" {
		t.Errorf("none = %q", got)
	}
	if got := mimeByExt("a.txt"); got != "text/plain" {
		t.Errorf("charset is stripped: %q", got)
	}
}

// The ticket's attachment list, and the store failing under it.
func TestTicketAttachmentListFailures(t *testing.T) {
	x, files := newFilesEnv(t)
	ticket := x.newTicket(t, "t", "")
	x.srv.Attachments = filestore.NewAttachments(failingFileDB{files.mem, errors.New("db down")}, filestore.NewBlobs(t.TempDir()), 64, nil)
	if res, body := x.do(t, "GET", "/api/tickets/"+ticket+"/attachments", nil, nil); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("list = %d %s", res.StatusCode, body)
	}
	// The ticket itself still answers, its ids empty rather than an error.
	res, body := x.do(t, "GET", "/api/tickets/"+ticket, nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"attachment_ids":[]`) {
		t.Errorf("ticket = %d %s", res.StatusCode, body)
	}
	x.srv.Attachments = nil
	res, body = x.do(t, "GET", "/api/tickets/"+ticket+"/attachments", nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"attachments":[]`) {
		t.Errorf("no store = %d %s", res.StatusCode, body)
	}
}
