package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A named screenshot is taken off the request (Playwright would write it in
// the browser container), its image is written in the workspace when the
// response comes back, and the agent reads Playwright's answer with the
// link pointing at its own file.
func TestNamedScreenshotIsSavedInTheWorkspace(t *testing.T) {
	cwd := t.TempDir()
	var agent bytes.Buffer
	b := newBrowserBridge(cwd, &agent)

	req := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"browser_take_screenshot","arguments":{"filename":"artifacts/shots/home.jpg","fullPage":true}}}` + "\n"
	var engine bytes.Buffer
	if err := b.fromAgent(bufio.NewReader(strings.NewReader(req)), &engine); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Params struct {
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(engine.Bytes(), &sent); err != nil {
		t.Fatal(err)
	}
	if _, ok := sent.Params.Arguments["filename"]; ok || sent.Params.Arguments["type"] != "jpeg" || sent.Params.Arguments["fullPage"] != true {
		t.Fatalf("forwarded arguments = %v", sent.Params.Arguments)
	}

	img := []byte("\xff\xd8 not really a jpeg")
	resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "result": map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "### Result\n- [Screenshot of full page](.playwright-mcp/page-1.jpeg)\n"},
		map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(img), "mimeType": "image/jpeg"},
	}}})
	if err := b.fromEngine(bufio.NewReader(bytes.NewReader(append(resp, '\n')))); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(cwd, "artifacts/shots/home.jpg"))
	if err != nil || !bytes.Equal(got, img) {
		t.Fatalf("saved = %q, %v", got, err)
	}
	var out struct {
		ID     int `json:"id"`
		Result struct {
			Content []map[string]any `json:"content"`
			IsError bool             `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(agent.Bytes(), &out); err != nil {
		t.Fatalf("agent got %q: %v", agent.String(), err)
	}
	if out.ID != 7 || out.Result.IsError || len(out.Result.Content) != 1 ||
		!strings.Contains(out.Result.Content[0]["text"].(string), "(artifacts/shots/home.jpg)") {
		t.Errorf("agent got %s", agent.String())
	}
	if b.failed() {
		t.Error("a clean session counts as failed")
	}
}

// Anything else passes through untouched, both ways.
func TestBrowserBridgePassesOtherMessagesThrough(t *testing.T) {
	var agent, engine bytes.Buffer
	b := newBrowserBridge(t.TempDir(), &agent)
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_navigate","arguments":{"url":"http://localhost:3000"}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	if err := b.fromAgent(bufio.NewReader(strings.NewReader(in)), &engine); err != nil {
		t.Fatal(err)
	}
	if engine.String() != in {
		t.Errorf("forwarded %q", engine.String())
	}
	shot := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"image","data":"aGk=","mimeType":"image/png"}]}}` + "\n"
	if err := b.fromEngine(bufio.NewReader(strings.NewReader(shot))); err != nil {
		t.Fatal(err)
	}
	if agent.String() != shot {
		t.Errorf("an unnamed screenshot was rewritten: %q", agent.String())
	}
}

// When the engine cannot serve the session, every request still waiting is
// answered with why, instead of hanging until the harness gives up.
func TestEngineErrorAnswersWaitingRequests(t *testing.T) {
	var agent, engine bytes.Buffer
	b := newBrowserBridge(t.TempDir(), &agent)
	req := `{"jsonrpc":"2.0","id":"init-1","method":"initialize","params":{}}` + "\n"
	if err := b.fromAgent(bufio.NewReader(strings.NewReader(req)), &engine); err != nil {
		t.Fatal(err)
	}
	if err := b.fromEngine(bufio.NewReader(strings.NewReader(`{"apothecary_error":"the project's browser is off"}` + "\n"))); err == nil {
		t.Fatal("an engine error was not an error")
	}
	var out struct {
		ID    string `json:"id"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(agent.Bytes(), &out); err != nil || out.ID != "init-1" ||
		!strings.Contains(out.Error.Message, "the project's browser is off") {
		t.Fatalf("agent got %q (%v)", agent.String(), err)
	}
	if !b.failed() {
		t.Error("a refused session does not count as failed")
	}
}

// browser-session runs the server on its stdio with --proxy-server at a
// loopback relay to the session socket.
func TestBrowserSessionRelaysTheProxyPort(t *testing.T) {
	dir, err := os.MkdirTemp("", "bs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.WriteString(c, "proxy here")
	}()

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	code := make(chan int, 1)
	// The "server" prints its arguments and waits for stdin to close.
	go func() {
		code <- browserSession(sock, []string{"sh", "-c", `echo "$@"; cat >/dev/null`, "sh"}, stdinR, stdoutW, io.Discard)
		stdoutW.Close()
	}()
	line, err := bufio.NewReader(stdoutR).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(line)
	if len(f) != 4 || f[0] != "--proxy-server" || !strings.HasPrefix(f[1], "http://127.0.0.1:") || f[2] != "--output-dir" {
		t.Fatalf("server argv = %q", line)
	}
	c, err := net.DialTimeout("tcp", strings.TrimPrefix(f[1], "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(c)
	c.Close()
	if string(got) != "proxy here" {
		t.Errorf("relay gave %q", got)
	}
	stdinW.Close()
	select {
	case c := <-code:
		if c != 0 {
			t.Errorf("exit = %d", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session outlived its stdin")
	}
}
