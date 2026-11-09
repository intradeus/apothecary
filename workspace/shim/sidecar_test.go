package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fake ACP agent runs as this test binary re-executed with
// SHIM_FAKE_ACP=1: a scripted agent speaking the same wire protocol as
// opencode acp / codex-acp, enough to exercise the host end to end.
func TestMain(m *testing.M) {
	if os.Getenv("SHIM_FAKE_ACP") == "1" {
		fakeACPAgent()
		os.Exit(0)
	}
	if os.Getenv(shimEnv) == "1" {
		asShim()
		os.Exit(0)
	}
	// No test in this package reaches the developer's home or shell: SSH
	// sessions exec in $HOME, the terminal and an SSH shell are login
	// shells that would read the developer's profile (a zsh without one
	// even starts its first-run wizard), agents start with os.Environ().
	// A test that cares sets its own.
	home, err := os.MkdirTemp("", "shim-home-")
	if err == nil {
		_ = os.Setenv("HOME", home)
	}
	_ = os.Setenv("SHELL", "/bin/sh")
	code := m.Run()
	if home != "" {
		os.RemoveAll(home)
	}
	if testing.CoverMode() != "" {
		os.RemoveAll(coverScratch())
	}
	os.Exit(code)
}

func fakeACPAgent() {
	out := bufio.NewWriter(os.Stdout)
	var omu sync.Mutex
	write := func(v any) {
		data, _ := json.Marshal(v)
		omu.Lock()
		out.Write(append(data, '\n'))
		out.Flush()
		omu.Unlock()
	}
	update := func(sid string, u map[string]any) {
		write(map[string]any{"jsonrpc": "2.0", "method": "session/update",
			"params": map[string]any{"sessionId": sid, "update": u}})
	}
	var cmu sync.Mutex
	cancels := map[string]chan struct{}{}
	pending := map[string]chan json.RawMessage{}
	nextReq := 100
	ask := func(method string, params any) json.RawMessage {
		cmu.Lock()
		nextReq++
		id := fmt.Sprint(nextReq)
		ch := make(chan json.RawMessage, 1)
		pending[id] = ch
		cmu.Unlock()
		write(map[string]any{"jsonrpc": "2.0", "id": nextReq, "method": method, "params": params})
		return <-ch
	}
	modes := []string{"build", "plan"}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var f struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &f) != nil {
			continue
		}
		if f.Method == "" { // a response to one of our requests
			cmu.Lock()
			if ch := pending[string(f.ID)]; ch != nil {
				ch <- f.Result
				delete(pending, string(f.ID))
			}
			cmu.Unlock()
			continue
		}
		var p struct {
			SessionID string `json:"sessionId"`
			ModeID    string `json:"modeId"`
			ModelID   string `json:"modelId"`
			Prompt    []struct {
				Text string `json:"text"`
			} `json:"prompt"`
		}
		_ = json.Unmarshal(f.Params, &p)
		res := func(v any) { write(map[string]any{"jsonrpc": "2.0", "id": f.ID, "result": v}) }
		fail := func(msg string) {
			write(map[string]any{"jsonrpc": "2.0", "id": f.ID, "error": map[string]any{"code": -32602, "message": msg}})
		}
		switch f.Method {
		case "initialize":
			res(map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}})
		case "session/new":
			ms := []any{}
			for _, m := range modes {
				ms = append(ms, map[string]any{"id": m, "name": m})
			}
			res(map[string]any{"sessionId": "s1",
				"modes":  map[string]any{"availableModes": ms, "currentModeId": "build"},
				"models": map[string]any{"availableModels": []any{map[string]any{"modelId": "anthropic/claude-x"}}}})
		case "session/load":
			// History replay arrives BEFORE the load response.
			update(p.SessionID, map[string]any{"sessionUpdate": "agent_message_chunk",
				"content": map[string]any{"type": "text", "text": "OLD HISTORY"}})
			res(map[string]any{})
		case "session/set_mode":
			ok := false
			for _, m := range modes {
				ok = ok || m == p.ModeID
			}
			if !ok {
				fail("unknown mode")
				continue
			}
			res(map[string]any{})
		case "session/set_model":
			if p.ModelID != "anthropic/claude-x" {
				fail("unknown model")
				continue
			}
			res(map[string]any{})
		case "session/cancel":
			cmu.Lock()
			if ch := cancels[p.SessionID]; ch != nil {
				close(ch)
				delete(cancels, p.SessionID)
			}
			cmu.Unlock()
		case "session/prompt":
			text := ""
			if len(p.Prompt) > 0 {
				text = p.Prompt[0].Text
			}
			id, sid := f.ID, p.SessionID
			cancel := make(chan struct{})
			cmu.Lock()
			cancels[sid] = cancel
			cmu.Unlock()
			go func() {
				reply := func(v any) { write(map[string]any{"jsonrpc": "2.0", "id": id, "result": v}) }
				if strings.Contains(text, "PERMISSION") {
					r := ask("session/request_permission", map[string]any{"sessionId": sid,
						"toolCall": map[string]any{"toolCallId": "t0", "title": "rm -rf build"},
						"options": []any{
							map[string]any{"optionId": "no", "kind": "reject_once", "name": "Reject"},
							map[string]any{"optionId": "yes", "kind": "allow_once", "name": "Allow"},
						}})
					update(sid, map[string]any{"sessionUpdate": "agent_message_chunk",
						"content": map[string]any{"type": "text", "text": "permission:" + string(r)}})
				}
				if strings.Contains(text, "DIE") {
					fmt.Fprintln(os.Stderr, "fatal: out of memory")
					os.Exit(3)
				}
				if strings.Contains(text, "SILENT") {
					// A model call that failed, reported only in the log.
					fmt.Fprintln(os.Stderr, `level=ERROR message="stream error" error="model is not allowed"`)
					time.Sleep(20 * time.Millisecond)
					reply(map[string]any{"stopReason": "end_turn"})
					return
				}
				if strings.Contains(text, "WAIT") {
					<-cancel
					reply(map[string]any{"stopReason": "cancelled"})
					return
				}
				update(sid, map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "thinking"}})
				update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "echo: "}})
				update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}})
				update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "Read go.mod",
					"kind": "read", "status": "pending", "rawInput": map[string]any{"path": "go.mod"},
					"locations": []any{map[string]any{"path": "go.mod", "line": 3}}})
				update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed",
					"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "module x"}},
						map[string]any{"type": "diff", "path": "a.txt", "oldText": "a", "newText": "b"}}})
				update(sid, map[string]any{"sessionUpdate": "plan", "entries": []any{
					map[string]any{"content": "step one", "status": "completed", "priority": "high"}}})
				update(sid, map[string]any{"sessionUpdate": "usage_update", "used": 100, "size": 1000,
					"cost": map[string]any{"amount": 0.25, "currency": "USD"}})
				update(sid, map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{}})
				reply(map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": 10, "outputTokens": 5}})
			}()
		default:
			fail("method not found")
		}
	}
}

type testSidecar struct {
	t   *testing.T
	srv *httptest.Server
	sc  *sidecar
}

func newTestSidecar(t *testing.T, repo string) *testSidecar {
	t.Helper()
	evl := newEventLog()
	sc := &sidecar{repo: repo, log: evl, agent: newAgentHost(evl)}
	srv := httptest.NewServer(sc.routes())
	t.Cleanup(func() {
		sc.agent.stop()
		srv.Close()
	})
	return &testSidecar{t: t, srv: srv, sc: sc}
}

func (ts *testSidecar) post(path string, body any) (int, map[string]any) {
	ts.t.Helper()
	data, _ := json.Marshal(body)
	res, err := http.Post(ts.srv.URL+path, "application/json", bytes.NewReader(data))
	if err != nil {
		ts.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (ts *testSidecar) get(path string, v any) int {
	ts.t.Helper()
	res, err := http.Get(ts.srv.URL + path)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer res.Body.Close()
	_ = json.NewDecoder(res.Body).Decode(v)
	return res.StatusCode
}

// collect reads the event stream until stop says enough (or times out).
func (ts *testSidecar) collect(after int64, stop func(sideEvent) bool) []sideEvent {
	ts.t.Helper()
	res, err := http.Get(fmt.Sprintf("%s/agent/events?after=%d", ts.srv.URL, after))
	if err != nil {
		ts.t.Fatal(err)
	}
	defer res.Body.Close()
	evs := make(chan sideEvent)
	go func() {
		defer close(evs)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == "" {
				continue
			}
			var ev sideEvent
			if json.Unmarshal(sc.Bytes(), &ev) == nil {
				evs <- ev
			}
		}
	}()
	var out []sideEvent
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-evs:
			if !ok {
				return out
			}
			out = append(out, ev)
			if stop(ev) {
				return out
			}
		case <-timeout:
			ts.t.Fatalf("timed out; events so far: %+v", out)
			return out
		}
	}
}

func fakeAgentArgv(t *testing.T) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{"/usr/bin/env", "SHIM_FAKE_ACP=1", exe}
}

func TestSidecarAgentTurn(t *testing.T) {
	ts := newTestSidecar(t, t.TempDir())
	code, start := ts.post("/agent/start", startReq{Argv: fakeAgentArgv(t)})
	if code != 200 || start["boot"] == "" {
		t.Fatalf("start = %d %v", code, start)
	}
	// A wrong agent name is a clear error naming what IS available.
	code, bad := ts.post("/agent/session", sessionReq{Mode: "nope"})
	if code != http.StatusBadGateway || !strings.Contains(fmt.Sprint(bad["error"]), "build, plan") {
		t.Fatalf("bad mode = %d %v", code, bad)
	}
	code, sess := ts.post("/agent/session", sessionReq{Mode: "plan", Model: "anthropic/claude-x"})
	if code != 200 || sess["session_id"] != "s1" || sess["model"] != "anthropic/claude-x" ||
		fmt.Sprint(sess["models"]) != "[anthropic/claude-x]" || fmt.Sprint(sess["modes"]) != "[build plan]" {
		t.Fatalf("session = %d %v", code, sess)
	}
	code, _ = ts.post("/agent/prompt", promptReq{SessionID: "s1", Turn: "T1", Text: "hi"})
	if code != http.StatusAccepted {
		t.Fatalf("prompt = %d", code)
	}
	evs := ts.collect(0, func(ev sideEvent) bool { return ev.Type == "turn_end" })
	var types []string
	var text strings.Builder
	var tool *toolInfo
	for _, ev := range evs {
		types = append(types, ev.Type)
		if ev.Turn != "T1" && ev.Type != "gap" {
			t.Errorf("event %s not attributed to the turn: %+v", ev.Type, ev)
		}
		switch ev.Type {
		case "message":
			text.WriteString(ev.Text)
		case "tool_update":
			tool = ev.Tool
		}
	}
	want := "turn_start thought message message tool_call tool_update plan usage turn_end"
	if got := strings.Join(types, " "); got != want {
		t.Fatalf("events = %s\nwant      %s", got, want)
	}
	if text.String() != "echo: hi" {
		t.Errorf("message text = %q", text.String())
	}
	if tool == nil || tool.Status != "completed" || tool.Output != "module x" || len(tool.Diffs) != 1 || tool.Diffs[0].NewText != "b" {
		t.Errorf("tool update = %+v", tool)
	}
	last := evs[len(evs)-1]
	if last.StopReason != "end_turn" || last.Usage == nil || last.Usage.InputTokens != 10 {
		t.Errorf("turn_end = %+v", last)
	}
	var st hostStatus
	ts.get("/agent/status", &st)
	if len(st.Turns) != 0 || st.Sessions["s1"] != 0.25 || !st.Running {
		t.Errorf("status after turn = %+v", st)
	}

	// The stream resumes after a seq — what the engine does after a restart.
	mid := evs[3].Seq
	again := ts.collect(mid, func(ev sideEvent) bool { return ev.Type == "turn_end" })
	if len(again) != len(evs)-4 || again[0].Seq != mid+1 {
		t.Errorf("resume after %d returned %d events starting at %d", mid, len(again), again[0].Seq)
	}
}

func TestSidecarCancelAndBusy(t *testing.T) {
	ts := newTestSidecar(t, t.TempDir())
	ts.post("/agent/start", startReq{Argv: fakeAgentArgv(t)})
	ts.post("/agent/session", sessionReq{})
	if code, _ := ts.post("/agent/prompt", promptReq{SessionID: "s1", Turn: "T1", Text: "WAIT"}); code != 202 {
		t.Fatalf("prompt = %d", code)
	}
	// One turn at a time per session: the engine queues, the host refuses.
	if code, _ := ts.post("/agent/prompt", promptReq{SessionID: "s1", Turn: "T2", Text: "x"}); code != http.StatusConflict {
		t.Fatalf("second prompt = %d, want 409", code)
	}
	var st hostStatus
	ts.get("/agent/status", &st)
	if len(st.Turns) != 1 || st.Turns[0].ID != "T1" || st.Turns[0].Session != "s1" {
		t.Fatalf("status mid-turn = %+v", st)
	}
	if code, _ := ts.post("/agent/cancel", map[string]string{"session_id": "s1"}); code != http.StatusNoContent {
		t.Fatalf("cancel = %d", code)
	}
	evs := ts.collect(0, func(ev sideEvent) bool { return ev.Type == "turn_end" })
	if last := evs[len(evs)-1]; last.StopReason != "cancelled" {
		t.Fatalf("turn_end = %+v", last)
	}
}

// Two sessions of one process run their turns at the same time (a stage's
// and the Ask pane's), each turn's events attributed to its own session.
func TestSidecarSessionsRunTurnsSideBySide(t *testing.T) {
	ts := newTestSidecar(t, t.TempDir())
	ts.post("/agent/start", startReq{Argv: fakeAgentArgv(t)})
	ts.post("/agent/session", sessionReq{})                 // s1
	ts.post("/agent/session", sessionReq{SessionID: "ask"}) // loaded
	if code, _ := ts.post("/agent/prompt", promptReq{SessionID: "s1", Turn: "T1", Text: "WAIT"}); code != 202 {
		t.Fatalf("prompt s1 = %d", code)
	}
	if code, _ := ts.post("/agent/prompt", promptReq{SessionID: "ask", Turn: "A1", Text: "hi"}); code != 202 {
		t.Fatalf("prompt ask while s1 is in flight = %d, want 202", code)
	}
	evs := ts.collect(0, func(ev sideEvent) bool { return ev.Type == "turn_end" && ev.Session == "ask" })
	for _, ev := range evs {
		switch ev.Session {
		case "ask":
			if ev.Turn != "A1" {
				t.Errorf("ask event on turn %q: %+v", ev.Turn, ev)
			}
		case "s1":
			if ev.Turn != "T1" {
				t.Errorf("s1 event on turn %q: %+v", ev.Turn, ev)
			}
		}
	}
	var st hostStatus
	ts.get("/agent/status", &st)
	if len(st.Turns) != 1 || st.Turns[0].Session != "s1" {
		t.Fatalf("status after ask's turn = %+v", st)
	}
	ts.post("/agent/cancel", map[string]string{"session_id": "s1"})
	ts.collect(0, func(ev sideEvent) bool { return ev.Type == "turn_end" && ev.Session == "s1" })
}

// Two command lines are two processes side by side, each with its own
// sessions; a session request says which (proc).
func TestSidecarHostsSeveralAgentProcesses(t *testing.T) {
	ts := newTestSidecar(t, t.TempDir())
	_, a := ts.post("/agent/start", startReq{Argv: fakeAgentArgv(t)})
	_, b := ts.post("/agent/start", startReq{Argv: append(fakeAgentArgv(t), "--second")})
	pa, pb := fmt.Sprint(a["proc"]), fmt.Sprint(b["proc"])
	if pa == "" || pb == "" || pa == pb {
		t.Fatalf("procs = %q, %q", pa, pb)
	}
	// Ambiguous without a proc; fine with one.
	if code, out := ts.post("/agent/session", sessionReq{}); code != http.StatusBadGateway || !strings.Contains(fmt.Sprint(out["error"]), "several") {
		t.Fatalf("session with no proc = %d %v", code, out)
	}
	if code, _ := ts.post("/agent/session", sessionReq{Proc: pa, SessionID: "sa"}); code != 200 {
		t.Fatalf("session on a = %d", code)
	}
	if code, _ := ts.post("/agent/session", sessionReq{Proc: pb, SessionID: "sb"}); code != 200 {
		t.Fatalf("session on b = %d", code)
	}
	ts.post("/agent/prompt", promptReq{SessionID: "sa", Turn: "TA", Text: "hi"})
	ts.post("/agent/prompt", promptReq{SessionID: "sb", Turn: "TB", Text: "hi"})
	ends := 0
	ts.collect(0, func(ev sideEvent) bool {
		if ev.Type == "turn_end" {
			ends++
		}
		return ends == 2
	})
	var st hostStatus
	ts.get("/agent/status", &st)
	if !st.Running || len(st.Sessions) != 2 {
		t.Errorf("status = %+v", st)
	}
	// Starting a's command line again reuses a; b is untouched.
	_, again := ts.post("/agent/start", startReq{Argv: fakeAgentArgv(t)})
	if fmt.Sprint(again["proc"]) != pa {
		t.Errorf("restart reused %v, want %s", again["proc"], pa)
	}
	ts.get("/agent/status", &st)
	if len(st.Sessions) != 2 {
		t.Errorf("sessions after reuse = %+v", st.Sessions)
	}
}

// A read-only session's permission requests for mutating tools are refused.
func TestSidecarReadOnlySessionRefusesMutatingTools(t *testing.T) {
	ts := newTestSidecar(t, t.TempDir())
	ts.post("/agent/start", startReq{Argv: fakeAgentArgv(t)})
	if code, _ := ts.post("/agent/session", sessionReq{SessionID: "ro", ReadOnly: true}); code != 200 {
		t.Fatalf("session = %d", code)
	}
	ts.post("/agent/prompt", promptReq{SessionID: "ro", Turn: "T1", Text: "PERMISSION please"})
	evs := ts.collect(0, func(ev sideEvent) bool { return ev.Type == "turn_end" })
	var denied, answered bool
	for _, ev := range evs {
		if ev.Type == "permission" {
			denied = ev.Text == "denied (read-only session)"
		}
		if ev.Type == "message" && strings.Contains(ev.Text, "permission:") {
			answered = strings.Contains(ev.Text, `"optionId":"no"`)
		}
	}
	if !denied || !answered {
		t.Errorf("read-only permission: denied=%v answered-with-reject=%v in %+v", denied, answered, evs)
	}
}

func TestSidecarPermissionAutoApprovedAndLoadSkipsReplay(t *testing.T) {
	ts := newTestSidecar(t, t.TempDir())
	ts.post("/agent/start", startReq{Argv: fakeAgentArgv(t)})
	// Loading a session replays its history; the engine already has it.
	code, sess := ts.post("/agent/session", sessionReq{SessionID: "old"})
	if code != 200 || sess["loaded"] != true {
		t.Fatalf("load = %d %v", code, sess)
	}
	ts.post("/agent/prompt", promptReq{SessionID: "old", Turn: "T1", Text: "PERMISSION please"})
	evs := ts.collect(0, func(ev sideEvent) bool { return ev.Type == "turn_end" })
	var sawPerm bool
	for _, ev := range evs {
		if ev.Type == "message" && strings.Contains(ev.Text, "OLD HISTORY") {
			t.Error("replayed history was re-emitted")
		}
		if ev.Type == "message" && strings.Contains(ev.Text, "permission:") && !strings.Contains(ev.Text, `"optionId":"yes"`) {
			t.Errorf("permission answer = %s, want the allow option", ev.Text)
		}
		if ev.Type == "permission" {
			sawPerm = ev.Text == "auto-approved" && ev.Tool != nil && ev.Tool.Title == "rm -rf build"
		}
	}
	if !sawPerm {
		t.Errorf("no auto-approved permission event in %+v", evs)
	}
}

func TestSidecarAgentExit(t *testing.T) {
	ts := newTestSidecar(t, t.TempDir())
	code, out := ts.post("/agent/start", startReq{Argv: []string{"sh", "-c", "echo boom >&2; exit 3"}})
	if code != http.StatusBadGateway || !strings.Contains(fmt.Sprint(out["error"]), "boom") {
		t.Fatalf("start of a dying agent = %d %v", code, out)
	}
}

func TestModelCandidates(t *testing.T) {
	got := modelCandidates("acme/alpha-5", []string{"alpha-5/high", "alpha-5", "o3"})
	if strings.Join(got, ",") != "acme/alpha-5,alpha-5,alpha-5/high" {
		t.Errorf("candidates = %v", got)
	}
}

func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(p, s string) {
		full := filepath.Join(dir, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	write("README.md", "hello\n")
	write("src/old.go", "package old\n")
	write(".gitignore", "node_modules/\n")
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	run("switch", "-q", "-c", "work")
	write("README.md", "hello\nworld\n")
	run("commit", "-qam", "edit readme")
	write("src/new.go", "package src\n\nfunc New() {}\n") // untracked
	_ = os.Remove(filepath.Join(dir, "src/old.go"))       // deleted, uncommitted
	write("node_modules/x.js", "x")                       // ignored
	return dir
}

func TestSidecarFilesAndGit(t *testing.T) {
	repo := gitInit(t)
	ts := newTestSidecar(t, repo)

	var tree []treeEntry
	if code := ts.get("/fs/tree", &tree); code != 200 {
		t.Fatalf("tree = %d", code)
	}
	var names []string
	for _, e := range tree {
		n := e.Name
		if e.Dir {
			n += "/"
		}
		if e.Ignored {
			n += "(ignored)"
		}
		names = append(names, n)
	}
	if got := strings.Join(names, " "); got != "node_modules/(ignored) src/ .gitignore README.md" {
		t.Errorf("tree = %s", got)
	}
	var f fileResp
	if code := ts.get("/fs/file?path=README.md", &f); code != 200 || f.Content != "hello\nworld\n" {
		t.Errorf("file = %d %+v", code, f)
	}
	var e map[string]string
	if code := ts.get("/fs/file?path=../etc/passwd", &e); code != 400 {
		t.Errorf("escape = %d", code)
	}
	// A symlink out of the repo is refused even though its name looks local.
	_ = os.Symlink("/etc", filepath.Join(repo, "etc"))
	if code := ts.get("/fs/file?path=etc/hosts", &e); code != 400 {
		t.Errorf("symlink escape = %d", code)
	}
	_ = os.Remove(filepath.Join(repo, "etc"))

	var ch changesResp
	if code := ts.get("/git/changes?base=main", &ch); code != 200 {
		t.Fatalf("changes = %d", code)
	}
	if ch.Branch != "work" || len(ch.Commits) != 1 || ch.Commits[0].Subject != "edit readme" {
		t.Errorf("changes header = %+v", ch)
	}
	got := map[string]changedFile{}
	for _, f := range ch.Files {
		got[f.Path] = f
	}
	if f := got["README.md"]; f.Status != "M" || !f.Committed || f.Additions != 1 {
		t.Errorf("README = %+v", f)
	}
	if f := got["src/old.go"]; f.Status != "D" || f.Committed || f.Deletions != 1 {
		t.Errorf("old.go = %+v", f)
	}
	if f := got["src/new.go"]; f.Status != "?" || f.Additions != 3 {
		t.Errorf("new.go = %+v", f)
	}
	if _, ok := got["node_modules/x.js"]; ok || len(got) != 3 {
		t.Errorf("files = %+v", ch.Files)
	}
	var d diffResp
	ts.get("/git/diff?base=main&path=README.md", &d)
	if !strings.Contains(d.Diff, "+world") {
		t.Errorf("README diff = %q", d.Diff)
	}
	ts.get("/git/diff?base=main&path=src/new.go", &d)
	if !strings.Contains(d.Diff, "+func New() {}") {
		t.Errorf("untracked diff = %q", d.Diff)
	}
	ts.get("/git/diff?base=main&path=.gitignore", &d)
	if d.Diff != "" {
		t.Errorf("unchanged file has a diff: %q", d.Diff)
	}
}

// A turn that ends "normally" with no output but an error in the agent's log
// is the model call failing silently: it must surface as a failed turn.
func TestSidecarSilentFailureBecomesAnError(t *testing.T) {
	ts := newTestSidecar(t, t.TempDir())
	ts.post("/agent/start", startReq{Argv: fakeAgentArgv(t)})
	ts.post("/agent/session", sessionReq{})
	ts.post("/agent/prompt", promptReq{SessionID: "s1", Turn: "T1", Text: "SILENT"})
	evs := ts.collect(0, func(ev sideEvent) bool { return ev.Type == "turn_end" })
	last := evs[len(evs)-1]
	if last.StopReason != "error" || !strings.Contains(last.Error, "model is not allowed") {
		t.Fatalf("turn_end = %+v", last)
	}
	// A turn that did produce output is never second-guessed.
	ts.post("/agent/prompt", promptReq{SessionID: "s1", Turn: "T2", Text: "fine"})
	evs = ts.collect(last.Seq, func(ev sideEvent) bool { return ev.Type == "turn_end" })
	if end := evs[len(evs)-1]; end.StopReason != "end_turn" || end.Error != "" {
		t.Fatalf("normal turn_end = %+v", end)
	}
}

// ?repo= addresses one of a multi-repo run's extra checkouts; anything the
// workspace does not have, or a name that is not a checkout name, is refused.
func TestSidecarRepoSelector(t *testing.T) {
	primary := gitInit(t)
	extra := gitInit(t)
	repos := t.TempDir()
	if err := os.Symlink(extra, filepath.Join(repos, "api")); err != nil {
		t.Fatal(err)
	}
	evl := newEventLog()
	sc := &sidecar{repo: primary, repos: repos, log: evl, agent: newAgentHost(evl)}
	srv := httptest.NewServer(sc.routes())
	defer srv.Close()
	ts := &testSidecar{t: t, srv: srv, sc: sc}

	var tree []treeEntry
	if code := ts.get("/fs/tree?repo=api", &tree); code != 200 || len(tree) == 0 {
		t.Fatalf("extra tree = %d %v", code, tree)
	}
	var ch changesResp
	if code := ts.get("/git/changes?repo=api&base=main", &ch); code != 200 || ch.Branch != "work" {
		t.Errorf("extra changes = %d %+v", code, ch)
	}
	var e map[string]string
	for _, bad := range []string{"nope", "../etc", "Api"} {
		if code := ts.get("/fs/tree?repo="+bad, &e); code != 404 {
			t.Errorf("repo %q = %d, want 404", bad, code)
		}
	}
	// "" and "repo" are the primary.
	if code := ts.get("/fs/tree?repo=repo", &tree); code != 200 {
		t.Errorf("primary by name = %d", code)
	}
}

// A body that is not JSON is refused on every route that takes one.
func TestSidecarRefusesBadBodies(t *testing.T) {
	ts := newTestSidecar(t, t.TempDir())
	for _, path := range []string{"/agent/start", "/agent/session", "/agent/prompt", "/agent/cancel"} {
		res, err := http.Post(ts.srv.URL+path, "application/json", strings.NewReader("{nope"))
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]string
		_ = json.NewDecoder(res.Body).Decode(&out)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest || !strings.Contains(out["error"], "invalid body") {
			t.Errorf("%s = %d %v", path, res.StatusCode, out)
		}
	}
}

// With no agent there is no session, turn or cancel to have: each says so
// with the status the engine tells apart.
func TestSidecarWithNoAgent(t *testing.T) {
	ts := newTestSidecar(t, t.TempDir())
	if code, out := ts.post("/agent/session", sessionReq{}); code != http.StatusConflict {
		t.Errorf("session = %d %v", code, out)
	}
	if code, out := ts.post("/agent/session", sessionReq{Proc: "nope"}); code != http.StatusConflict {
		t.Errorf("session on an unknown proc = %d %v", code, out)
	}
	if code, out := ts.post("/agent/prompt", promptReq{SessionID: "ghost", Text: "x"}); code != http.StatusBadRequest ||
		!strings.Contains(fmt.Sprint(out["error"]), "not live") {
		t.Errorf("prompt = %d %v", code, out)
	}
	if code, _ := ts.post("/agent/cancel", map[string]string{"session_id": "ghost"}); code != http.StatusConflict {
		t.Errorf("cancel = %d", code)
	}
	if code, out := ts.post("/agent/start", startReq{}); code != http.StatusBadGateway || !strings.Contains(fmt.Sprint(out["error"]), "argv") {
		t.Errorf("start with no argv = %d %v", code, out)
	}
	if code, out := ts.post("/agent/start", startReq{Argv: []string{"/no/such/agent"}}); code != http.StatusBadGateway ||
		!strings.Contains(fmt.Sprint(out["error"]), "start agent") {
		t.Errorf("start of nothing = %d %v", code, out)
	}
	var st hostStatus
	ts.get("/agent/status", &st)
	if st.Running || st.Boot == "" || len(st.Sessions) != 0 {
		t.Errorf("status = %+v", st)
	}
}

// An agent dying mid-turn ends the turn with its last words, says which
// process went, and leaves the host ready for a new one.
func TestSidecarAgentDiesMidTurn(t *testing.T) {
	ts := newTestSidecar(t, t.TempDir())
	_, start := ts.post("/agent/start", startReq{Argv: fakeAgentArgv(t)})
	ts.post("/agent/session", sessionReq{})
	ts.post("/agent/prompt", promptReq{SessionID: "s1", Turn: "T1", Text: "DIE"})
	saw := map[string]sideEvent{}
	ts.collect(0, func(ev sideEvent) bool {
		saw[ev.Type] = ev
		return saw["turn_end"].Type != "" && saw["agent_exit"].Type != ""
	})
	if end := saw["turn_end"]; end.StopReason != "error" || !strings.Contains(end.Error, errAgentGone.Error()) || !strings.Contains(end.Error, "out of memory") {
		t.Errorf("turn_end = %+v", end)
	}
	if ex := saw["agent_exit"]; ex.Proc != start["proc"] || ex.Error != "agent exited (code 3)" || !strings.Contains(ex.Text, "out of memory") {
		t.Errorf("agent_exit = %+v", ex)
	}
	var st hostStatus
	ts.get("/agent/status", &st)
	if st.Running || len(st.Sessions) != 0 || len(st.Turns) != 0 {
		t.Errorf("status after the agent died = %+v", st)
	}
	if code, _ := ts.post("/agent/prompt", promptReq{SessionID: "s1", Turn: "T2", Text: "x"}); code != http.StatusBadRequest {
		t.Errorf("prompt to a dead session = %d", code)
	}
	if code, _ := ts.post("/agent/start", startReq{Argv: fakeAgentArgv(t)}); code != 200 {
		t.Errorf("restart = %d", code)
	}
	if code, _ := ts.post("/agent/session", sessionReq{}); code != 200 {
		t.Errorf("session after the restart = %d", code)
	}
}

// A stop is the host's own doing: the processes go, with no exit to report.
func TestSidecarAgentStop(t *testing.T) {
	ts := newTestSidecar(t, t.TempDir())
	ts.post("/agent/start", startReq{Argv: fakeAgentArgv(t)})
	ts.post("/agent/session", sessionReq{})
	before := ts.sc.log.last()
	if code, _ := ts.post("/agent/stop", nil); code != http.StatusNoContent {
		t.Fatalf("stop = %d", code)
	}
	var st hostStatus
	ts.get("/agent/status", &st)
	if st.Running || len(st.Sessions) != 0 || st.LastSeq != before {
		t.Errorf("status after stop = %+v (seq was %d)", st, before)
	}
}

func (ts *testSidecar) lookBack(query string) []sideEvent {
	ts.t.Helper()
	res, err := http.Get(ts.srv.URL + "/agent/events?follow=0&" + query)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer res.Body.Close()
	var out []sideEvent
	dec := json.NewDecoder(res.Body)
	for {
		var ev sideEvent
		if err := dec.Decode(&ev); err != nil {
			return out
		}
		out = append(out, ev)
	}
}

// follow=0 is a look back. A reader of another boot is told its seqs mean
// nothing here; one that fell behind the buffer, that it lost events.
func TestSidecarEventsGaps(t *testing.T) {
	ts := newTestSidecar(t, t.TempDir())
	ts.sc.log.emit(sideEvent{Type: "message", Text: "one"})
	ts.sc.log.emit(sideEvent{Type: "message", Text: "two"})

	if evs := ts.lookBack("after=1"); len(evs) != 1 || evs[0].Text != "two" {
		t.Errorf("after=1 = %+v", evs)
	}
	if evs := ts.lookBack("after=1&boot=" + ts.sc.log.boot); len(evs) != 1 {
		t.Errorf("same boot = %+v", evs)
	}
	evs := ts.lookBack("after=2&boot=other")
	if len(evs) != 3 || evs[0].Type != "gap" || evs[0].Boot != ts.sc.log.boot || evs[1].Text != "one" {
		t.Errorf("another boot = %+v", evs)
	}

	for range maxEventsBuf {
		ts.sc.log.emit(sideEvent{Type: "message"})
	}
	evs = ts.lookBack("after=1")
	if len(evs) != maxEventsBuf+1 || evs[0].Type != "gap" || !strings.Contains(evs[0].Text, "dropped") {
		t.Errorf("behind the buffer: %d events, first %+v", len(evs), evs[0])
	}
	if evs := ts.lookBack("after=" + fmt.Sprint(ts.sc.log.last()-1)); len(evs) != 1 {
		t.Errorf("caught up = %d events", len(evs))
	}
}

// A quiet stream still shows the reader the connection is alive, and a reader
// that left is noticed.
func TestSidecarEventsKeepalive(t *testing.T) {
	old := eventsKeepalive
	eventsKeepalive = 5 * time.Millisecond
	t.Cleanup(func() { eventsKeepalive = old })
	ts := newTestSidecar(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.srv.URL+"/agent/events", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if line, err := bufio.NewReader(res.Body).ReadString('\n'); err != nil || line != "\n" {
		t.Errorf("keepalive = %q, %v", line, err)
	}
}

type noFlush struct{ http.ResponseWriter }

func TestSidecarEventsNeedStreaming(t *testing.T) {
	s := &sidecar{log: newEventLog()}
	rec := httptest.NewRecorder()
	s.agentEvents(noFlush{rec}, httptest.NewRequest("GET", "/agent/events", nil))
	if rec.Code != 500 {
		t.Errorf("code = %d", rec.Code)
	}
}

// Every workspace route answers for the checkout it was asked about: 404 for
// one this workspace does not have, and its own errors otherwise.
func TestSidecarWorkspaceErrors(t *testing.T) {
	repo := gitInit(t)
	ts := newTestSidecar(t, repo)
	var e map[string]string
	for _, path := range []string{"/fs/tree", "/fs/file?path=README.md", "/git/changes", "/git/diff?path=README.md", "/term"} {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		if code := ts.get(path+sep+"repo=nope", &e); code != 404 {
			t.Errorf("%s of an unknown repo = %d", path, code)
		}
	}
	for path, want := range map[string]int{
		"/fs/tree?path=missing": 404,
		"/fs/tree?path=../..":   400,
		"/fs/file?path=missing": 404,
		"/fs/file?path=src":     500,
		"/git/diff?path=../x":   400,
		"/git/diff?path=":       400,
		"/term":                 400, // no upgrade
	} {
		if code := ts.get(path, &e); code != want {
			t.Errorf("%s = %d, want %d", path, code, want)
		}
	}
	ts2 := newTestSidecar(t, t.TempDir())
	if code := ts2.get("/git/changes", &e); code != http.StatusConflict {
		t.Errorf("changes outside a checkout = %d", code)
	}
}

// /health reports the checkout, and the directory it resolves to: the
// devcontainer's workspace folder, what an editor opens.
func TestSidecarHealth(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "workspaces", "app")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "repo")
	if err := os.Symlink(folder, link); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(folder)
	ts := newTestSidecar(t, link)
	var h map[string]string
	if code := ts.get("/health", &h); code != 200 || h["repo"] != link || h["workspace"] != real || h["boot"] != ts.sc.log.boot {
		t.Errorf("health = %d %v", code, h)
	}
	gone := newTestSidecar(t, filepath.Join(dir, "gone"))
	if gone.get("/health", &h); h["workspace"] != "" {
		t.Errorf("workspace of a missing repo = %q", h["workspace"])
	}
}

func unixClient(sock string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
}

func waitServing(t *testing.T, sock string) {
	t.Helper()
	waitFor(t, func() bool {
		c, err := net.Dial("unix", sock)
		if err != nil {
			return false
		}
		c.Close()
		return true
	})
}

func sidecarHealth(t *testing.T, sock string) map[string]string {
	t.Helper()
	res, err := unixClient(sock).Get("http://sidecar/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var h map[string]string
	if err := json.NewDecoder(res.Body).Decode(&h); err != nil {
		t.Fatal(err)
	}
	return h
}

// The subcommand as the provisioner starts it: the socket open to the engine
// whatever its uid, SSH beside it, $HOME's checkout by default.
func TestRunSidecar(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "sidecar.sock")
	cmd := shimCmd(t, "sidecar", "--listen", sock)
	cmd.Env = append(cmd.Env, "HOME="+dir)
	startShim(t, cmd)
	waitServing(t, sock)
	h := sidecarHealth(t, sock)
	if h["repo"] != filepath.Join(dir, "repo") || h["boot"] == "" {
		t.Errorf("health = %v", h)
	}
	if info, err := os.Stat(sock); err != nil || info.Mode().Perm() != 0o666 {
		t.Errorf("socket mode = %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ssh.sock")); err != nil {
		t.Errorf("no ssh socket beside it: %v", err)
	}

	// Singleton: a second one finds the live socket and leaves, the first
	// keeps its boot.
	if r := shim(t, "", "sidecar", "--listen", sock); r.code != 0 || r.stderr != "" {
		t.Errorf("a second sidecar = %+v", r)
	}
	if again := sidecarHealth(t, sock); again["boot"] != h["boot"] {
		t.Errorf("the sidecar was replaced: %v, %v", h, again)
	}

	// A dead sidecar's socket file does not stop the next one.
	stopShim(cmd)
	next := shimCmd(t, "sidecar", "--listen", sock, "--repo", dir, "--repos", dir, "--ssh", "off")
	startShim(t, next)
	waitServing(t, sock)
	if got := sidecarHealth(t, sock); got["boot"] == h["boot"] || got["repo"] != dir {
		t.Errorf("after the restart = %v", got)
	}
}

// SSH never decides whether a sidecar serves; the HTTP socket does.
func TestRunSidecarSocketFailures(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "sidecar.sock")
	var stderr bytes.Buffer
	cmd := shimCmd(t, "sidecar", "--listen", sock, "--repo", dir, "--repos", dir, "--ssh", filepath.Join(dir, "missing", "ssh.sock"))
	cmd.Stderr = &stderr
	startShim(t, cmd)
	waitServing(t, sock)
	if h := sidecarHealth(t, sock); h["repo"] != dir {
		t.Errorf("health = %v", h)
	}
	stopShim(cmd)
	if !strings.Contains(stderr.String(), "ssh: not serving on") {
		t.Errorf("stderr = %q", stderr.String())
	}

	r := shim(t, "", "sidecar", "--listen", filepath.Join(dir, "missing", "s.sock"), "--repo", dir, "--repos", dir)
	if r.code != 1 || !strings.Contains(r.stderr, "sidecar: listen") {
		t.Errorf("no socket = %+v", r)
	}
}
