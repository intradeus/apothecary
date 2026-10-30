//go:build live

package browser

// Live test against the host's docker: builds the real browser image (node,
// @playwright/mcp, Chromium — minutes the first time), starts the real
// container, and drives one MCP session that browses an app on the test's
// own loopback through the routing proxy, screenshots it, and is refused the
// outside world. Removes its container; the image stays (content-addressed,
// reused by the daemon).
//
//	go test -tags live -run TestLiveBrowserSession -v -timeout 30m ./internal/browser

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"apothecary/internal/ports/browserport"
	"apothecary/internal/wsimage"
)

func TestLiveBrowserSession(t *testing.T) {
	runs, err := os.MkdirTemp("", "apo-browser-live-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runs)
	_ = os.Chmod(runs, 0o755) // the container's root is not this test's user
	const project = "live-test"
	defer exec.Command("docker", "rm", "-f", containerName(project)).Run()

	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>Live app</title></head><body><h1>Hello from the run</h1></body></html>`)
	}))
	defer app.Close()
	dial := func(ctx context.Context, port int) (net.Conn, error) {
		if port != 3000 {
			return nil, fmt.Errorf("nothing on %d", port)
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", app.Listener.Addr().String())
	}

	m := New(Config{RunsDir: runs, Image: wsimage.New(runs).EnsureBrowserImage, Tag: wsimage.BrowserImageTag,
		Log: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	defer m.Close()

	toServer, fromAgent := io.Pipe()
	fromServer, toAgent := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- m.Session(ctx, browserport.BrowserSession{Project: project, Run: "r1", Dial: dial}, struct {
			io.Reader
			io.Writer
		}{toServer, toAgent})
		toAgent.Close()
	}()

	rd := bufio.NewReader(fromServer)
	id := 0
	call := func(method string, params any) map[string]any {
		t.Helper()
		id++
		line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if _, err := fromAgent.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
		for {
			l, err := rd.ReadBytes('\n')
			if err != nil {
				t.Fatalf("%s: %v (session: %v)", method, err, <-done)
			}
			var m map[string]any
			if json.Unmarshal(l, &m) != nil {
				continue
			}
			if v, ok := m["id"].(float64); ok && int(v) == id {
				return m
			}
		}
	}
	text := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return string(b)
	}

	init := call("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "live", "version": "1"}})
	if _, ok := init["result"]; !ok {
		t.Fatalf("initialize = %s", text(init))
	}
	fromAgent.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))

	nav := call("tools/call", map[string]any{"name": "browser_navigate", "arguments": map[string]any{"url": "http://localhost:3000/"}})
	if !strings.Contains(text(nav), "Page Title: Live app") {
		t.Fatalf("navigate to the run = %s", text(nav))
	}
	// An action's snapshot goes to a file in the container; asking for one
	// returns it inline.
	snap := call("tools/call", map[string]any{"name": "browser_snapshot", "arguments": map[string]any{}})
	if !strings.Contains(text(snap), "Hello from the run") {
		t.Fatalf("snapshot = %s", text(snap))
	}
	shot := call("tools/call", map[string]any{"name": "browser_take_screenshot", "arguments": map[string]any{}})
	if !strings.Contains(text(shot), `"type":"image"`) {
		t.Fatalf("screenshot = %.500s", text(shot))
	}
	out := call("tools/call", map[string]any{"name": "browser_navigate", "arguments": map[string]any{"url": "https://example.com/"}})
	if strings.Contains(text(out), "Example Domain") {
		t.Fatalf("the browser reached the outside world: %.500s", text(out))
	}
	t.Logf("outside world: %.300s", text(out))

	fromAgent.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the session outlived its stdin")
	}
	if out, _ := exec.Command("docker", "exec", containerName(project), "sh", "-c",
		`for p in /proc/[0-9]*; do tr '\0' ' ' <$p/cmdline 2>/dev/null; echo; done | grep -c "[c]hrom" || true`).Output(); strings.TrimSpace(string(out)) != "0" {
		t.Errorf("chromium outlived its session: %s", out)
	}
}
