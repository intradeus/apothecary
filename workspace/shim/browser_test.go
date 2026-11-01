package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"
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
		code <- browserSession(func() (net.Conn, error) { return net.Dial("unix", sock) }, []string{"sh", "-c", `echo "$@"; cat >/dev/null`, "sh"}, stdinR, stdoutW, io.Discard)
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

const browserUpgraded = "HTTP/1.1 101 Switching Protocols\r\nUpgrade: apothecary-mcp\r\nConnection: Upgrade\r\n\r\n"

// browserEngine serves the browser stream: each connection gets the next
// handler, which reads the request and answers on the raw connection.
func browserEngine(t *testing.T, handlers ...func(*http.Request, net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for _, h := range handlers {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil {
					return
				}
				h(r, c)
			}()
		}
	}()
	return "http://" + ln.Addr().String()
}

func replyWith(raw string) func(*http.Request, net.Conn) {
	return func(_ *http.Request, c net.Conn) { _, _ = io.WriteString(c, raw) }
}

// The upgrade names the run and carries its token; a 401 means the engine
// restarted, and the fresh token in run.env gets the stream, with the first
// bytes that came behind the 101 kept.
func TestDialBrowserRetriesARotatedToken(t *testing.T) {
	fastProtocol(t)
	seen := make(chan *http.Request, 2)
	url := browserEngine(t,
		func(r *http.Request, c net.Conn) {
			writeTestEnv(t, "http://"+r.Host, "fresh")
			_, _ = io.WriteString(c, "HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n")
			seen <- r
		},
		func(r *http.Request, c net.Conn) {
			_, _ = io.WriteString(c, browserUpgraded+"{\"early\":true}\n")
			seen <- r
		})
	writeTestEnv(t, url, "stale")
	conn, br, err := dialBrowser()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if line, err := br.ReadString('\n'); err != nil || line != "{\"early\":true}\n" {
		t.Errorf("first line = %q, %v", line, err)
	}
	first, second := <-seen, <-seen
	if first.Header.Get("Authorization") != "Bearer stale" || second.Header.Get("Authorization") != "Bearer fresh" {
		t.Errorf("tokens = %q then %q", first.Header.Get("Authorization"), second.Header.Get("Authorization"))
	}
	if second.URL.Path != "/api/runs/run1/browser-mcp" || second.Header.Get("Upgrade") != browserMCPProtocol {
		t.Errorf("upgrade = %s %v", second.URL.Path, second.Header)
	}
}

func TestDialBrowserRefused(t *testing.T) {
	fastProtocol(t)
	unauthorized := replyWith("HTTP/1.1 401 Unauthorized\r\nContent-Length: 4\r\n\r\nnope")
	for _, c := range []struct {
		name     string
		handlers []func(*http.Request, net.Conn)
		want     string
	}{
		{"json", []func(*http.Request, net.Conn){replyWith("HTTP/1.1 403 Forbidden\r\nContent-Length: 29\r\n\r\n{\"error\":\"the browser's off\"}")},
			"the engine refused the browser (403 Forbidden): the browser's off"},
		{"text", []func(*http.Request, net.Conn){replyWith("HTTP/1.1 500 Oops\r\nContent-Length: 4\r\n\r\nboom")},
			"(500 Oops): boom"},
		{"401 twice", []func(*http.Request, net.Conn){unauthorized, unauthorized}, "(401 Unauthorized): nope"},
		{"garbage", []func(*http.Request, net.Conn){replyWith("not http\r\n\r\n")}, "engine: "},
	} {
		writeTestEnv(t, browserEngine(t, c.handlers...), "t")
		if _, _, err := dialBrowser(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	writeTestEnv(t, "http://127.0.0.1:"+strconv.Itoa(freePort(t)), "t")
	if _, _, err := dialBrowser(); err == nil || !strings.Contains(err.Error(), "reach the engine") {
		t.Errorf("dead engine: %v", err)
	}
	if err := os.WriteFile(runEnvPath, []byte("APOTHECARY_ENGINE_URL=http://x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dialBrowser(); err == nil || !strings.Contains(err.Error(), "no engine to reach") {
		t.Errorf("no run id: %v", err)
	}
}

func browserRequest(t *testing.T, b *browserBridge, line string) string {
	t.Helper()
	var engine bytes.Buffer
	if err := b.fromAgent(bufio.NewReader(strings.NewReader(line+"\n")), &engine); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(engine.String(), "\n")
}

func shotRequest(args string) string {
	return `{"id":1,"method":"tools/call","params":{"name":"browser_take_screenshot","arguments":` + args + `}}`
}

// Only a named screenshot is rewritten; everything else, however odd, goes
// through as the agent wrote it.
func TestBrowserRequestsLeftAlone(t *testing.T) {
	b := newBrowserBridge(t.TempDir(), io.Discard)
	for _, line := range []string{
		`not json`,
		`{"id":1,"method":"tools/call","params":[1]}`,
		`{"id":1,"method":"tools/call","params":{"name":"browser_click","arguments":{"filename":"a.png"}}}`,
		shotRequest(`[1]`),
		shotRequest(`{"filename":5}`),
		shotRequest(`{"filename":"  "}`),
		shotRequest(`{}`),
	} {
		if got := browserRequest(t, b, line); got != line {
			t.Errorf("%s rewritten to %s", line, got)
		}
	}
}

// The type a file name implied survives its removal, and an explicit one wins.
func TestBrowserShotType(t *testing.T) {
	for _, c := range []struct{ args, want string }{
		{`{"filename":"a.jpeg"}`, "jpeg"},
		{`{"filename":"a.WEBP"}`, "webp"},
		{`{"filename":"a.png"}`, ""},
		{`{"filename":"a.jpg","type":"png"}`, "png"},
	} {
		b := newBrowserBridge(t.TempDir(), io.Discard)
		var sent struct {
			Params struct {
				Arguments map[string]string `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(browserRequest(t, b, shotRequest(c.args))), &sent); err != nil {
			t.Fatal(err)
		}
		if _, named := sent.Params.Arguments["filename"]; named || sent.Params.Arguments["type"] != c.want {
			t.Errorf("%s forwarded as %v", c.args, sent.Params.Arguments)
		}
	}
}

// What a server prints that is not JSON-RPC reaches the agent as it is.
func TestBrowserResponseThatIsNotJSON(t *testing.T) {
	var agent bytes.Buffer
	b := newBrowserBridge(t.TempDir(), &agent)
	if err := b.fromEngine(bufio.NewReader(strings.NewReader("a log line\n"))); err != nil || agent.String() != "a log line\n" {
		t.Errorf("agent got %q, %v", agent.String(), err)
	}
}

// The agent's stream can end anywhere: a last line with no newline is still
// forwarded, a blank one is not, and a failure either way is reported.
func TestBrowserFromAgentStreamEnds(t *testing.T) {
	b := newBrowserBridge(t.TempDir(), io.Discard)
	var engine bytes.Buffer
	if err := b.fromAgent(bufio.NewReader(strings.NewReader("\n{\"id\":1}")), &engine); err != nil || engine.String() != `{"id":1}` {
		t.Errorf("forwarded %q, %v", engine.String(), err)
	}
	broken := errors.New("broken")
	if err := b.fromAgent(bufio.NewReader(iotest.ErrReader(broken)), &engine); !errors.Is(err, broken) {
		t.Errorf("read failure = %v", err)
	}
	if err := b.fromAgent(bufio.NewReader(strings.NewReader("x\n")), brokenWriter{}); err == nil {
		t.Error("a failed write to the engine went unreported")
	}
}

// failNthWriter fails its nth write.
type failNthWriter struct{ n int }

func (w *failNthWriter) Write(p []byte) (int, error) {
	if w.n--; w.n == 0 {
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

// A server line with no newline gets one (the agent reads lines), and when
// the agent's side is gone the bridge stops.
func TestBrowserFromEngineWrites(t *testing.T) {
	var agent bytes.Buffer
	b := newBrowserBridge(t.TempDir(), &agent)
	if err := b.fromEngine(bufio.NewReader(strings.NewReader(`{"id":1,"result":{}}`))); err != nil {
		t.Fatal(err)
	}
	if agent.String() != `{"id":1,"result":{}}`+"\n" {
		t.Errorf("agent got %q", agent.String())
	}
	for n := 1; n <= 2; n++ { // the line, then its newline
		b := newBrowserBridge(t.TempDir(), &failNthWriter{n: n})
		if err := b.fromEngine(bufio.NewReader(strings.NewReader(`{"id":1}`))); err == nil {
			t.Errorf("failure of write %d went unreported", n)
		}
	}
}

// However the stream ends, what is still waiting hears why.
func TestBrowserStreamEndAnswersWaitingRequests(t *testing.T) {
	broken := errors.New("reset by peer")
	for _, c := range []struct {
		name string
		r    io.Reader
		want string
		err  bool
	}{
		{"closed", strings.NewReader(""), "the project browser closed the session", false},
		{"broken", iotest.ErrReader(broken), "the project browser's stream broke: reset by peer", true},
	} {
		var agent bytes.Buffer
		b := newBrowserBridge(t.TempDir(), &agent)
		browserRequest(t, b, `{"id":7,"method":"tools/list"}`)
		if err := b.fromEngine(bufio.NewReader(c.r)); (err != nil) != c.err {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if !strings.Contains(agent.String(), c.want) || !strings.Contains(agent.String(), `"id":7`) || !b.failed() {
			t.Errorf("%s: agent got %q, failed %v", c.name, agent.String(), b.failed())
		}
	}
	// With nothing waiting, a closed session is just over.
	var agent bytes.Buffer
	b := newBrowserBridge(t.TempDir(), &agent)
	if err := b.fromEngine(bufio.NewReader(strings.NewReader(""))); err != nil || agent.Len() != 0 || b.failed() {
		t.Errorf("clean end: %v, agent %q, failed %v", err, agent.String(), b.failed())
	}
}

func TestEngineError(t *testing.T) {
	for line, want := range map[string]bool{
		`{"apothecary_error":"off"}`: true,
		`{"apothecary_error":5}`:     true,
		`{"id":1}`:                   false,
		`[1]`:                        false,
		`not json`:                   false,
	} {
		if _, got := engineError([]byte(line)); got != want {
			t.Errorf("%s = %v", line, got)
		}
	}
}

// shotResponse sends a screenshot request, then the given result for it, and
// returns what the agent read.
func shotResponse(t *testing.T, cwd, file, result string) string {
	t.Helper()
	var agent bytes.Buffer
	b := newBrowserBridge(cwd, &agent)
	args, _ := json.Marshal(map[string]string{"filename": file})
	browserRequest(t, b, shotRequest(string(args)))
	line := `{"jsonrpc":"2.0","id":1,"result":` + result + "}\n"
	if err := b.fromEngine(bufio.NewReader(strings.NewReader(line))); err != nil {
		t.Fatal(err)
	}
	return agent.String()
}

const pngResult = `{"content":[{"type":"image","data":"aGk=","mimeType":"image/png"}]}`

// With no link to rewrite the agent is told where the file went, and a file
// name is taken literally however it looks.
func TestBrowserShotSavedWithoutALink(t *testing.T) {
	cwd := t.TempDir()
	if got := shotResponse(t, cwd, "a.png", pngResult); !strings.Contains(got, "Saved the screenshot to a.png in the workspace.") {
		t.Errorf("agent got %s", got)
	}
	if data, err := os.ReadFile(filepath.Join(cwd, "a.png")); err != nil || string(data) != "hi" {
		t.Errorf("file = %q, %v", data, err)
	}
	abs := filepath.Join(t.TempDir(), "sub", "b.png")
	shotResponse(t, cwd, abs, pngResult)
	if data, err := os.ReadFile(abs); err != nil || string(data) != "hi" {
		t.Errorf("absolute path: %q, %v", data, err)
	}
	linked := `{"content":[{"type":"text","text":"- [Screenshot](x.png)"},{"type":"image","data":"aGk="}]}`
	if got := shotResponse(t, cwd, "$1.png", linked); !strings.Contains(got, "($1.png)") {
		t.Errorf("a $ in the name was expanded: %s", got)
	}
}

// A file that cannot be written is the tool's error, in the agent's hands.
func TestBrowserShotSaveFails(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	got := shotResponse(t, cwd, "file/a.png", pngResult)
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Result.IsError || len(out.Result.Content) != 1 || !strings.Contains(out.Result.Content[0].Text, "Could not save the screenshot to file/a.png") {
		t.Errorf("agent got %s", got)
	}
}

// A screenshot answer with nothing to save comes back as the server said it.
func TestBrowserShotResponsesLeftAlone(t *testing.T) {
	for _, result := range []string{
		`[1]`,
		`{"content":"no"}`,
		`{"content":[{"type":"text","text":"navigation failed"}]}`,
		`{"content":[{"type":"image","data":"!!not base64"}]}`,
	} {
		want := `{"jsonrpc":"2.0","id":1,"result":` + result + "}\n"
		if got := shotResponse(t, t.TempDir(), "a.png", result); got != want {
			t.Errorf("%s came back as %s", result, got)
		}
	}
	// A refused screenshot (no result at all) too, and nothing is left behind.
	var agent bytes.Buffer
	b := newBrowserBridge(t.TempDir(), &agent)
	browserRequest(t, b, shotRequest(`{"filename":"a.png"}`))
	refused := `{"jsonrpc":"2.0","id":1,"error":{"code":1,"message":"no page"}}` + "\n"
	if err := b.fromEngine(bufio.NewReader(strings.NewReader(refused))); err != nil || agent.String() != refused {
		t.Errorf("agent got %q, %v", agent.String(), err)
	}
	if len(b.shots) != 0 || len(b.pending) != 0 {
		t.Errorf("left behind: %v %v", b.shots, b.pending)
	}
}

func TestBrowserSessionExits(t *testing.T) {
	dial := func() (net.Conn, error) { return nil, errors.New("unused") }
	var stderr bytes.Buffer
	if code := browserSession(dial, []string{"/no/such/server"}, strings.NewReader(""), io.Discard, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "browser-session: start:") {
		t.Errorf("no server = %d %q", code, stderr.String())
	}
	if code := browserSession(dial, []string{"sh", "-c", "exit 5"}, strings.NewReader(""), io.Discard, io.Discard); code != 5 {
		t.Errorf("its exit code = %d", code)
	}
	if code := browserSession(dial, []string{"sh", "-c", "kill -KILL $$"}, strings.NewReader(""), io.Discard, io.Discard); code != 1 {
		t.Errorf("killed = %d", code)
	}
}

func TestRunBrowserSessionRefusesBadArguments(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"--nope"},
		{"--sock"},
		{"--sock", "s"}, // no argv
		{"--", "server"},
		{"--sock", "s", "--engine", "e:1", "--token", "t", "--", "server"},
		{"--engine", "e:1", "--", "server"},
	} {
		if r := shim(t, "", append([]string{"browser-session"}, args...)...); r.code != 2 {
			t.Errorf("browser-session %v = %+v", args, r)
		}
	}
}

// Kubernetes: Chromium's proxy traffic goes to the engine's TCP listener, each
// connection opened with the session's token. The "server" here connects to
// the proxy port it was given, then waits for its stdin to end.
func TestRunBrowserSessionDialsTheEngine(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := shimCmd(t, "browser-session", "--engine", ln.Addr().String(), "--token", "tok", "--",
		"sh", "-c", `"$0" probe "${2#http://}"; cat >/dev/null`, exe)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || line != "APO1 tok proxy\n" {
		t.Errorf("preamble = %q, %v", line, err)
	}
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Errorf("exit = %v", err)
	}
}

// Docker: the same, through the unix socket beside the container.
func TestRunBrowserSessionDialsTheSocket(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "proxy.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := shimCmd(t, "browser-session", "--sock", sock, "--",
		"sh", "-c", `"$0" probe "${2#http://}"; cat >/dev/null`, exe)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Errorf("exit = %v", err)
	}
}

// Stopping the session stops the server and whatever it spawned.
func TestRunBrowserSessionStopsItsServer(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "proxy.sock")
	cmd := shimCmd(t, "browser-session", "--sock", sock, "--", "sh", "-c", `echo $$; exec sleep 60`)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil || cmd.ProcessState.ExitCode() != 1 {
		t.Errorf("exit = %v", err)
	}
	waitGone(t, pid)
}

// browser-mcp as a harness runs it: a named screenshot goes through the
// engine and lands in the agent's working directory.
func TestRunBrowserMCP(t *testing.T) {
	cwd := t.TempDir()
	url := browserEngine(t, func(r *http.Request, c net.Conn) {
		_, _ = io.WriteString(c, browserUpgraded)
		var req struct {
			ID     int `json:"id"`
			Params struct {
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		line, err := bufio.NewReader(c).ReadBytes('\n')
		if err != nil || json.Unmarshal(line, &req) != nil || req.ID != 1 || req.Params.Arguments["filename"] != nil {
			t.Errorf("engine got %q, %v", line, err)
		}
		_, _ = io.WriteString(c, `{"jsonrpc":"2.0","id":1,"result":`+pngResult+"}\n")
		// The session ends here, with nothing waiting: a clean exit.
	})
	env := filepath.Join(t.TempDir(), "run.env")
	if err := os.WriteFile(env, []byte("APOTHECARY_RUN_ID=run1\nAPOTHECARY_ENGINE_URL="+url+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := shimCmd(t, "browser-mcp")
	cmd.Dir = cwd
	cmd.Env = append(cmd.Env, shimRunEnv+"="+env)
	r := runShim(t, shotRequest(`{"filename":"home.png"}`)+"\n", cmd)
	if r.code != 0 || !strings.Contains(r.stdout, "Saved the screenshot to home.png") {
		t.Errorf("browser-mcp = %+v", r)
	}
	if data, err := os.ReadFile(filepath.Join(cwd, "home.png")); err != nil || string(data) != "hi" {
		t.Errorf("screenshot = %q, %v", data, err)
	}
}

// The engine refusing mid-session fails the process, once the agent has heard.
func TestRunBrowserMCPFails(t *testing.T) {
	url := browserEngine(t, func(_ *http.Request, c net.Conn) {
		_, _ = io.WriteString(c, browserUpgraded)
		if _, err := bufio.NewReader(c).ReadBytes('\n'); err != nil {
			return
		}
		_, _ = io.WriteString(c, `{"apothecary_error":"the project's browser is off"}`+"\n")
	})
	env := filepath.Join(t.TempDir(), "run.env")
	if err := os.WriteFile(env, []byte("APOTHECARY_RUN_ID=run1\nAPOTHECARY_ENGINE_URL="+url+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := shimCmd(t, "browser-mcp")
	cmd.Env = append(cmd.Env, shimRunEnv+"="+env)
	r := runShim(t, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n", cmd)
	if r.code != 1 || !strings.Contains(r.stdout, "the project's browser is off") || !strings.Contains(r.stderr, "browser-mcp: ") {
		t.Errorf("browser-mcp = %+v", r)
	}

	// No engine to reach at all: said so, exit 1, before any stdio.
	cmd = shimCmd(t, "browser-mcp")
	cmd.Env = append(cmd.Env, shimRunEnv+"="+filepath.Join(t.TempDir(), "missing"))
	if r := runShim(t, "", cmd); r.code != 1 || !strings.Contains(r.stderr, "no engine to reach") {
		t.Errorf("no engine = %+v", r)
	}
}
