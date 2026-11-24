//go:build linux

package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// wsClient is the client half of the protocol, just enough to drive /term.
type wsClient struct {
	c  net.Conn
	br *bufio.Reader
}

func dialWS(t *testing.T, addr, path string) *wsClient {
	t.Helper()
	c, res := upgrade(t, addr, path)
	if res.StatusCode != 101 {
		t.Fatalf("upgrade: %v", res.Status)
	}
	return c
}

// upgrade asks for the WebSocket and returns the answer, whatever it is.
func upgrade(t *testing.T, addr, path string) (*wsClient, *http.Response) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	req := "GET " + path + " HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\n\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	return &wsClient{c: c, br: br}, res
}

func (w *wsClient) send(op byte, data []byte) {
	hdr := []byte{0x80 | op}
	if len(data) < 126 {
		hdr = append(hdr, 0x80|byte(len(data)))
	} else {
		hdr = append(hdr, 0x80|126, byte(len(data)>>8), byte(len(data)))
	}
	mask := []byte{1, 2, 3, 4}
	hdr = append(hdr, mask...)
	masked := make([]byte, len(data))
	for i := range data {
		masked[i] = data[i] ^ mask[i%4]
	}
	_, _ = w.c.Write(append(hdr, masked...))
}

// frame reads one server frame.
func (w *wsClient) frame() (op byte, data []byte, err error) {
	var h [2]byte
	if _, err := io.ReadFull(w.br, h[:]); err != nil {
		return 0, nil, err
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		_, _ = io.ReadFull(w.br, b[:])
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		_, _ = io.ReadFull(w.br, b[:])
		n = binary.BigEndian.Uint64(b[:])
	}
	data = make([]byte, n)
	_, err = io.ReadFull(w.br, data)
	return h[0] & 0x0f, data, err
}

// readUntil reads frames, text and binary alike, until want is among them.
func (w *wsClient) readUntil(t *testing.T, want string) string {
	t.Helper()
	_ = w.c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var got strings.Builder
	for !strings.Contains(got.String(), want) {
		_, data, err := w.frame()
		if err != nil {
			t.Fatalf("read: %v (so far %q)", err, tailOf(got.String()))
		}
		got.Write(data)
	}
	return got.String()
}

func tailOf(s string) string { return s[max(0, len(s)-400):] }

// termHello is the session frame every attach starts with.
type termHello struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	N       int    `json:"n"`
	Created bool   `json:"created"`
}

func (w *wsClient) hello(t *testing.T) termHello {
	t.Helper()
	_ = w.c.SetReadDeadline(time.Now().Add(10 * time.Second))
	op, data, err := w.frame()
	var h termHello
	if err != nil || op != wsOpText || json.Unmarshal(data, &h) != nil || h.Type != "session" || h.ID == "" {
		t.Fatalf("first frame = %d %q %v, want the session frame", op, data, err)
	}
	return h
}

func addr(ts *testSidecar) string { return strings.TrimPrefix(ts.srv.URL, "http://") }

func (ts *testSidecar) sessions() []termSessionInfo {
	ts.t.Helper()
	var out []termSessionInfo
	if code := ts.get("/term/sessions", &out); code != 200 {
		ts.t.Fatalf("GET /term/sessions = %d", code)
	}
	return out
}

// waitSessions polls the list until ok says it is what the test waits for.
func (ts *testSidecar) waitSessions(what string, ok func([]termSessionInfo) bool) []termSessionInfo {
	ts.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		got := ts.sessions()
		if ok(got) {
			return got
		}
		if time.Now().After(deadline) {
			ts.t.Fatalf("%s: sessions = %+v", what, got)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (ts *testSidecar) del(path string) int {
	ts.t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, ts.srv.URL+path, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		ts.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestSidecarTerminal(t *testing.T) {
	ts := newTestSidecar(t, t.TempDir())
	ws := dialWS(t, addr(ts), "/term?cols=80&rows=24")
	if h := ws.hello(t); !h.Created || h.N != 1 || len(h.ID) != 16 {
		t.Errorf("hello = %+v", h)
	}
	ws.send(wsOpText, []byte(`{"type":"resize","cols":100,"rows":30}`))
	ws.send(wsOpBinary, []byte("stty size; echo marker-$((40+2))\n"))
	out := ws.readUntil(t, "marker-42")
	if !strings.Contains(out, "30 100") {
		t.Errorf("resize not applied: %q", out)
	}
}

// Keystrokes arrive binary or as plain text; a control message it does not
// know is neither, and a shell that exits ends the session: the client is
// told, then closed, and the list drops it.
func TestSidecarTerminalInput(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ts := newTestSidecar(t, t.TempDir())
	ws := dialWS(t, addr(ts), "/term")
	ws.hello(t)
	ws.send(wsOpText, []byte(`{"type":"telemetry"}`))
	ws.send(wsOpText, []byte("echo typed-$((1+1))\n"))
	if out := ws.readUntil(t, "typed-2"); strings.Contains(out, "telemetry") {
		t.Errorf("a control message reached the shell: %q", out)
	}
	ws.send(wsOpBinary, []byte("exit\n"))
	ws.readUntil(t, `{"type":"exit"}`)
	_ = ws.c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadAll(ws.br); err != nil {
		t.Errorf("after the shell's exit: %v, want the stream closed", err)
	}
	if got := ts.sessions(); len(got) != 0 {
		t.Errorf("an exited shell is still listed: %+v", got)
	}
}

// A client that says goodbye gets a close back, and the shell lives on: the
// next attach by id is the same shell, its earlier output replayed first.
func TestSidecarTerminalSurvivesTheClient(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ts := newTestSidecar(t, t.TempDir())
	ws := dialWS(t, addr(ts), "/term")
	h := ws.hello(t)
	ws.send(wsOpBinary, []byte("echo shell=$$=; echo before-$((1+1))\n"))
	out := ws.readUntil(t, "before-2")
	m := regexp.MustCompile(`shell=(\d+)=`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no pid in %q", out)
	}
	pid, _ := strconv.Atoi(m[1])
	ws.send(wsOpClose, []byte{0x03, 0xe8})
	_ = ws.c.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		op, _, err := ws.frame()
		if err != nil {
			t.Fatalf("goodbye answered %v, want a close", err)
		}
		if op == wsOpClose {
			break
		}
	}
	ts.waitSessions("detached", func(s []termSessionInfo) bool { return len(s) == 1 && s[0].Attached == 0 })
	if !groupAlive(pid) {
		t.Fatal("the shell went with its client")
	}

	again := dialWS(t, addr(ts), "/term?session="+h.ID+"&cols=90&rows=20")
	if h2 := again.hello(t); h2.ID != h.ID || h2.Created || h2.N != h.N {
		t.Errorf("re-attach hello = %+v, want %+v not created", h2, h)
	}
	again.readUntil(t, "before-2") // the replay
	again.send(wsOpBinary, []byte("echo again=$$=; stty size\n"))
	if out := again.readUntil(t, "20 90"); !strings.Contains(out, "again="+m[1]+"=") {
		t.Errorf("a different shell, or the attach's size not applied: %q", out)
	}
}

// Two clients on one session see the same output, and either one types.
func TestSidecarTerminalSharedByTwo(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ts := newTestSidecar(t, t.TempDir())
	a := dialWS(t, addr(ts), "/term")
	h := a.hello(t)
	b := dialWS(t, addr(ts), "/term?session="+h.ID)
	b.hello(t)
	ts.waitSessions("two attached", func(s []termSessionInfo) bool { return len(s) == 1 && s[0].Attached == 2 })
	a.send(wsOpBinary, []byte("echo from-a-$((1+1))\n"))
	b.readUntil(t, "from-a-2")
	b.send(wsOpBinary, []byte("echo from-b-$((2+2))\n"))
	a.readUntil(t, "from-b-4")
}

// Closing a session hangs its shell up: every client is told and closed,
// and the list drops it.
func TestSidecarTerminalDelete(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ts := newTestSidecar(t, t.TempDir())
	ws := dialWS(t, addr(ts), "/term")
	h := ws.hello(t)
	ws.send(wsOpBinary, []byte("echo shell=$$=; echo up-$((1+1))\n"))
	out := ws.readUntil(t, "up-2")
	pid, _ := strconv.Atoi(regexp.MustCompile(`shell=(\d+)=`).FindStringSubmatch(out)[1])
	if code := ts.del("/term/sessions/" + h.ID); code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", code)
	}
	ws.readUntil(t, `{"type":"exit"}`)
	_ = ws.c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadAll(ws.br); err != nil {
		t.Errorf("after the close: %v, want the stream closed", err)
	}
	if got := ts.sessions(); len(got) != 0 {
		t.Errorf("a closed session is still listed: %+v", got)
	}
	waitGone(t, pid)
	if code := ts.del("/term/sessions/" + h.ID); code != http.StatusNotFound {
		t.Errorf("DELETE again = %d, want 404", code)
	}
}

// An unknown session and one past the cap are refused before the upgrade.
func TestSidecarTerminalRefusals(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ts := newTestSidecar(t, t.TempDir())
	if _, res := upgrade(t, addr(ts), "/term?session=0123456789abcdef"); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown session = %d, want 404", res.StatusCode)
	}
	// A request that is no WebSocket leaves no session behind it.
	res, err := http.Get(ts.srv.URL + "/term")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("no upgrade = %d, want 400", res.StatusCode)
	}
	ts.waitSessions("no upgrade", func(s []termSessionInfo) bool { return len(s) == 0 })
	for i := range termMaxSessions {
		ws := dialWS(t, addr(ts), "/term")
		if h := ws.hello(t); h.N != i+1 {
			t.Errorf("session %d numbered %d", i+1, h.N)
		}
	}
	if _, res := upgrade(t, addr(ts), "/term"); res.StatusCode != http.StatusConflict {
		t.Errorf("past the cap = %d, want 409", res.StatusCode)
	}
	if _, res := upgrade(t, addr(ts), "/term?repo=nope"); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown checkout = %d, want 404", res.StatusCode)
	}
}

// The replay is the newest termRingSize bytes: older output is gone. The
// flood runs with nobody attached: a client reading it live may fall
// termClientQueue chunks behind on a loaded machine and be dropped, which is
// publish's design, not what this test is about.
func TestSidecarTerminalReplayIsBounded(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ts := newTestSidecar(t, t.TempDir())
	ws := dialWS(t, addr(ts), "/term")
	h := ws.hello(t)
	ws.send(wsOpBinary, []byte(fmt.Sprintf("echo OLD-$((1+1)); yes x | head -c %d; echo NEW-$((2+2))\n", 2*termRingSize)))
	ws.c.Close()
	ts.waitSessions("detached", func(s []termSessionInfo) bool { return len(s) == 1 && s[0].Attached == 0 })
	// Wait for the end of the flood in the ring itself, so the replay holds
	// it and nothing of it arrives live after the replay.
	sess := ts.sc.terms.get(h.ID)
	deadline := time.Now().Add(30 * time.Second)
	for {
		sess.mu.Lock()
		done := strings.Contains(string(sess.ring.snapshot()), "NEW-4")
		sess.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the flood never reached the ring")
		}
		time.Sleep(20 * time.Millisecond)
	}

	again := dialWS(t, addr(ts), "/term?session="+h.ID)
	again.hello(t)
	got := again.readUntil(t, "NEW-4")
	if strings.Contains(got, "OLD-2") {
		t.Error("the replay reached past the ring")
	}
	if len(got) > termRingSize || len(got) < termRingSize/2 {
		t.Errorf("replayed %d bytes, want at most %d", len(got), termRingSize)
	}
}

// What runs in a session: nothing at the prompt; a foreground program, by
// name, attached or not; nothing again after Ctrl-C; a background job too.
func TestSidecarTerminalBusy(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ts := newTestSidecar(t, t.TempDir())
	ws := dialWS(t, addr(ts), "/term")
	h := ws.hello(t)
	ws.send(wsOpBinary, []byte("echo ready-$((1+1))\n"))
	ws.readUntil(t, "ready-2")
	idle := func(s []termSessionInfo) bool { return len(s) == 1 && !s[0].Busy && s[0].Command == "" }
	running := func(s []termSessionInfo) bool { return len(s) == 1 && s[0].Busy && s[0].Command == "sleep" }
	ts.waitSessions("at the prompt", idle)

	ws.send(wsOpBinary, []byte("sleep 300\n"))
	ts.waitSessions("a foreground program", running)
	ws.c.Close()
	ts.waitSessions("detached", func(s []termSessionInfo) bool { return len(s) == 1 && s[0].Attached == 0 })
	ts.waitSessions("still running, detached", running)

	again := dialWS(t, addr(ts), "/term?session="+h.ID)
	again.hello(t)
	again.send(wsOpBinary, []byte{0x03})
	ts.waitSessions("after Ctrl-C", idle)

	again.send(wsOpBinary, []byte("sleep 300 &\n"))
	ts.waitSessions("a background job", running)
	if code := ts.del("/term/sessions/" + h.ID); code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", code)
	}
	ts.waitSessions("closed", func(s []termSessionInfo) bool { return len(s) == 0 })
}

// With no shell to start, the terminal says why in the stream.
func TestSidecarTerminalUnavailable(t *testing.T) {
	t.Setenv("SHELL", "/no/such/shell")
	ts := newTestSidecar(t, t.TempDir())
	ws := dialWS(t, addr(ts), "/term")
	ws.readUntil(t, "terminal unavailable")
	if got := ts.sessions(); len(got) != 0 {
		t.Errorf("a shell that never started is listed: %+v", got)
	}
}

func TestResizePTY(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	if err := resizePTY(devNull, 0, 24); err != nil {
		t.Errorf("no size to set: %v", err)
	}
	if err := resizePTY(devNull, 80, 24); err == nil {
		t.Error("resized something that is no terminal")
	}
	if _, err := ptyForeground(devNull); err == nil {
		t.Error("a foreground group of something that is no terminal")
	}
	devNull.Close()
	if err := resizePTY(devNull, 80, 24); err == nil {
		t.Error("resized a closed file")
	}
}
