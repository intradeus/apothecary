package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// A pass is written whole to its file in the checkout and the agent is told
// where, how big and how many lines; a second call writes it afresh over
// whatever the agent left there, a read-only copy included.
func TestJournalReadWritesAPassToItsFile(t *testing.T) {
	fastProtocol(t)
	repo := withRepo(t)
	var asked atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Store(r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"id":"fix-ci-2","file":"fix-ci-2.md","text":"# pass\nline two\n"}`))
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "tok")

	for range 2 {
		got, err := journalRead("fix-ci-2")
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(repo, ".apothecary", "journal", "fix-ci-2.md")
		if want := "Wrote pass fix-ci-2 to " + file + " (.apothecary/journal/fix-ci-2.md in the checkout): 16 bytes, 2 lines."; !strings.HasPrefix(got, want) {
			t.Errorf("result = %q, want %q…", got, want)
		}
		data, err := os.ReadFile(file)
		if err != nil || string(data) != "# pass\nline two\n" {
			t.Fatalf("file = %q, %v", data, err)
		}
		if err := os.WriteFile(file, []byte("edited"), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if got := asked.Load(); got != "GET /api/runs/run1/journal/fix-ci-2 Bearer tok" {
		t.Errorf("asked %v", got)
	}
}

// No id reads every record into one file; a short decision comes back
// inline and writes nothing.
func TestJournalReadRecordsAndInline(t *testing.T) {
	fastProtocol(t)
	repo := withRepo(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/runs/run1/journal":
			_, _ = w.Write([]byte(`{"id":"records","file":"records.md","text":"all\n"}`))
		case "/api/runs/run1/journal/decision-3":
			_, _ = w.Write([]byte(`{"id":"decision-3","text":"### clarification [decision-3]\n"}`))
		case "/api/runs/run1/journal/decision-4":
			_, _ = w.Write([]byte(`{"id":"decision-4","file":"decision-4.md","text":"long\n"}`))
		}
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "t")

	if got, err := journalRead(" "); err != nil || !strings.HasPrefix(got, "Wrote every record of the run to ") {
		t.Errorf("records = %q, %v", got, err)
	}
	if got, err := journalRead("decision-3"); err != nil || got != "### clarification [decision-3]\n" {
		t.Errorf("decision = %q, %v", got, err)
	}
	if got, err := journalRead("decision-4"); err != nil || !strings.HasPrefix(got, "Wrote decision-4 to ") {
		t.Errorf("long decision = %q, %v", got, err)
	}
	entries, _ := os.ReadDir(filepath.Join(repo, ".apothecary", "journal"))
	if len(entries) != 2 {
		t.Errorf("files = %v", entries)
	}
}

// An id the run has not, an engine that cannot be reached, a token rotated
// under it, a reply it cannot read, a file name that would leave the
// journal's directory: each is a tool error the agent reads, or a retry.
func TestJournalReadFailures(t *testing.T) {
	fastProtocol(t)
	withRepo(t)
	var rotated atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/api/runs/run1/journal/") {
		case "nope-1":
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no such journal entry: \"nope-1\""}`))
		case "plain-1":
			w.WriteHeader(500)
			_, _ = w.Write([]byte("down"))
		case "bad-1":
			_, _ = w.Write([]byte("{"))
		case "escape-1":
			_, _ = w.Write([]byte(`{"id":"escape-1","file":"../x.md","text":"x"}`))
		case "dot-1":
			_, _ = w.Write([]byte(`{"id":"dot-1","file":".x","text":"x"}`))
		case "rotate-1":
			if r.Header.Get("Authorization") != "Bearer new" {
				writeTestEnv(t, "http://"+r.Host, "new")
				rotated.Store(true)
				w.WriteHeader(401)
				return
			}
			_, _ = w.Write([]byte(`{"id":"rotate-1","text":"fresh"}`))
		}
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "old")

	for id, want := range map[string]string{
		"nope-1":   `journal_read failed: engine returned 404: no such journal entry: "nope-1"`,
		"plain-1":  "journal_read failed: engine returned 500: down",
		"bad-1":    "journal_read failed: bad engine response",
		"escape-1": "journal_read failed: the engine named a file outside the journal",
		"dot-1":    "journal_read failed: the engine named a file outside the journal",
	} {
		if _, err := journalRead(id); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: %v, want %q", id, err, want)
		}
	}
	if got, err := journalRead("rotate-1"); err != nil || got != "fresh" || !rotated.Load() {
		t.Errorf("rotated token: %q, %v", got, err)
	}
	writeTestEnv(t, "http://127.0.0.1:1", "t")
	if _, err := journalRead("x-1"); err == nil || !strings.HasPrefix(err.Error(), "journal_read unavailable") {
		t.Errorf("unreachable: %v", err)
	}
}

// A checkout the shim cannot write to is said so.
func TestJournalReadUnwritableCheckout(t *testing.T) {
	fastProtocol(t)
	repo := withRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".apothecary"), []byte("a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeJournalFile(journalEntry{ID: "a-1", File: "a-1.md"}); err == nil {
		t.Error("wrote under a file")
	}
	if err := os.Remove(filepath.Join(repo, ".apothecary")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repo, ".apothecary", "journal", "a-1.md")
	if err := os.MkdirAll(filepath.Join(dir, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := writeJournalFile(journalEntry{ID: "a-1", File: "a-1.md"}); err == nil {
		t.Error("wrote over a directory")
	}
}

// Through MCP: listed beside ask_human below an agent, called by name, its
// failures tool errors; not below an agent, refused.
func TestServeMCPJournalRead(t *testing.T) {
	fastProtocol(t)
	withRepo(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/decision-1") {
			_, _ = w.Write([]byte(`{"id":"decision-1","text":"the decision"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "t")

	s := newMCPSession(t, true)
	s.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"journal_read","arguments":{"id":"decision-1"}}}`)
	s.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"journal_read","arguments":{"id":"gone-1"}}}`)
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
