//go:build linux

package sidecar

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type unixDialer string

func (u unixDialer) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", string(u))
}

// The browser's terminal path end to end: a WebSocket to the engine, proxied
// by TerminalProxy over the unix socket into the real sidecar's PTY.
func TestTerminalThroughTheProxy(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "shim")
	if out, err := exec.Command("go", "build", "-o", shim, "apothecary/workspace/shim").CombinedOutput(); err != nil {
		t.Fatalf("build shim: %v\n%s", err, out)
	}
	sock := filepath.Join(dir, "s.sock")
	cmd := exec.Command(shim, "sidecar", "--listen", sock, "--repo", dir)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; ; i++ {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			break
		}
		if i > 500 {
			t.Fatal("sidecar never served")
		}
		time.Sleep(10 * time.Millisecond)
	}

	srv := httptest.NewServer(New(unixDialer(sock)).TerminalProxy())
	defer srv.Close()

	c, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	_, _ = c.Write([]byte("GET /api/runs/r/terminal?cols=80&rows=24 HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\n" +
		"Connection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " + key + "\r\n\r\n"))
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, nil)
	if err != nil || res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade through the proxy: %v %v", res, err)
	}
	// One masked binary frame of keystrokes.
	msg := []byte("echo through-$((6*7))\n")
	frame := []byte{0x82, 0x80 | byte(len(msg)), 1, 2, 3, 4}
	for i, b := range msg {
		frame = append(frame, b^[]byte{1, 2, 3, 4}[i%4])
	}
	_, _ = c.Write(frame)
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var got strings.Builder
	for !strings.Contains(got.String(), "through-42") {
		var h [2]byte
		if _, err := io.ReadFull(br, h[:]); err != nil {
			t.Fatalf("read: %v (so far %q)", err, got.String())
		}
		n := int(h[1] & 0x7f)
		if n == 126 {
			var b [2]byte
			_, _ = io.ReadFull(br, b[:])
			n = int(b[0])<<8 | int(b[1])
		}
		data := make([]byte, n)
		_, _ = io.ReadFull(br, data)
		got.Write(data)
	}
}
