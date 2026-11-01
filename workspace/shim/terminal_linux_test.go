//go:build linux

package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
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
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	req := "GET " + path + " HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\n\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, nil)
	if err != nil || res.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", res, err)
	}
	return &wsClient{c: c, br: br}
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

func (w *wsClient) readUntil(t *testing.T, want string) string {
	t.Helper()
	_ = w.c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var got strings.Builder
	for !strings.Contains(got.String(), want) {
		var h [2]byte
		if _, err := io.ReadFull(w.br, h[:]); err != nil {
			t.Fatalf("read: %v (so far %q)", err, got.String())
		}
		n := int(h[1] & 0x7f)
		if n == 126 {
			var b [2]byte
			_, _ = io.ReadFull(w.br, b[:])
			n = int(binary.BigEndian.Uint16(b[:]))
		}
		data := make([]byte, n)
		_, _ = io.ReadFull(w.br, data)
		got.Write(data)
	}
	return got.String()
}

func TestSidecarTerminal(t *testing.T) {
	ts := newTestSidecar(t, t.TempDir())
	ws := dialWS(t, strings.TrimPrefix(ts.srv.URL, "http://"), "/term?cols=80&rows=24")
	defer ws.c.Close()
	ws.send(wsOpText, []byte(`{"type":"resize","cols":100,"rows":30}`))
	ws.send(wsOpBinary, []byte("stty size; echo marker-$((40+2))\n"))
	out := ws.readUntil(t, "marker-42")
	if !strings.Contains(out, "30 100") {
		t.Errorf("resize not applied: %q", out)
	}
}

// Keystrokes arrive binary or as plain text; a control message it does not
// know is neither, and a shell that exits ends the stream.
func TestSidecarTerminalInput(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ts := newTestSidecar(t, t.TempDir())
	ws := dialWS(t, strings.TrimPrefix(ts.srv.URL, "http://"), "/term")
	defer ws.c.Close()
	ws.send(wsOpText, []byte(`{"type":"telemetry"}`))
	ws.send(wsOpText, []byte("echo typed-$((1+1))\n"))
	if out := ws.readUntil(t, "typed-2"); strings.Contains(out, "telemetry") {
		t.Errorf("a control message reached the shell: %q", out)
	}
	ws.send(wsOpBinary, []byte("exit\n"))
	_ = ws.c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadAll(ws.br); err != nil {
		t.Errorf("after the shell's exit: %v, want the stream closed", err)
	}
}

// A client that says goodbye gets a close back, and takes the shell with it.
func TestSidecarTerminalClose(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ts := newTestSidecar(t, t.TempDir())
	ws := dialWS(t, strings.TrimPrefix(ts.srv.URL, "http://"), "/term")
	defer ws.c.Close()
	ws.send(wsOpBinary, []byte("echo shell=$$=; echo ready-$((1+1))\n"))
	out := ws.readUntil(t, "ready-2")
	m := regexp.MustCompile(`shell=(\d+)=`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no pid in %q", out)
	}
	pid, _ := strconv.Atoi(m[1])
	ws.send(wsOpClose, []byte{0x03, 0xe8})
	waitGone(t, pid)
}

// With no shell to start, the terminal says why in the stream.
func TestSidecarTerminalUnavailable(t *testing.T) {
	t.Setenv("SHELL", "/no/such/shell")
	ts := newTestSidecar(t, t.TempDir())
	ws := dialWS(t, strings.TrimPrefix(ts.srv.URL, "http://"), "/term")
	defer ws.c.Close()
	ws.readUntil(t, "terminal unavailable")
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
}
