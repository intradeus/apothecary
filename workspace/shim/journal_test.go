package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// withRepo points the journal's files at a temporary checkout.
func withRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := repoRoot
	repoRoot = func() string { return dir }
	t.Cleanup(func() { repoRoot = old })
	return dir
}

// An attempt is written whole to the file its reference names, and the
// agent is told where — the path quoted — how big and how many lines; the
// reference's # goes to the engine as %23. A second call writes it afresh
// over whatever the agent left there, a read-only copy included.
func TestJournalReadWritesAnAttemptToItsFile(t *testing.T) {
	fastProtocol(t)
	repo := withRepo(t)
	var asked atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Store(r.Method + " " + r.URL.EscapedPath() + " " + r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"ref":"fix-ci#2","file":"fix-ci#2.md","text":"# attempt\nline two\n"}`))
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "tok")

	for range 2 {
		got, err := journalRead("fix-ci#2")
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(repo, ".apothecary", "journal", "fix-ci#2.md")
		if want := "Wrote everything fix-ci#2 left to \"" + file + "\" (.apothecary/journal/fix-ci#2.md in the checkout): 19 bytes, 2 lines."; !strings.HasPrefix(got, want) {
			t.Errorf("result = %q, want %q…", got, want)
		}
		data, err := os.ReadFile(file)
		if err != nil || string(data) != "# attempt\nline two\n" {
			t.Fatalf("file = %q, %v", data, err)
		}
		if err := os.WriteFile(file, []byte("edited"), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if got := asked.Load(); got != "GET /api/runs/run1/journal/fix-ci%232 Bearer tok" {
		t.Errorf("asked %v", got)
	}
}

// No reference reads every record into run.md; a short decision comes back
// inline and writes nothing; a long decision or note is written at its
// reference, in its attempt's folder.
func TestJournalReadRecordsAndInline(t *testing.T) {
	fastProtocol(t)
	repo := withRepo(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/runs/run1/journal":
			_, _ = w.Write([]byte(`{"ref":"run","file":"run.md","text":"all\n"}`))
		case "/api/runs/run1/journal/build%231/decision%233":
			_, _ = w.Write([]byte(`{"ref":"build#1/decision#3","text":"### clarification [build#1/decision#3]\n"}`))
		case "/api/runs/run1/journal/build%231/decision%234":
			_, _ = w.Write([]byte(`{"ref":"build#1/decision#4","file":"build#1/decision#4.md","text":"long\n"}`))
		case "/api/runs/run1/journal/build%231/note%232":
			_, _ = w.Write([]byte(`{"ref":"build#1/note#2","file":"build#1/note#2.md","text":"long\n"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "t")

	if got, err := journalRead(" "); err != nil || !strings.HasPrefix(got, "Wrote every record of the run to ") || !strings.Contains(got, "journal/run.md\"") {
		t.Errorf("records = %q, %v", got, err)
	}
	if got, err := journalRead("build#1/decision#3"); err != nil || got != "### clarification [build#1/decision#3]\n" {
		t.Errorf("decision = %q, %v", got, err)
	}
	if got, err := journalRead("build#1/decision#4"); err != nil || !strings.HasPrefix(got, "Wrote build#1/decision#4 to ") {
		t.Errorf("long decision = %q, %v", got, err)
	}
	if got, err := journalRead("build#1/note#2"); err != nil || !strings.HasPrefix(got, "Wrote build#1/note#2 to ") {
		t.Errorf("long note = %q, %v", got, err)
	}
	for _, f := range []string{"run.md", "build#1/decision#4.md", "build#1/note#2.md"} {
		if _, err := os.Stat(filepath.Join(repo, ".apothecary", "journal", filepath.FromSlash(f))); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

// A reference the run has not, an engine that cannot be reached, a token
// rotated under it, a reply it cannot read, a file path that would leave the
// journal's directory: each is a tool error the agent reads, or a retry.
func TestJournalReadFailures(t *testing.T) {
	fastProtocol(t)
	withRepo(t)
	var rotated atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		named := func(file string) {
			_, _ = w.Write([]byte(`{"ref":"x#1","file":` + strconv.Quote(file) + `,"text":"x"}`))
		}
		switch strings.TrimPrefix(r.URL.Path, "/api/runs/run1/journal/") {
		case "nope#1":
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no such journal entry: the run has no attempt nope#1"}`))
		case "plain#1":
			w.WriteHeader(500)
			_, _ = w.Write([]byte("down"))
		case "bad#1":
			_, _ = w.Write([]byte("{"))
		case "up#1":
			named("../x.md")
		case "dot#1":
			named(".x")
		case "deep#1":
			named("a#1/b/c.md")
		case "abs#1":
			named("/etc/x.md")
		case "space#1":
			named("a b.md")
		case "empty#1":
			named("a#1/")
		case "rotate#1":
			if r.Header.Get("Authorization") != "Bearer new" {
				writeTestEnv(t, "http://"+r.Host, "new")
				rotated.Store(true)
				w.WriteHeader(401)
				return
			}
			_, _ = w.Write([]byte(`{"ref":"rotate#1","text":"fresh"}`))
		}
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "old")

	outside := "journal_read failed: the engine named a file outside the journal"
	for r, want := range map[string]string{
		"nope#1":  `journal_read failed: engine returned 404: no such journal entry: the run has no attempt nope#1`,
		"plain#1": "journal_read failed: engine returned 500: down",
		"bad#1":   "journal_read failed: bad engine response",
		"up#1":    outside, "dot#1": outside, "deep#1": outside, "abs#1": outside, "space#1": outside, "empty#1": outside,
	} {
		if _, err := journalRead(r); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: %v, want %q", r, err, want)
		}
	}
	if got, err := journalRead("rotate#1"); err != nil || got != "fresh" || !rotated.Load() {
		t.Errorf("rotated token: %q, %v", got, err)
	}
	writeTestEnv(t, "http://127.0.0.1:1", "t")
	if _, err := journalRead("x#1"); err == nil || !strings.HasPrefix(err.Error(), "journal_read unavailable") {
		t.Errorf("unreachable: %v", err)
	}
}

// An attempt's outputs that are not text are fetched by name, one at a
// time, and written byte for byte in the folder named after the attempt;
// one the engine cannot give, or a name or folder that would leave the
// journal, is said so and the rest still written.
func TestJournalReadWritesOutputsBeside(t *testing.T) {
	fastProtocol(t)
	repo := withRepo(t)
	png := "\x89PNG\r\n\x1a\n\x00\xff"
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		mu.Unlock()
		switch r.URL.Query().Get("file") {
		case "":
			ref := strings.TrimPrefix(r.URL.Path, "/api/runs/run1/journal/")
			_, _ = w.Write([]byte(`{"ref":` + strconv.Quote(ref) + `,"file":` + strconv.Quote(ref+".md") + `,"text":"x\n",` +
				`"beside":[{"name":"shot.png"},{"name":"gone.png"},{"name":"../up.png"},{"name":"shot-2.png"}]}`))
		case "shot.png", "shot-2.png":
			_, _ = w.Write([]byte(png))
		default:
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(`{"error":"no longer stored"}`))
		}
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "t")

	got, err := journalRead("review#5")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repo, ".apothecary", "journal", "review#5")
	for _, want := range []string{
		"Its outputs that are not text are written as themselves in \"" + dir + "\": shot.png, shot-2.png.",
		"Could not write: gone.png (engine returned 410: no longer stored); ../up.png (a name outside the journal).",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("result = %q, want %q", got, want)
		}
	}
	for _, f := range []string{"shot.png", "shot-2.png"} {
		if data, err := os.ReadFile(filepath.Join(dir, f)); err != nil || string(data) != png {
			t.Errorf("%s = %q, %v", f, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, ".apothecary", "journal", "up.png")); err == nil {
		t.Error("a file was written outside the attempt's folder")
	}
	mu.Lock()
	if !slices.Contains(asked, "/api/runs/run1/journal/review%235?file=shot.png") {
		t.Errorf("asked %v", asked)
	}
	mu.Unlock()

	// A folder the engine names outside the journal: nothing beside it.
	got, err = journalRead("..")
	if err == nil || !strings.Contains(err.Error(), "outside the journal") {
		t.Errorf("a file named ../.md = %q, %v", got, err)
	}
	if got := writeBeside("/x", journalEntry{Ref: "a/b"}); !strings.Contains(got, "not written: the engine named a folder outside the journal") {
		t.Errorf("folder = %q", got)
	}
}

// A checkout the shim cannot write to is said so.
func TestJournalReadUnwritableCheckout(t *testing.T) {
	fastProtocol(t)
	repo := withRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".apothecary"), []byte("a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeJournalFile(journalEntry{Ref: "a#1", File: "a#1.md"}); err == nil {
		t.Error("wrote under a file")
	}
	if err := os.Remove(filepath.Join(repo, ".apothecary")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repo, ".apothecary", "journal", "a#1.md")
	if err := os.MkdirAll(filepath.Join(dir, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := writeJournalFile(journalEntry{Ref: "a#1", File: "a#1.md"}); err == nil {
		t.Error("wrote over a directory")
	}
}

// Through MCP: listed beside ask_human below an agent, called by name, its
// failures tool errors; not below an agent, refused.
func TestServeMCPJournalRead(t *testing.T) {
	fastProtocol(t)
	withRepo(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/decision#1") {
			_, _ = w.Write([]byte(`{"ref":"run/decision#1","text":"the decision"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "t")

	s := newMCPSession(t, true)
	s.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"journal_read","arguments":{"ref":"run/decision#1"}}}`)
	s.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"journal_read","arguments":{"ref":"gone#1"}}}`)
	ok := s.reply(1)["result"].(map[string]any)
	if ok["content"].([]any)[0].(map[string]any)["text"] != "the decision" || ok["isError"] == true {
		t.Errorf("journal_read = %v", ok)
	}
	if bad := s.reply(2)["result"].(map[string]any); bad["isError"] != true {
		t.Errorf("a missing entry = %v", bad)
	}
}

func TestServeMCPJournalReadOutsideAnAgent(t *testing.T) {
	s := newMCPSession(t, false)
	s.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"journal_read","arguments":{}}}`)
	if s.reply(1)["error"] == nil {
		t.Error("journal_read outside an agent was not refused")
	}
}
