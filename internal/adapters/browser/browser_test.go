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
	"os"
	"path/filepath"
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

// --- the manager, on a scripted docker ------------------------------------------

type fakeDocker struct {
	mu        sync.Mutex
	calls     []string
	container string // "" | "<image>\t<state>"
	streams   [][]string
	stream    func(args []string, stdin io.Reader, stdout io.Writer) error
}

func (d *fakeDocker) run(_ context.Context, args []string) ([]byte, []byte, int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, strings.Join(args, " "))
	switch args[0] {
	case "ps":
		if d.container == "" || slices.Contains(args, "label=apothecary.browser=true") {
			return nil, nil, 0, nil
		}
		img, state, _ := strings.Cut(d.container, "\t")
		if strings.Contains(strings.Join(args, " "), "{{.State}}") {
			return []byte(d.container + "\n"), nil, 0, nil
		}
		_ = state
		return []byte(img + "\n"), nil, 0, nil
	case "run":
		d.container = args[len(args)-1] + "\trunning"
	case "rm":
		d.container = ""
	case "start":
		img, _, _ := strings.Cut(d.container, "\t")
		d.container = img + "\trunning"
	case "stop":
		img, _, _ := strings.Cut(d.container, "\t")
		d.container = img + "\texited"
	}
	return nil, nil, 0, nil
}

func (d *fakeDocker) exec(_ context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	d.mu.Lock()
	d.streams = append(d.streams, args)
	f := d.stream
	d.mu.Unlock()
	if f != nil {
		return f(args, stdin, stdout)
	}
	return nil
}

func (d *fakeDocker) did(prefix string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func newTestManager(t *testing.T, d *fakeDocker, img *string) *Manager {
	t.Helper()
	runs, err := os.MkdirTemp("", "br")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runs) })
	m := New(Config{RunsDir: runs, Run: d.run, Stream: d.exec, IdleAfter: 50 * time.Millisecond,
		Image: func(context.Context) (string, error) { return *img, nil },
		Tag:   func() string { return *img },
		Log:   slog.New(slog.DiscardHandler)})
	t.Cleanup(m.Close)
	return m
}

// A session brings the container up (no network, labelled, the project dir
// mounted), execs one session runner whose proxy socket routes to the run,
// and leaves the container to the idle stop when it ends.
func TestSessionBringsTheBrowserUpAndRoutesTheRun(t *testing.T) {
	d := &fakeDocker{}
	img := "apothecary/browser:aaa"
	m := newTestManager(t, d, &img)
	dial, _ := runApp(t, 3000)
	d.stream = func(args []string, stdin io.Reader, stdout io.Writer) error {
		i := slices.Index(args, "--sock")
		if i < 0 {
			return fmt.Errorf("no --sock in %v", args)
		}
		// What Chromium does through the relay: a proxy request on the socket.
		hc := &http.Client{Transport: &http.Transport{
			Proxy:       http.ProxyURL(&url.URL{Scheme: "http", Host: "relay"}),
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return net.Dial("unix", args[i+1]) },
		}}
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
	runCall := ""
	for _, c := range d.calls {
		if strings.HasPrefix(c, "run ") {
			runCall = c
		}
	}
	// Named by the project's id, lowercased; the name is a label for docker ps.
	dir := filepath.Join(m.cfg.RunsDir, "browser", "01hwebapp")
	for _, want := range []string{"--name apo-browser-01hwebapp", "--network none", "--label apothecary=true",
		"--label apothecary.project=01HWEBAPP", "--label apothecary.project.name=Web App",
		"--label apothecary.browser=true", "-v " + dir + ":" + dir, "apothecary/browser:aaa"} {
		if !strings.Contains(runCall, want) {
			t.Errorf("docker run lacks %q: %s", want, runCall)
		}
	}
	ex := strings.Join(d.streams[0], " ")
	if !strings.HasPrefix(ex, "exec -i apo-browser-01hwebapp /usr/local/bin/apothecary-shim browser-session --sock "+dir+"/s-") ||
		!strings.Contains(ex, " -- playwright-mcp --headless --isolated --browser chromium") {
		t.Errorf("exec = %s", ex)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the session left %v behind", entries)
	}
	// Idle: stopped once nobody has used it for IdleAfter.
	waitFor(t, func() bool { return d.did("stop") == 1 })
	st, err := m.Status(context.Background(), "Web App")
	if err != nil || st.State != "stopped" || st.Sessions != 0 || st.Image != img || st.Current != img {
		t.Errorf("status = %+v, %v", st, err)
	}
}

// A container on a stale image is recreated only when nobody uses it.
func TestStaleBrowserIsRecreatedOnlyWhenIdle(t *testing.T) {
	d := &fakeDocker{container: "apothecary/browser:old\trunning"}
	img := "apothecary/browser:new"
	m := newTestManager(t, d, &img)
	hold := make(chan struct{})
	started := make(chan struct{}, 2)
	d.stream = func([]string, io.Reader, io.Writer) error {
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
	// Nobody else is using it: the first session recreates it.
	first := sess()
	<-started
	if d.did("rm -f apo-browser-p") != 1 || !strings.HasPrefix(d.container, img) {
		t.Fatalf("calls = %v", d.calls)
	}
	// It goes stale again under a busy session: the second keeps it.
	img = "apothecary/browser:newer"
	second := sess()
	<-started
	if d.did("rm -f") != 1 || d.did("start apo-browser-p") != 1 {
		t.Errorf("a busy browser was recreated: %v", d.calls)
	}
	close(hold)
	for _, ch := range []chan error{first, second} {
		if err := <-ch; err != nil {
			t.Error(err)
		}
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
