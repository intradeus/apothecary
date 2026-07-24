package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// writeRunEnv points the shim at url with the given token. It replaces the
// file atomically (temp + rename): tests rotate the token from a goroutine
// while readRunEnv reads concurrently, and a plain truncate-then-write would
// let the reader catch an empty/partial file.
func writeTestEnv(t *testing.T, url, token string) {
	t.Helper()
	content := "APOTHECARY_RUN_ID=run1\nAPOTHECARY_RUN_TOKEN=" + token + "\nAPOTHECARY_ENGINE_URL=" + url + "\n"
	tmp := runEnvPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, runEnvPath); err != nil {
		t.Fatal(err)
	}
}

func fastProtocol(t *testing.T) {
	t.Helper()
	oldPoll, oldBase, oldCap, oldLimit, oldPath := pollInterval, backoffBase, backoffCap, unreachableLimit, runEnvPath
	pollInterval, backoffBase, backoffCap, unreachableLimit = time.Millisecond, time.Millisecond, 5*time.Millisecond, 200*time.Millisecond
	runEnvPath = filepath.Join(t.TempDir(), "run.env")
	t.Cleanup(func() {
		pollInterval, backoffBase, backoffCap, unreachableLimit, runEnvPath = oldPoll, oldBase, oldCap, oldLimit, oldPath
	})
}

func TestAskHumanPostPollAnswer(t *testing.T) {
	fastProtocol(t)
	var posts, gets int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok1" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "POST":
			posts++
			var body struct {
				Type, Question string
				Options        []string
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Type != "clarification" || body.Question != "which db?" || len(body.Options) != 2 {
				t.Errorf("ping body = %+v", body)
			}
			w.Header().Set("X-Ping-Id", "p1")
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/pings/p1"):
			gets++
			if gets < 3 {
				w.WriteHeader(204)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"answer": "postgres", "feedback": "use v16"})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "tok1")

	got, err := askHuman(askArgs{Type: "clarification", Question: "which db?", Options: []string{"postgres", "mysql"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres\n(note: use v16)" {
		t.Errorf("answer = %q", got)
	}
	if posts != 1 || gets < 3 {
		t.Errorf("posts=%d gets=%d", posts, gets)
	}
}

func TestAskHumanAnswerRendering(t *testing.T) {
	fastProtocol(t)
	cases := []struct {
		answer, feedback, want string
	}{
		{"text", "free-form reply", "free-form reply"},
		{"other", "none fit", "other — none of the offered options fit; the human answered: none fit"},
		{"yes", "", "yes"},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"answer": c.answer, "feedback": c.feedback})
		}))
		writeTestEnv(t, srv.URL, "t")
		got, err := askHuman(askArgs{Type: "blocker", Question: "q"})
		srv.Close()
		if err != nil || got != c.want {
			t.Errorf("answer %q/%q -> %q, %v (want %q)", c.answer, c.feedback, got, err, c.want)
		}
	}
}

func TestAskHumanRereadsEnvOn401(t *testing.T) {
	fastProtocol(t)
	// rotated is written by the httptest handler goroutine and read by the
	// test body — atomic so the two don't race on the bare bool.
	var rotated atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			rotated.Store(true)
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"answer": "text", "feedback": "ok"})
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "stale")
	// The token rotates from a goroutine mid-poll. Join it before returning:
	// otherwise its access to the runEnvPath global races fastProtocol's
	// cleanup restoring that global (file-based sync is not a happens-before
	// edge the race detector tracks).
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(5 * time.Millisecond)
		writeTestEnv(t, srv.URL, "fresh")
	}()
	got, err := askHuman(askArgs{Type: "blocker", Question: "q"})
	<-done
	if err != nil || got != "ok" || !rotated.Load() {
		t.Fatalf("got=%q err=%v rotated=%v", got, err, rotated.Load())
	}
}

func TestAskHumanUnreachableGivesToolError(t *testing.T) {
	fastProtocol(t)
	writeTestEnv(t, "http://127.0.0.1:1", "t") // nothing listens on port 1
	_, err := askHuman(askArgs{Type: "blocker", Question: "q"})
	if err == nil || !strings.Contains(err.Error(), "ask_human unavailable") {
		t.Fatalf("want unavailable error, got %v", err)
	}
}

func TestServeMCPFraming(t *testing.T) {
	fastProtocol(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"answer": "text", "feedback": "mcp-ok"})
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "t")

	var out bytes.Buffer
	oldOut := mcpOut
	mcpOut = &out
	defer func() { mcpOut = oldOut }()

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-01-01"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`, // notification: no reply
		`{"jsonrpc":"2.0","id":"two","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ask_human","arguments":{"type":"blocker","question":"q"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"nope"}`,
	}, "\n") + "\n"
	serveMCP(strings.NewReader(in))
	// tools/call replies from its own goroutine (send() holds stdoutMu while
	// writing mcpOut), so every read of the shared buffer must take the same
	// lock — otherwise it races the in-flight write.
	readOut := func() string {
		stdoutMu.Lock()
		defer stdoutMu.Unlock()
		return out.String()
	}
	// tools/call replies async; wait briefly for the 4th reply.
	deadline := time.Now().Add(2 * time.Second)
	for strings.Count(readOut(), "\n") < 4 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}

	final := readOut()
	byID := map[string]map[string]any{}
	sc := bufio.NewScanner(strings.NewReader(final))
	for sc.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			t.Fatalf("bad frame %q: %v", sc.Text(), err)
		}
		byID[string(mustJSON(msg["id"]))] = msg
	}
	if len(byID) != 4 {
		t.Fatalf("replies = %d: %s", len(byID), final)
	}
	init := byID["1"]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-01-01" {
		t.Errorf("initialize echoed %v", init["protocolVersion"])
	}
	tools := byID[`"two"`]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "ask_human" {
		t.Errorf("tools/list = %v", tools)
	}
	call := byID["3"]["result"].(map[string]any)
	text := call["content"].([]any)[0].(map[string]any)["text"]
	if text != "mcp-ok" || call["isError"] == true {
		t.Errorf("tools/call = %v", call)
	}
	if byID["4"]["error"] == nil {
		t.Errorf("unknown method must error: %v", byID["4"])
	}
}

func mustJSON(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}
