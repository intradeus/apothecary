package sidecar

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// tcpDialer dials a fixed loopback address, as a PortConn dials the socket.
type tcpDialer struct {
	addr string
	err  error
}

func (d tcpDialer) Dial(ctx context.Context) (net.Conn, error) {
	if d.err != nil {
		return nil, d.err
	}
	var nd net.Dialer
	return nd.DialContext(ctx, "tcp", d.addr)
}

func serve(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(tcpDialer{addr: strings.TrimPrefix(srv.URL, "http://")})
}

// recorder keeps what the sidecar saw of the last request.
type recorder struct {
	method, path string
	query        url.Values
	body         string
	ctype        string
}

func recording(t *testing.T, answer string) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.method, rec.path, rec.query, rec.body = r.Method, r.URL.Path, r.URL.Query(), string(b)
		rec.ctype = r.Header.Get("Content-Type")
		_, _ = io.WriteString(w, answer)
	})
	return c, rec
}

func TestHealth(t *testing.T) {
	c, rec := recording(t, `{"boot":"b1","repo":"/home/u/repo","workspace":"/workspaces/app"}`)
	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h.Boot != "b1" || h.Repo != "/home/u/repo" || h.Workspace != "/workspaces/app" {
		t.Errorf("health = %+v", h)
	}
	if rec.method != "GET" || rec.path != "/health" {
		t.Errorf("request = %s %s", rec.method, rec.path)
	}
}

func TestAgentCallsShapeTheirRequests(t *testing.T) {
	ctx := context.Background()
	t.Run("start", func(t *testing.T) {
		c, rec := recording(t, `{"boot":"b","init":{"x":1},"proc":"p1"}`)
		out, err := c.Start(ctx, StartRequest{Argv: []string{"agent", "--acp"}, Env: map[string]string{"K": "v"}, Cwd: "/w"})
		if err != nil {
			t.Fatal(err)
		}
		if out.Proc != "p1" || string(out.Init) != `{"x":1}` {
			t.Errorf("start = %+v", out)
		}
		if rec.method != "POST" || rec.path != "/agent/start" || rec.ctype != "application/json" {
			t.Errorf("request = %s %s %s", rec.method, rec.path, rec.ctype)
		}
		var got StartRequest
		if err := json.Unmarshal([]byte(rec.body), &got); err != nil || got.Env["K"] != "v" || got.Argv[1] != "--acp" {
			t.Errorf("body = %s (%v)", rec.body, err)
		}
	})
	t.Run("session", func(t *testing.T) {
		c, rec := recording(t, `{"session_id":"s1","loaded":true,"cost_usd":1.5,"models":["fake/a"]}`)
		out, err := c.Session(ctx, SessionRequest{Proc: "p1", ReadOnly: true, MCPServers: []MCPServer{{Name: "m", URL: "http://x"}}})
		if err != nil {
			t.Fatal(err)
		}
		if out.SessionID != "s1" || !out.Loaded || out.CostUSD != 1.5 || len(out.Models) != 1 {
			t.Errorf("session = %+v", out)
		}
		if rec.path != "/agent/session" || !strings.Contains(rec.body, `"read_only":true`) || !strings.Contains(rec.body, `"mcp_servers"`) {
			t.Errorf("request = %s %s", rec.path, rec.body)
		}
	})
	t.Run("prompt", func(t *testing.T) {
		c, rec := recording(t, "")
		if err := c.Prompt(ctx, PromptRequest{SessionID: "s", Turn: "t", Text: "hi"}); err != nil {
			t.Fatal(err)
		}
		if rec.path != "/agent/prompt" || !strings.Contains(rec.body, `"text":"hi"`) {
			t.Errorf("request = %s %s", rec.path, rec.body)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		c, rec := recording(t, "")
		if err := c.Cancel(ctx, "s"); err != nil {
			t.Fatal(err)
		}
		if rec.path != "/agent/cancel" || rec.body != `{"session_id":"s"}` {
			t.Errorf("request = %s %s", rec.path, rec.body)
		}
	})
	t.Run("stop", func(t *testing.T) {
		c, rec := recording(t, "")
		if err := c.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		if rec.method != "POST" || rec.path != "/agent/stop" {
			t.Errorf("request = %s %s", rec.method, rec.path)
		}
	})
	t.Run("status", func(t *testing.T) {
		c, rec := recording(t, `{"boot":"b","running":true,"last_seq":7,"sessions":{"s":0.5},"turns":[{"id":"t1","session":"s"}],"procs":["p"]}`)
		st, err := c.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if rec.path != "/agent/status" || !st.Running || st.LastSeq != 7 || st.Sessions["s"] != 0.5 {
			t.Errorf("status = %+v", st)
		}
		if got := st.Turn("s"); got == nil || got.ID != "t1" {
			t.Errorf("Turn(s) = %+v", got)
		}
		if st.Turn("other") != nil {
			t.Error("a session with no turn must be nil")
		}
	})
}

func TestWorkspaceCallsEscapeTheirQuery(t *testing.T) {
	ctx := context.Background()
	const odd = "dir with spaces/a&b=c.go"
	t.Run("tree", func(t *testing.T) {
		c, rec := recording(t, `[{"name":"a","path":"a","dir":true}]`)
		out, err := c.Tree(ctx, "repo", odd)
		if err != nil {
			t.Fatal(err)
		}
		if rec.path != "/fs/tree" || rec.query.Get("path") != odd || rec.query.Get("repo") != "repo" {
			t.Errorf("request = %s %v", rec.path, rec.query)
		}
		if len(out) != 1 || !out[0].Dir {
			t.Errorf("tree = %+v", out)
		}
	})
	t.Run("file", func(t *testing.T) {
		c, rec := recording(t, `{"path":"a","size":3,"content":"abc"}`)
		out, err := c.File(ctx, "lib", odd)
		if err != nil {
			t.Fatal(err)
		}
		if rec.path != "/fs/file" || rec.query.Get("path") != odd || rec.query.Get("repo") != "lib" || out.Content != "abc" {
			t.Errorf("request = %s %v, %+v", rec.path, rec.query, out)
		}
	})
	t.Run("changes", func(t *testing.T) {
		c, rec := recording(t, `{"branch":"b","commits":[{"sha":"s"}],"files":[{"path":"p","additions":2}]}`)
		out, err := c.Changes(ctx, "", "origin/main")
		if err != nil {
			t.Fatal(err)
		}
		if rec.path != "/git/changes" || rec.query.Get("base") != "origin/main" || out.Files[0].Additions != 2 {
			t.Errorf("request = %s %v, %+v", rec.path, rec.query, out)
		}
	})
	t.Run("diff", func(t *testing.T) {
		c, rec := recording(t, `{"path":"p","diff":"@@"}`)
		out, err := c.Diff(ctx, "r", "b", odd)
		if err != nil {
			t.Fatal(err)
		}
		if rec.path != "/git/diff" || rec.query.Get("path") != odd || rec.query.Get("base") != "b" || out.Diff != "@@" {
			t.Errorf("request = %s %v, %+v", rec.path, rec.query, out)
		}
	})
}

func TestErrorAnswers(t *testing.T) {
	ctx := context.Background()
	t.Run("a json error", func(t *testing.T) {
		c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"a turn is running"}`)
		})
		_, err := c.Health(ctx)
		if !IsStatus(err, http.StatusConflict) || IsStatus(err, http.StatusNotFound) {
			t.Fatalf("err = %v", err)
		}
		var e *Error
		if !errors.As(err, &e) || e.Msg != "a turn is running" {
			t.Fatalf("err = %#v", err)
		}
		if got := err.Error(); got != "sidecar 409: a turn is running" {
			t.Errorf("Error() = %q", got)
		}
	})
	t.Run("a plain body", func(t *testing.T) {
		c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "  no such file  ", http.StatusNotFound)
		})
		var e *Error
		if _, err := c.File(ctx, "", "x"); !errors.As(err, &e) || e.Msg != "no such file" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("json without the field", func(t *testing.T) {
		c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"other":1}`)
		})
		var e *Error
		if _, err := c.Status(ctx); !errors.As(err, &e) || e.Msg != `{"other":1}` {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a redirect is not a success", func(t *testing.T) {
		c := serve(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusMultipleChoices) })
		if err := c.Stop(ctx); !IsStatus(err, http.StatusMultipleChoices) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("not a sidecar error", func(t *testing.T) {
		if IsStatus(errors.New("plain"), 500) {
			t.Fatal("a plain error has no status")
		}
		if !IsStatus(fmt.Errorf("wrapped: %w", &Error{Status: 500}), 500) {
			t.Fatal("IsStatus must see through wrapping")
		}
	})
	t.Run("an unusable body", func(t *testing.T) {
		c := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "not json") })
		if _, err := c.Health(ctx); err == nil {
			t.Fatal("a garbage answer must not decode")
		}
	})
	t.Run("an unmarshalable request", func(t *testing.T) {
		c := serve(t, func(http.ResponseWriter, *http.Request) { t.Error("nothing should be sent") })
		if err := c.do(ctx, "POST", "/x", make(chan int), nil); err == nil {
			t.Fatal("an unmarshalable body must fail")
		}
	})
	t.Run("a bad method", func(t *testing.T) {
		c := serve(t, func(http.ResponseWriter, *http.Request) { t.Error("nothing should be sent") })
		if err := c.do(ctx, "BAD METHOD", "/x", nil, nil); err == nil {
			t.Fatal("a bad method must fail")
		}
	})
}

func TestDialFailureSurfaces(t *testing.T) {
	boom := errors.New("workspace is gone")
	c := New(tcpDialer{err: boom})
	ctx := context.Background()
	if _, err := c.Health(ctx); !errors.Is(err, boom) {
		t.Errorf("Health = %v", err)
	}
	if _, _, err := c.Events(ctx, "", 0); !errors.Is(err, boom) {
		t.Errorf("Events = %v", err)
	}
	if _, err := c.Buffered(ctx, "", 0); !errors.Is(err, boom) {
		t.Errorf("Buffered = %v", err)
	}
	if _, err := c.ServiceLog(ctx, "app", 1); !errors.Is(err, boom) {
		t.Errorf("ServiceLog = %v", err)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	if got := Cursor("boot-1", 42); got != "boot-1:42" {
		t.Fatalf("Cursor = %q", got)
	}
	boot, seq, ok := ParseCursor("boot-1:42")
	if !ok || boot != "boot-1" || seq != 42 {
		t.Fatalf("ParseCursor = %q, %d, %v", boot, seq, ok)
	}
	for _, bad := range []string{"", "nocolon", ":42", "b:", "b:notanumber"} {
		if b, s, ok := ParseCursor(bad); ok || b != "" || s != 0 {
			t.Errorf("ParseCursor(%q) = %q, %d, %v", bad, b, s, ok)
		}
	}
}

func TestEventsFollowsAndSkipsNoise(t *testing.T) {
	var query url.Values
	release := make(chan struct{})
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, `{"seq":1,"type":"text","text":"hi"}`+"\n\nnot json\n"+`{"seq":2,"type":"turn_end"}`+"\n")
		fl.Flush()
		<-release
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	evs, errc, err := c.Events(ctx, "boot-1", 5)
	if err != nil {
		t.Fatal(err)
	}
	if query.Get("after") != "5" || query.Get("boot") != "boot-1" || query.Has("follow") {
		t.Errorf("query = %v", query)
	}
	for _, want := range []int64{1, 2} {
		select {
		case ev := <-evs:
			if ev.Seq != want {
				t.Fatalf("seq = %d, want %d", ev.Seq, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("no event")
		}
	}
	close(release) // the stream ends: EOF comes through errc, out closes
	select {
	case err := <-errc:
		if !errors.Is(err, io.EOF) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no end of stream")
	}
	if _, open := <-evs; open {
		t.Error("the event channel must close")
	}
}

func TestEventsStopsWithItsContext(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.(http.Flusher).Flush()
		<-block
	})
	ctx, cancel := context.WithCancel(context.Background())
	_, errc, err := c.Events(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want the context's", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stream outlived its context")
	}
}

// A consumer that stopped reading must not pin the stream goroutine.
func TestEventsDoesNotBlockOnASlowConsumerAfterCancel(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		for i := range 200 {
			if _, err := fmt.Fprintf(w, `{"seq":%d}`+"\n", i); err != nil {
				return
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	evs, errc, err := c.Events(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	<-evs
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stuck sending to a consumer that left")
	}
}

func TestEventsRefused(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	if _, _, err := c.Events(context.Background(), "", 0); !IsStatus(err, http.StatusServiceUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.Buffered(context.Background(), "", 0); !IsStatus(err, http.StatusServiceUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestBuffered(t *testing.T) {
	var query url.Values
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = io.WriteString(w, `{"seq":3}`+"\n"+`{"seq":4,"type":"gap"}`+"\n")
	})
	got, err := c.Buffered(context.Background(), "b", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Type != "gap" {
		t.Fatalf("events = %+v", got)
	}
	if query.Get("follow") != "0" || query.Get("after") != "2" || query.Get("boot") != "b" {
		t.Errorf("query = %v", query)
	}

	// A bad line ends the look-back, keeping what came before it.
	c = serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"seq":1}`+"\ngarbage\n") })
	got, err = c.Buffered(context.Background(), "", 0)
	if err == nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestServices(t *testing.T) {
	c, rec := recording(t, `[{"name":"app","phase":"healthy","by":"stage"}]`)
	got, err := c.Services(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rec.path != "/services" || len(got) != 1 || got[0].Phase != "healthy" {
		t.Fatalf("%s: %+v", rec.path, got)
	}
}

func TestServiceLog(t *testing.T) {
	var seen string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.RequestURI()
		_, _ = io.WriteString(w, "line 1\nline 2\n")
	})
	got, err := c.ServiceLog(context.Background(), "my app/x", 50)
	if err != nil {
		t.Fatal(err)
	}
	if got != "line 1\nline 2\n" {
		t.Errorf("log = %q", got)
	}
	if seen != "/services/my%20app%2Fx/log?tail=50" {
		t.Errorf("request = %q: the name must stay one path segment", seen)
	}

	c = serve(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no such service", http.StatusNotFound) })
	if _, err := c.ServiceLog(context.Background(), "x", 1); !IsStatus(err, http.StatusNotFound) || !strings.Contains(err.Error(), "no such service") {
		t.Fatalf("err = %v", err)
	}
}

// The proxy keeps the browser's query, pins repo to the caller's choice (never
// the browser's), and always lands on /term.
func TestTerminalProxyRewrites(t *testing.T) {
	var got *http.Request
	c := serve(t, func(_ http.ResponseWriter, r *http.Request) { got = r })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/runs/r1/terminal?cols=80&repo=evil", nil)
	c.TerminalProxy("lib").ServeHTTP(rec, req)
	if got == nil {
		t.Fatal("the sidecar saw nothing")
	}
	if got.URL.Path != "/term" || got.Host != "sidecar" {
		t.Errorf("forwarded to %s (host %s)", got.URL.Path, got.Host)
	}
	if got.URL.Query().Get("repo") != "lib" || got.URL.Query().Get("cols") != "80" {
		t.Errorf("query = %v", got.URL.Query())
	}
}

func TestTerminalProxyDialFailureIsABadGateway(t *testing.T) {
	c := New(tcpDialer{err: errors.New("workspace is gone")})
	rec := httptest.NewRecorder()
	c.TerminalProxy("").ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "terminal: ") {
		t.Fatalf("proxy = %d %q", rec.Code, rec.Body.String())
	}
}

// A hand-written Upgrade: the 101 must pass and the two sides must splice.
func TestTerminalProxySplicesAnUpgrade(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = brw.Flush()
		line, _ := brw.ReadString('\n')
		_, _ = brw.WriteString(strings.ToUpper(line))
		_ = brw.Flush()
	})
	front := httptest.NewServer(c.TerminalProxy(""))
	defer front.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, _ = io.WriteString(conn, "GET /term HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d", res.StatusCode)
	}
	_, _ = io.WriteString(conn, "hello\n")
	line, err := br.ReadString('\n')
	if err != nil || line != "HELLO\n" {
		t.Fatalf("echo = %q, %v", line, err)
	}
}
