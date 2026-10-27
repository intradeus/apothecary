package main

// The project browser's two ends (internal/browser):
//
//	browser-mcp      in the WORKSPACE: the stdio MCP server a flow's builtin
//	                 `browser` names. It upgrades a connection to the engine
//	                 (GET /api/runs/{run}/browser-mcp, the run's bearer token)
//	                 and pipes the agent's stdio through it; the engine pipes
//	                 it on to a Chromium in the project's browser container.
//	browser-session  in the BROWSER CONTAINER: one MCP session. It listens on
//	                 an ephemeral loopback port, relays it to the session's
//	                 routing-proxy socket, and runs @playwright/mcp with that
//	                 port as Chromium's proxy, on its own stdio.
//
// Screenshots are the browser's point (they go in the merge request), but
// Playwright writes a named screenshot where IT runs — in the browser
// container, which the agent cannot read. So browser-mcp takes a named
// screenshot's file name off the request, lets the image come back in the
// response instead, and writes it in the workspace, relative to the agent's
// working directory, as the tool says it does.

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// browserMCPProtocol is the Upgrade token of the engine's browser stream.
const browserMCPProtocol = "apothecary-mcp"

// browserErrorKey marks the engine's last line on a stream it could not
// serve: {"apothecary_error": "..."} — not JSON-RPC, so never forwarded.
const browserErrorKey = "apothecary_error"

func runBrowserMCP() {
	conn, br, err := dialBrowser()
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser-mcp:", err)
		os.Exit(1)
	}
	defer conn.Close()
	cwd, _ := os.Getwd()
	b := newBrowserBridge(cwd, os.Stdout)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := b.fromEngine(br); err != nil {
			fmt.Fprintln(os.Stderr, "browser-mcp:", err)
		}
	}()
	go func() {
		_ = b.fromAgent(bufio.NewReader(os.Stdin), conn)
		halfClose(conn)
	}()
	<-done
	if b.failed() {
		os.Exit(1)
	}
}

// dialBrowser upgrades a connection to the engine's browser stream. A 401
// re-reads run.env once: the engine rotates run tokens when it restarts.
func dialBrowser() (net.Conn, *bufio.Reader, error) {
	for attempt := 0; ; attempt++ {
		env := readRunEnv()
		u, err := url.Parse(env["APOTHECARY_ENGINE_URL"])
		if err != nil || u.Host == "" || env["APOTHECARY_RUN_ID"] == "" {
			return nil, nil, fmt.Errorf("no engine to reach (%s has no APOTHECARY_ENGINE_URL / APOTHECARY_RUN_ID)", runEnvPath)
		}
		conn, err := net.DialTimeout("tcp", u.Host, 10*time.Second)
		if err != nil {
			return nil, nil, fmt.Errorf("reach the engine: %w", err)
		}
		req := "GET /api/runs/" + url.PathEscape(env["APOTHECARY_RUN_ID"]) + "/browser-mcp HTTP/1.1\r\n" +
			"Host: " + u.Host + "\r\n" +
			"Authorization: Bearer " + env["APOTHECARY_RUN_TOKEN"] + "\r\n" +
			"Connection: Upgrade\r\nUpgrade: " + browserMCPProtocol + "\r\n\r\n"
		if _, err := io.WriteString(conn, req); err != nil {
			conn.Close()
			return nil, nil, err
		}
		br := bufio.NewReader(conn)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("engine: %w", err)
		}
		if resp.StatusCode == http.StatusSwitchingProtocols {
			return conn, br, nil
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		conn.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(body))
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return nil, nil, fmt.Errorf("the engine refused the browser (%s): %s", resp.Status, msg)
	}
}

// browserBridge pipes MCP between the agent (stdio) and the engine, keeping
// what a named screenshot needs.
type browserBridge struct {
	cwd string
	out io.Writer // the agent's stdin side: JSON-RPC lines

	mu      sync.Mutex
	wmu     sync.Mutex
	pending map[string]bool   // request ids awaiting a response
	shots   map[string]string // request id -> the file a screenshot goes to
	broken  bool
}

func newBrowserBridge(cwd string, out io.Writer) *browserBridge {
	return &browserBridge{cwd: cwd, out: out, pending: map[string]bool{}, shots: map[string]string{}}
}

func (b *browserBridge) failed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.broken
}

func (b *browserBridge) write(line []byte) error {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	if _, err := b.out.Write(line); err != nil {
		return err
	}
	if len(line) == 0 || line[len(line)-1] != '\n' {
		_, err := b.out.Write([]byte{'\n'})
		return err
	}
	return nil
}

type bridgeMsg struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

// fromAgent forwards the agent's requests, rewriting named screenshots.
func (b *browserBridge) fromAgent(r *bufio.Reader, engine io.Writer) error {
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			out := b.request(line)
			if _, werr := engine.Write(out); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// request notes a request's id and takes a screenshot's file name off it.
func (b *browserBridge) request(line []byte) []byte {
	var m bridgeMsg
	if json.Unmarshal(line, &m) != nil || len(m.ID) == 0 || m.Method == "" {
		return line
	}
	id := string(m.ID)
	b.mu.Lock()
	b.pending[id] = true
	b.mu.Unlock()
	if m.Method != "tools/call" {
		return line
	}
	var p map[string]json.RawMessage
	if json.Unmarshal(m.Params, &p) != nil {
		return line
	}
	var name string
	_ = json.Unmarshal(p["name"], &name)
	if name != "browser_take_screenshot" {
		return line
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(p["arguments"], &args) != nil {
		return line
	}
	var file string
	if json.Unmarshal(args["filename"], &file) != nil || strings.TrimSpace(file) == "" {
		return line
	}
	delete(args, "filename")
	// The type the file name would have implied, now that it is gone.
	if _, set := args["type"]; !set {
		switch strings.ToLower(filepath.Ext(file)) {
		case ".jpg", ".jpeg":
			args["type"] = json.RawMessage(`"jpeg"`)
		case ".webp":
			args["type"] = json.RawMessage(`"webp"`)
		}
	}
	a, _ := json.Marshal(args)
	p["arguments"] = a
	params, _ := json.Marshal(p)
	full := map[string]json.RawMessage{}
	if json.Unmarshal(line, &full) != nil {
		return line
	}
	full["params"] = params
	out, err := json.Marshal(full)
	if err != nil {
		return line
	}
	b.mu.Lock()
	b.shots[id] = file
	b.mu.Unlock()
	return append(out, '\n')
}

// fromEngine forwards the server's messages, saving screenshots on the way.
func (b *browserBridge) fromEngine(r *bufio.Reader) error {
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if msg, isErr := engineError(line); isErr {
				b.abort(msg)
				return errors.New(msg)
			}
			if werr := b.write(b.response(line)); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				b.abort("the project browser closed the session")
				return nil
			}
			b.abort("the project browser's stream broke: " + err.Error())
			return err
		}
	}
}

func engineError(line []byte) (string, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(line, &m) != nil {
		return "", false
	}
	raw, ok := m[browserErrorKey]
	if !ok {
		return "", false
	}
	var msg string
	_ = json.Unmarshal(raw, &msg)
	return msg, true
}

// abort answers every request still waiting, so the agent hears why rather
// than hanging until its harness times the call out.
func (b *browserBridge) abort(msg string) {
	b.mu.Lock()
	ids := make([]string, 0, len(b.pending))
	for id := range b.pending {
		ids = append(ids, id)
	}
	b.pending = map[string]bool{}
	if len(ids) > 0 {
		b.broken = true
	}
	b.mu.Unlock()
	for _, id := range ids {
		e, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id),
			"error": map[string]any{"code": -32603, "message": "apothecary browser: " + msg}})
		_ = b.write(e)
	}
}

// shotLinkRe is Playwright's link to the file it wrote.
var shotLinkRe = regexp.MustCompile(`(- \[Screenshot[^\]]*\]\()[^)]*(\))`)

// response clears a request's id and, for a named screenshot, writes the
// image where the agent asked and answers as if Playwright had.
func (b *browserBridge) response(line []byte) []byte {
	var m bridgeMsg
	if json.Unmarshal(line, &m) != nil || len(m.ID) == 0 || m.Method != "" {
		return line
	}
	id := string(m.ID)
	b.mu.Lock()
	delete(b.pending, id)
	file, isShot := b.shots[id]
	delete(b.shots, id)
	b.mu.Unlock()
	if !isShot || len(m.Result) == 0 {
		return line
	}
	var res map[string]json.RawMessage
	if json.Unmarshal(m.Result, &res) != nil {
		return line
	}
	var content []map[string]any
	if json.Unmarshal(res["content"], &content) != nil {
		return line
	}
	var img []byte
	var kept []map[string]any
	for _, c := range content {
		if c["type"] == "image" && img == nil {
			data, _ := c["data"].(string)
			if d, err := base64.StdEncoding.DecodeString(data); err == nil {
				img = d
				continue
			}
		}
		kept = append(kept, c)
	}
	if img == nil {
		return line // an error, or nothing to save: as the server said it
	}
	note := ""
	if err := b.save(file, img); err != nil {
		res["isError"] = json.RawMessage("true")
		note = fmt.Sprintf("Could not save the screenshot to %s in the workspace: %v", file, err)
	}
	linked := false
	for _, c := range kept {
		if t, ok := c["text"].(string); ok && c["type"] == "text" && shotLinkRe.MatchString(t) {
			c["text"] = shotLinkRe.ReplaceAllString(t, "${1}"+strings.ReplaceAll(file, "$", "$$")+"${2}")
			linked = true
		}
	}
	if note == "" && !linked {
		note = "Saved the screenshot to " + file + " in the workspace."
	}
	if note != "" {
		kept = append(kept, map[string]any{"type": "text", "text": note})
	}
	c, _ := json.Marshal(kept)
	res["content"] = c
	r, _ := json.Marshal(res)
	full := map[string]json.RawMessage{}
	if json.Unmarshal(line, &full) != nil {
		return line
	}
	full["result"] = r
	out, err := json.Marshal(full)
	if err != nil {
		return line
	}
	return append(out, '\n')
}

func (b *browserBridge) save(file string, data []byte) error {
	p := file
	if !filepath.IsAbs(p) {
		p = filepath.Join(b.cwd, p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// --- in the browser container ---------------------------------------------------

func runBrowserSession(args []string) {
	sock, engine, token, argv := "", "", "", []string(nil)
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			argv = args[i+1:]
			break
		}
		if (args[i] == "--engine" || args[i] == "--token") && i+1 < len(args) {
			if args[i] == "--engine" {
				engine = args[i+1]
			} else {
				token = args[i+1]
			}
			i++
			continue
		}
		if args[i] == "--sock" && i+1 < len(args) {
			i++
			sock = args[i]
			continue
		}
		fmt.Fprintln(os.Stderr, "browser-session: unknown argument", args[i])
		os.Exit(2)
	}
	if (sock == "") == (engine == "") || len(argv) == 0 || (engine != "" && token == "") {
		fmt.Fprintln(os.Stderr, "usage: shim browser-session --sock <proxy socket> | --engine <host:port> --token <t> -- <mcp server argv>")
		os.Exit(2)
	}
	// The routing proxy is the engine's: a unix socket beside this container
	// (docker), or a TCP listener the session's token opens (Kubernetes).
	dial := func() (net.Conn, error) { return net.Dial("unix", sock) }
	if engine != "" {
		dial = func() (net.Conn, error) {
			c, err := net.Dial("tcp", engine)
			if err != nil {
				return nil, err
			}
			if err := writePreamble(c, token, "proxy"); err != nil {
				c.Close()
				return nil, err
			}
			return c, nil
		}
	}
	os.Exit(browserSession(dial, argv, os.Stdin, os.Stdout, os.Stderr))
}

// browserSession runs one MCP server (argv) on the given stdio with its
// browser proxied, through a fresh loopback relay, to the engine's routing
// proxy (dial). It returns the server's exit code.
func browserSession(dial func() (net.Conn, error), argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(stderr, "browser-session: listen:", err)
		return 1
	}
	defer ln.Close()
	go serve(ln, dial)
	port := ln.Addr().(*net.TCPAddr).Port

	dir, err := os.MkdirTemp("", "apo-browser-*")
	if err != nil {
		fmt.Fprintln(stderr, "browser-session:", err)
		return 1
	}
	defer os.RemoveAll(dir)
	argv = append(slices.Clone(argv), "--proxy-server", "http://127.0.0.1:"+strconv.Itoa(port), "--output-dir", dir)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	// Its own group: Chromium and its helpers go with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(stderr, "browser-session: start:", err)
		return 1
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigs)
	go func() {
		if _, ok := <-sigs; ok {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
	}()
	err = cmd.Wait()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() >= 0 {
			return ee.ExitCode()
		}
		return 1
	}
	return 0
}
