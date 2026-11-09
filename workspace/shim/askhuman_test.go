package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
	oldPoll, oldBase, oldCap, oldLimit, oldPath, oldProg := pollInterval, backoffBase, backoffCap, unreachableLimit, runEnvPath, progressInterval
	pollInterval, backoffBase, backoffCap, unreachableLimit = time.Millisecond, time.Millisecond, 5*time.Millisecond, 200*time.Millisecond
	progressInterval = 5 * time.Millisecond
	runEnvPath = filepath.Join(t.TempDir(), "run.env")
	t.Cleanup(func() {
		pollInterval, backoffBase, backoffCap, unreachableLimit, runEnvPath, progressInterval = oldPoll, oldBase, oldCap, oldLimit, oldPath, oldProg
	})
}

func TestAskHumanAnsweredInBand(t *testing.T) {
	fastProtocol(t)
	var posts, gets int
	var mu sync.Mutex
	var holdMS int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST":
			posts++
			var body struct {
				Type, Question string
				Fields         []map[string]any
				HoldMS         int64 `json:"hold_ms"`
				Key            string
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Type != "clarification" || body.Question != "which db?" || len(body.Fields) != 1 || body.Fields[0]["id"] != "db" {
				t.Errorf("ping body = %+v", body)
			}
			holdMS = body.HoldMS
			if body.Key == "" {
				t.Error("ping raised without an idempotency key")
			}
			w.Header().Set("X-Ping-Id", "p1")
			_ = json.NewEncoder(w).Encode(map[string]string{"text": "postgres\n(note: use v16)"})
		default:
			gets++
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "tok1")

	field := json.RawMessage(`{"id":"db","type":"string","options":["postgres","mysql"]}`)
	got, err := askHuman(askArgs{Type: "clarification", Question: "which db?", Fields: []json.RawMessage{field}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A human who is already there answers inside the grace hold, and the
	// agent never loses its turn over it.
	if got.Text != "postgres\n(note: use v16)" {
		t.Errorf("answer = %q", got.Text)
	}
	if posts != 1 || gets != 0 {
		t.Errorf("posts=%d gets=%d", posts, gets)
	}
	if holdMS != graceHold.Milliseconds() {
		t.Errorf("hold_ms = %d, want %d", holdMS, graceHold.Milliseconds())
	}
}

// The call blocks until the human answers: an open question is long-polled
// (with progress ticks keeping the harness from timing the call out), never
// handed back to the model as "stop now".
func TestAskHumanWaitsForTheAnswer(t *testing.T) {
	fastProtocol(t)
	var mu sync.Mutex
	var posts, gets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "POST" {
			posts++
			w.Header().Set("X-Ping-Id", "p9")
			w.WriteHeader(204)
			return
		}
		gets++
		if r.URL.Path != "/api/runs/run1/pings/p9" {
			t.Errorf("poll path = %s", r.URL.Path)
		}
		if gets < 3 {
			time.Sleep(10 * time.Millisecond)
			w.WriteHeader(204)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "use postgres"})
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "t")

	var ticks atomic.Int32
	got, err := askHuman(askArgs{Type: "clarification", Question: "which db?"}, nil, func() { ticks.Add(1) })
	if err != nil || got.Text != "use postgres" {
		t.Fatalf("got %+v, %v", got, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 || gets != 3 {
		t.Errorf("posts=%d gets=%d, want one raise then polls until answered", posts, gets)
	}
	if ticks.Load() == 0 {
		t.Error("no progress was reported while waiting")
	}
}

// A parked run cancels the turn; if the call outlives that, 410 ends it with
// a notice instead of polling forever.
func TestAskHumanParked(t *testing.T) {
	fastProtocol(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.Header().Set("X-Ping-Id", "p3")
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "t")
	got, err := askHuman(askArgs{Type: "blocker", Question: "q"}, nil, nil)
	if err != nil || !got.Parked || !strings.Contains(got.Text, "parked") || !strings.Contains(got.Text, "p3") {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// Cancellation (the harness cancelled the tool call) stops the wait.
func TestAskHumanCancelled(t *testing.T) {
	fastProtocol(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Ping-Id", "p4")
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "t")
	done := make(chan struct{})
	go func() { time.Sleep(30 * time.Millisecond); close(done) }()
	_, err := askHuman(askArgs{Type: "blocker", Question: "q"}, done, nil)
	if !errors.Is(err, errCancelled) {
		t.Fatalf("err = %v, want cancelled", err)
	}
}

// A lost response to the raise is retried with the SAME key, so the engine
// can recognise the retry instead of asking the human twice.
func TestAskHumanRetryKeepsKey(t *testing.T) {
	fastProtocol(t)
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Key string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		keys = append(keys, body.Key)
		n := len(keys)
		mu.Unlock()
		if n == 1 {
			// Drop the connection mid-response: a transport error client-side.
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			c.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "ok"})
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "t")
	got, err := askHuman(askArgs{Type: "blocker", Question: "q"}, nil, nil)
	if err != nil || got.Text != "ok" {
		t.Fatalf("got %+v, %v", got, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("keys = %q, want the same key twice", keys)
	}
}

// The engine's composed text is what the agent reads; the raw form answer
// rides along for an elicitation to be answered with.
func TestAskHumanReturnsTheFormAnswer(t *testing.T) {
	fastProtocol(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"text":"db: pg\nn: 2","answer":"accept","content":{"db":"pg","n":2}}`))
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "t")
	got, err := askHuman(askArgs{Type: "blocker", Question: "q"}, nil, nil)
	if err != nil || got.Text != "db: pg\nn: 2" || got.Answer != "accept" || got.Content["db"] != "pg" || got.Content["n"] != 2.0 {
		t.Fatalf("got %+v, %v", got, err)
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
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "ok"})
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
	got, err := askHuman(askArgs{Type: "blocker", Question: "q"}, nil, nil)
	<-done
	if err != nil || got.Text != "ok" || !rotated.Load() {
		t.Fatalf("got=%+v err=%v rotated=%v", got, err, rotated.Load())
	}
}

func TestAskHumanUnreachableGivesToolError(t *testing.T) {
	fastProtocol(t)
	writeTestEnv(t, "http://127.0.0.1:1", "t") // nothing listens on port 1
	_, err := askHuman(askArgs{Type: "blocker", Question: "q"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "ask_human unavailable") {
		t.Fatalf("want unavailable error, got %v", err)
	}
}

func TestServeMCPFraming(t *testing.T) {
	fastProtocol(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "mcp-ok"})
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
	serveMCP(strings.NewReader(in), true)
	// tools/call replies from its own goroutine (send() holds stdoutMu while
	// writing mcpOut), so every read of the shared buffer must take the same
	// lock — otherwise it races the in-flight write.
	readOut := func() string {
		stdoutMu.Lock()
		defer stdoutMu.Unlock()
		return out.String()
	}
	// tools/call replies async; wait briefly for the 4th reply.
	deadline := time.Now().Add(5 * time.Second)
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

// Not below an agent, the server starts but offers nothing, and a call by
// name is refused without reaching the engine.
func TestServeMCPToollessOutsideAnAgent(t *testing.T) {
	var out bytes.Buffer
	oldOut := mcpOut
	mcpOut = &out
	defer func() { mcpOut = oldOut }()
	serveMCP(strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ask_human","arguments":{"type":"blocker","question":"q"}}}`,
	}, "\n")+"\n"), false)
	stdoutMu.Lock()
	got := out.String()
	stdoutMu.Unlock()
	if !strings.Contains(got, `"tools":[]`) {
		t.Errorf("tools/list outside an agent offers a tool: %s", got)
	}
	if !strings.Contains(got, `"error"`) {
		t.Errorf("tools/call outside an agent was not refused: %s", got)
	}
}

// underAgent finds the marker in its own environment or an ancestor's (codex
// filters its MCP servers' environment), and nowhere else.
func TestUnderAgent(t *testing.T) {
	old := procDir
	procDir = t.TempDir()
	defer func() { procDir = old }()
	proc := func(pid, ppid int, comm string, env ...string) {
		d := filepath.Join(procDir, strconv.Itoa(pid))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		stat := strconv.Itoa(pid) + " (" + comm + ") S " + strconv.Itoa(ppid) + " 1 1 0"
		_ = os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o644)
		_ = os.WriteFile(filepath.Join(d, "environ"), []byte(strings.Join(env, "\x00")), 0o644)
	}
	// sidecar(10) → codex-acp(20, marked) → codex app-server(30) → [ask-human]
	proc(10, 1, "apothecary-shim", "PATH=/bin")
	proc(20, 10, "codex-acp", "PATH=/bin", agentEnv+"=1")
	proc(30, 20, "codex (app) server", "HOME=/h")
	// code-server(40) → extension host(50) → codex(60) → [ask-human]
	proc(40, 1, "node", "PATH=/bin", agentEnv+"=")
	proc(50, 40, "node", "PATH=/bin")
	proc(60, 50, "codex", "HOME=/h")
	none := func(string) string { return "" }
	if !underAgent(none, 30) {
		t.Error("the agent's codex: marker on an ancestor not found")
	}
	if underAgent(none, 60) {
		t.Error("the human's codex (editor extension) is taken for an agent")
	}
	if !underAgent(func(k string) string { return map[string]string{agentEnv: "1"}[k] }, 60) {
		t.Error("the marker in its own environment is not enough")
	}
	if underAgent(none, 999) {
		t.Error("an unreadable ancestry is taken for an agent")
	}
}

// engineFunc answers the protocol's requests in process, with no server.
type engineFunc func(*http.Request) (*http.Response, error)

func (f engineFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func withEngine(t *testing.T, f engineFunc) {
	t.Helper()
	old := httpClient
	httpClient = &http.Client{Transport: f}
	t.Cleanup(func() { httpClient = old })
}

func engineReply(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

// A wait is cancelled wherever it stands: in the backoff after a lost engine,
// in the pause after a rotated token, before it asked at all.
func TestAskHumanCancelledMidWait(t *testing.T) {
	fastProtocol(t)
	backoffBase, pollInterval = time.Hour, time.Hour
	writeTestEnv(t, "http://engine", "t")
	for name, respond := range map[string]func() (*http.Response, error){
		"backoff": func() (*http.Response, error) { return nil, errors.New("connection refused") },
		"401":     func() (*http.Response, error) { return engineReply(401, ""), nil },
	} {
		done := make(chan struct{})
		withEngine(t, func(*http.Request) (*http.Response, error) {
			close(done)
			return respond()
		})
		if _, err := askHuman(askArgs{Type: "blocker", Question: "q"}, done, nil); !errors.Is(err, errCancelled) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	withEngine(t, func(*http.Request) (*http.Response, error) {
		t.Error("a cancelled call reached the engine")
		return engineReply(500, ""), nil
	})
	closed := make(chan struct{})
	close(closed)
	if _, err := askHuman(askArgs{Type: "blocker", Question: "q"}, closed, nil); !errors.Is(err, errCancelled) {
		t.Errorf("before asking: err = %v", err)
	}
}

func TestAskHumanEngineFailures(t *testing.T) {
	fastProtocol(t)
	writeTestEnv(t, "http://engine", "t")
	for _, c := range []struct {
		name string
		res  *http.Response
		want string
	}{
		{"refused", engineReply(403, "run is over"), "engine returned 403: run is over"},
		{"no id", engineReply(204, ""), "did not return the question's id"},
		{"bad answer", engineReply(200, "{nope"), "bad engine response"},
	} {
		withEngine(t, func(*http.Request) (*http.Response, error) { return c.res, nil })
		if _, err := askHuman(askArgs{Type: "blocker", Question: "q"}, nil, nil); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// The single artifact of older calls and the list both reach the engine as
// the list.
func TestAskHumanArtifacts(t *testing.T) {
	fastProtocol(t)
	writeTestEnv(t, "http://engine", "t")
	for _, c := range []struct {
		args askArgs
		want string
	}{
		{askArgs{Artifact: "plan.md"}, `["plan.md"]`},
		{askArgs{Artifact: "old.md", Artifacts: []string{"a.md", "b.md"}}, `["a.md","b.md"]`},
		{askArgs{}, `[]`},
	} {
		var sent string
		withEngine(t, func(r *http.Request) (*http.Response, error) {
			var body struct {
				Artifacts json.RawMessage `json:"artifacts"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			sent = string(body.Artifacts)
			return engineReply(200, `{"text":"ok"}`), nil
		})
		if _, err := askHuman(c.args, nil, nil); err != nil || sent != c.want {
			t.Errorf("artifacts = %s, %v, want %s", sent, err, c.want)
		}
	}
}

func TestCallEngineRefusesWhatItCannotSend(t *testing.T) {
	if _, err := callEngine("POST", "/x", make(chan int), nil); err == nil {
		t.Error("a body that does not marshal was sent")
	}
	if _, err := callEngine("POST", "/x", nil, map[string]string{"APOTHECARY_ENGINE_URL": "http://bad host"}); err == nil {
		t.Error("a url that does not parse was dialled")
	}
}

func TestReadRunEnv(t *testing.T) {
	fastProtocol(t)
	if env := readRunEnv(); len(env) != 0 {
		t.Errorf("no file = %v", env)
	}
	if err := os.WriteFile(runEnvPath, []byte("A=1\nnot a pair\n=x\nB=c=d\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if env := readRunEnv(); len(env) != 2 || env["A"] != "1" || env["B"] != "c=d" {
		t.Errorf("env = %v", env)
	}
}

// A walk up /proc stops at anything it cannot make sense of, and at a loop.
func TestUnderAgentGivesUpOnBadProc(t *testing.T) {
	old := procDir
	procDir = t.TempDir()
	defer func() { procDir = old }()
	stat := func(pid int, content string) {
		t.Helper()
		d := filepath.Join(procDir, strconv.Itoa(pid))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "stat"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stat(10, "10 no parens at all")
	stat(20, "20 (comm) S")
	stat(30, "30 (comm) S x 1")
	stat(40, "40 (comm) S 40 1") // its own parent
	none := func(string) string { return "" }
	for _, pid := range []int{10, 20, 30, 40, 1} {
		if underAgent(none, pid) {
			t.Errorf("pid %d taken for an agent", pid)
		}
	}
}

// mcpSession serves MCP in process, on a pipe the test writes to.
type mcpSession struct {
	t   *testing.T
	in  *io.PipeWriter
	out *bytes.Buffer
}

func newMCPSession(t *testing.T, offered bool) *mcpSession {
	t.Helper()
	inR, inW := io.Pipe()
	out := &bytes.Buffer{}
	stdoutMu.Lock()
	oldOut := mcpOut
	mcpOut = out
	stdoutMu.Unlock()
	served := make(chan struct{})
	go func() { serveMCP(inR, offered); close(served) }()
	t.Cleanup(func() {
		inW.Close()
		<-served
		stdoutMu.Lock()
		mcpOut = oldOut
		stdoutMu.Unlock()
	})
	return &mcpSession{t: t, in: inW, out: out}
}

func (s *mcpSession) send(line string) {
	s.t.Helper()
	if _, err := io.WriteString(s.in, line+"\n"); err != nil {
		s.t.Fatal(err)
	}
}

func (s *mcpSession) frames() []map[string]any {
	stdoutMu.Lock()
	data := s.out.String()
	stdoutMu.Unlock()
	var out []map[string]any
	for line := range strings.Lines(data) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			s.t.Fatalf("bad frame %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// frame waits for the reply to id.
func (s *mcpSession) reply(id float64) map[string]any {
	s.t.Helper()
	var got map[string]any
	waitFor(s.t, func() bool {
		for _, f := range s.frames() {
			if f["id"] == id {
				got = f
				return true
			}
		}
		return false
	})
	return got
}

// A question that is waiting keeps the harness informed; one the harness
// gives up on (notifications/cancelled) stops, and is owed nothing.
func TestServeMCPProgressAndCancel(t *testing.T) {
	fastProtocol(t)
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.Header().Set("X-Ping-Id", "p1")
		} else {
			polls.Add(1)
			time.Sleep(time.Millisecond)
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "t")

	s := newMCPSession(t, true)
	s.send(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"ask_human","arguments":{"type":"blocker","question":"q"},"_meta":{"progressToken":"tok"}}}`)
	var progress []map[string]any
	waitFor(t, func() bool {
		progress = nil
		for _, f := range s.frames() {
			if f["method"] == "notifications/progress" {
				progress = append(progress, f["params"].(map[string]any))
			}
		}
		return len(progress) >= 2
	})
	if progress[0]["progressToken"] != "tok" || progress[0]["progress"] != 1.0 || progress[1]["progress"] != 2.0 {
		t.Errorf("progress = %v", progress)
	}

	s.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":99}}`) // nothing by that id
	s.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5}}`)
	s.send(`{"jsonrpc":"2.0","id":6,"method":"ping"}`)
	if got := s.reply(6); got["result"] == nil {
		t.Errorf("ping = %v", got)
	}
	waitFor(t, func() bool {
		before := polls.Load()
		time.Sleep(5 * time.Millisecond)
		return polls.Load() == before
	})
	for _, f := range s.frames() {
		if f["id"] == 5.0 {
			t.Errorf("a cancelled call was answered: %v", f)
		}
	}
	calls.mu.Lock()
	defer calls.mu.Unlock()
	if _, ok := calls.m["5"]; ok {
		t.Error("the cancelled call is still registered")
	}
}

// What goes wrong comes back as the tool's error, for the agent to read; a
// tool it never had is the protocol's.
func TestServeMCPErrors(t *testing.T) {
	fastProtocol(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "run is over", http.StatusForbidden)
	}))
	defer srv.Close()
	writeTestEnv(t, srv.URL, "t")

	s := newMCPSession(t, true)
	s.send("")
	s.send("not json")
	s.send(`{"jsonrpc":"2.0","id":null,"method":"tools/list"}`)
	s.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	s.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"other"}}`)
	s.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ask_human","arguments":{"type":"blocker","question":"q"}}}`)

	if v := s.reply(1)["result"].(map[string]any)["protocolVersion"]; v != "2024-11-05" {
		t.Errorf("default protocol = %v", v)
	}
	if e, _ := s.reply(2)["error"].(map[string]any); e == nil || e["code"] != -32602.0 || e["message"] != "unknown tool other" {
		t.Errorf("unknown tool = %v", e)
	}
	res := s.reply(3)["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if res["isError"] != true || !strings.Contains(text, "engine returned 403: run is over") {
		t.Errorf("tool error = %v", res)
	}
	if n := len(s.frames()); n != 3 {
		t.Errorf("frames = %d, want a reply to each request and nothing else: %v", n, s.frames())
	}
}

// The subcommand as a harness runs it: the marker in its environment offers
// the tool, a call goes to the engine named in run.env, EOF ends it.
func TestRunAskHuman(t *testing.T) {
	var auth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization") + " " + r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "use postgres"})
	}))
	defer srv.Close()
	env := filepath.Join(t.TempDir(), "run.env")
	content := "APOTHECARY_RUN_ID=run1\nAPOTHECARY_RUN_TOKEN=tok\nAPOTHECARY_ENGINE_URL=" + srv.URL + "\n"
	if err := os.WriteFile(env, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := shimCmd(t, "ask-human")
	cmd.Env = append(cmd.Env, agentEnv+"=1", shimRunEnv+"="+env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer stdin.Close()
	br := bufio.NewReader(stdout)
	rpc := func(line string) map[string]any {
		t.Helper()
		if _, err := io.WriteString(stdin, line+"\n"); err != nil {
			t.Fatal(err)
		}
		out, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("bad frame %q: %v", out, err)
		}
		return m["result"].(map[string]any)
	}
	tools := rpc(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", tools)
	}
	call := rpc(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ask_human","arguments":{"type":"blocker","question":"q"}}}`)
	if text := call["content"].([]any)[0].(map[string]any)["text"]; text != "use postgres" {
		t.Errorf("answer = %v", call)
	}
	if got := auth.Load(); got != "Bearer tok /api/runs/run1/pings" {
		t.Errorf("reached the engine as %v", got)
	}
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Errorf("exit on EOF = %v", err)
	}
}
