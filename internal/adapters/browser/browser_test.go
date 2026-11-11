package browser

import (
	"apothecary/internal/ports/browserport"
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// runApp serves a tiny app on a loopback port and returns a Dial that
// reaches it for wantPort only — the run's workspace, as the engine's
// exposer would.
func runApp(t *testing.T, wantPort int) (Dial, *[]int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "app %s %s host=%s", r.Method, r.URL.Path, r.Host)
	}))
	t.Cleanup(srv.Close)
	var mu sync.Mutex
	dialed := &[]int{}
	return func(ctx context.Context, port int) (net.Conn, error) {
		mu.Lock()
		*dialed = append(*dialed, port)
		mu.Unlock()
		if port != wantPort {
			return nil, fmt.Errorf("nothing listens on %d", port)
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}, dialed
}

func proxyClient(t *testing.T, dial Dial) *http.Client {
	t.Helper()
	ps := httptest.NewServer(newProxy(dial, slog.New(slog.DiscardHandler)))
	t.Cleanup(ps.Close)
	u, _ := url.Parse(ps.URL)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}, Timeout: 5 * time.Second}
}

// The run's localhost, in any spelling, is the run's workspace port.
func TestProxyRoutesLoopbackToTheRun(t *testing.T) {
	dial, dialed := runApp(t, 3000)
	c := proxyClient(t, dial)
	for _, u := range []string{"http://localhost:3000/shot", "http://127.0.0.1:3000/shot", "http://[::1]:3000/shot", "http://app.localhost:3000/shot"} {
		resp, err := c.Get(u)
		if err != nil {
			t.Fatalf("GET %s: %v", u, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		host := strings.TrimSuffix(strings.TrimPrefix(u, "http://"), "/shot")
		if resp.StatusCode != 200 || string(body) != "app GET /shot host="+host {
			t.Errorf("GET %s = %d %q", u, resp.StatusCode, body)
		}
	}
	if slices.ContainsFunc(*dialed, func(p int) bool { return p != 3000 }) {
		t.Errorf("dialed %v", *dialed)
	}
	// A port the run does not serve is a gateway error, not a hang.
	resp, err := c.Get("http://localhost:4000/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("unserved port = %d", resp.StatusCode)
	}
}

// CONNECT (https, WebSockets) tunnels to the run's port, bytes as they are.
func TestProxyTunnelsConnectToTheRun(t *testing.T) {
	dial, _ := runApp(t, 3000)
	ps := httptest.NewServer(newProxy(dial, slog.New(slog.DiscardHandler)))
	defer ps.Close()
	c, err := net.Dial("tcp", strings.TrimPrefix(ps.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// The tunnelled request rides in the same write as the CONNECT: the
	// proxy must forward what it buffered.
	fmt.Fprint(c, "CONNECT localhost:3000 HTTP/1.1\r\nHost: localhost:3000\r\n\r\nGET /ws HTTP/1.1\r\nHost: localhost:3000\r\nConnection: close\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT = %v %v", resp, err)
	}
	inner, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(inner.Body)
	if string(body) != "app GET /ws host=localhost:3000" {
		t.Errorf("through the tunnel: %q", body)
	}
}

// Anything that is not the run is refused, both ways in: the browser is no
// way around the project's egress gateway.
func TestProxyRefusesTheOutsideWorld(t *testing.T) {
	dial, dialed := runApp(t, 3000)
	c := proxyClient(t, dial)
	for _, u := range []string{"http://example.com/", "http://10.0.0.5:3000/", "http://localhost.example.com:3000/", "https://example.com/"} {
		resp, err := c.Get(u)
		if strings.HasPrefix(u, "https") {
			// CONNECT refused: the client reports the proxy's status.
			if err == nil || !strings.Contains(err.Error(), "Forbidden") {
				t.Errorf("GET %s: err = %v, want Forbidden", u, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("GET %s: %v", u, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "localhost only") {
			t.Errorf("GET %s = %d %q", u, resp.StatusCode, body)
		}
	}
	if len(*dialed) != 0 {
		t.Errorf("the outside world was dialed: %v", *dialed)
	}
}

// --- the manager, on the fake host ---------------------------------------------

// A session brings the browser up, execs one session runner whose routing
// proxy routes to the run, closes that proxy with the session, and leaves the
// browser to the idle stop.
func TestSessionBringsTheBrowserUpAndRoutesTheRun(t *testing.T) {
	img := "mcr.example/playwright/mcp:v1@sha256:aaa"
	h := &hostFake{}
	m := New(Config{Host: h, IdleAfter: 50 * time.Millisecond, Log: slog.New(slog.DiscardHandler),
		Image: func(context.Context) (string, error) { return img, nil },
		Tag:   func() string { return img }})
	t.Cleanup(m.Close)
	dial, _ := runApp(t, 3000)
	var proxyAddr string
	h.exec = func(argv []string, stdin io.Reader, stdout io.Writer) error {
		i := slices.Index(argv, "--engine")
		if i < 0 {
			return fmt.Errorf("no --engine in %v", argv)
		}
		proxyAddr = argv[i+1]
		// What Chromium does through the relay: a proxy request to the engine.
		hc := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxyAddr})}}
		resp, err := hc.Get("http://localhost:3000/home")
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		_, err = io.Copy(stdout, io.MultiReader(strings.NewReader(string(body)+"\n"), stdin))
		return err
	}
	var out strings.Builder
	stdio := struct {
		io.Reader
		io.Writer
	}{strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n"), &out}
	if err := m.Session(context.Background(), browserport.BrowserSession{Project: "01HWEBAPP", ProjectName: "Web App", Run: "r1", Dial: dial}, stdio); err != nil {
		t.Fatal(err)
	}
	if want := "app GET /home host=localhost:3000\n" + `{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n"; out.String() != want {
		t.Errorf("session stdio = %q", out.String())
	}
	// The sole session may replace a stale browser.
	if got := strings.Join(h.ensured, ","); got != "01HWEBAPP:"+img+":replace" {
		t.Errorf("ensured = %s", got)
	}
	ex := strings.Join(h.execs[0], " ")
	if !strings.HasPrefix(ex, "/opt/apothecary/shim/apothecary-shim browser-session --engine "+proxyAddr+" -- ") ||
		!strings.Contains(ex, " -- node /app/cli.js --headless --isolated --browser chromium") {
		t.Errorf("exec = %s", ex)
	}
	// The session's routing proxy went with it.
	if c, err := net.DialTimeout("tcp", proxyAddr, time.Second); err == nil {
		c.Close()
		t.Error("the session's proxy outlived it")
	}
	// Idle: stopped once nobody has used it for IdleAfter.
	waitFor(t, func() bool { return len(h.stops()) == 1 })
	if h.stops()[0] != "01HWEBAPP" {
		t.Errorf("stopped = %v", h.stops())
	}
	h.image = img
	st, err := m.Status(context.Background(), "01HWEBAPP")
	if err != nil || st.State != "stopped" || st.Sessions != 0 || st.Image != img || st.Current != img || st.Container != "name-01HWEBAPP" {
		t.Errorf("status = %+v, %v", st, err)
	}
}

// A stale browser is replaced only by a session that has it to itself: a
// second session arriving while the first runs keeps what is there.
func TestStaleBrowserIsRecreatedOnlyWhenIdle(t *testing.T) {
	img := "img@sha256:new"
	var imgMu sync.Mutex
	h := &hostFake{}
	m := New(Config{Host: h, IdleAfter: time.Hour, Log: slog.New(slog.DiscardHandler),
		Image: func(context.Context) (string, error) { imgMu.Lock(); defer imgMu.Unlock(); return img, nil }})
	t.Cleanup(m.Close)
	hold := make(chan struct{})
	started := make(chan struct{}, 2)
	h.exec = func([]string, io.Reader, io.Writer) error {
		started <- struct{}{}
		<-hold
		return nil
	}
	sess := func() chan error {
		ch := make(chan error, 1)
		go func() {
			ch <- m.Session(context.Background(), browserport.BrowserSession{Project: "p", Run: "r",
				Dial: func(context.Context, int) (net.Conn, error) { return nil, io.EOF }}, struct {
				io.Reader
				io.Writer
			}{strings.NewReader(""), io.Discard})
		}()
		return ch
	}
	first := sess()
	<-started
	imgMu.Lock()
	img = "img@sha256:newer"
	imgMu.Unlock()
	second := sess()
	<-started
	close(hold)
	for _, ch := range []chan error{first, second} {
		if err := <-ch; err != nil {
			t.Error(err)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if got := strings.Join(h.ensured, ","); got != "p:img@sha256:new:replace,p:img@sha256:newer:keep" {
		t.Errorf("a busy browser was recreated: %s", got)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("condition never held")
}
