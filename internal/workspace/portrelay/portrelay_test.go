package portrelay

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"apothecary/internal/ports/envport"
)

// echoConn is a PortConn backed by a real TCP echo listener.
type echoConn struct {
	addr   string
	closed atomic.Bool
}

func newEchoConn(t *testing.T) *echoConn {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				data, _ := io.ReadAll(c)
				_, _ = c.Write(append([]byte("pong-"), data...))
			}(c)
		}
	}()
	return &echoConn{addr: ln.Addr().String()}
}

func (e *echoConn) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", e.addr)
}
func (e *echoConn) Close() error { e.closed.Store(true); return nil }

func dialURL(t *testing.T, u string) net.Conn {
	t.Helper()
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestExposePumpsAndIsIdempotent(t *testing.T) {
	m := New("127.0.0.1")
	defer m.CloseAll()
	pc := newEchoConn(t)
	u, err := m.Expose("run1", 3000, pc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "http://127.0.0.1:") {
		t.Errorf("url = %q", u)
	}

	c := dialURL(t, u)
	_, _ = c.Write([]byte("ping"))
	if cw, ok := c.(*net.TCPConn); ok {
		_ = cw.CloseWrite()
	}
	got, _ := io.ReadAll(c)
	c.Close()
	if string(got) != "pong-ping" {
		t.Errorf("round trip = %q", got)
	}

	// Second expose of the same port: same URL, redundant PortConn closed.
	pc2 := newEchoConn(t)
	u2, err := m.Expose("run1", 3000, pc2)
	if err != nil || u2 != u {
		t.Fatalf("idempotent expose = %q, %v", u2, err)
	}
	if !pc2.closed.Load() {
		t.Error("redundant PortConn must be closed")
	}
	if got := m.Existing("run1", 3000); got != u {
		t.Errorf("Existing = %q", got)
	}
	if list := m.List("run1"); len(list) != 1 || list[0].Port != 3000 || list[0].URL != u {
		t.Errorf("List = %+v", list)
	}
}

// fakeStore is an in-memory portrelay.Store.
type fakeStore struct {
	mu      sync.Mutex
	rows    map[string]map[int]int // runID -> wsPort -> hostPort
	deletes []string
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[string]map[int]int{}} }

func (f *fakeStore) SaveExposedPort(_ context.Context, runID string, wsPort, hostPort int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rows[runID] == nil {
		f.rows[runID] = map[int]int{}
	}
	f.rows[runID][wsPort] = hostPort
	return nil
}
func (f *fakeStore) DeleteExposedPorts(_ context.Context, runID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, runID)
	f.deletes = append(f.deletes, runID)
	return nil
}
func (f *fakeStore) LoadExposedPorts(context.Context) (map[string]map[int]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]map[int]int{}
	for r, ps := range f.rows {
		out[r] = map[int]int{}
		for p, h := range ps {
			out[r][p] = h
		}
	}
	return out, nil
}

func hostPortOf(t *testing.T, u string) int {
	t.Helper()
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	_, p, _ := net.SplitHostPort(parsed.Host)
	n, _ := strconv.Atoi(p)
	return n
}

func TestExposePersistsAndReExposesSameHostPort(t *testing.T) {
	store := newFakeStore()
	m := New("127.0.0.1")
	if err := m.Persist(store); err != nil {
		t.Fatal(err)
	}
	u1, err := m.Expose("run1", 3000, newEchoConn(t))
	if err != nil {
		t.Fatal(err)
	}
	hp := hostPortOf(t, u1)
	if store.rows["run1"][3000] != hp {
		t.Errorf("saved host port = %d, url = %d", store.rows["run1"][3000], hp)
	}
	if got := m.SavedPorts("run1"); len(got) != 1 || got[0] != 3000 {
		t.Errorf("SavedPorts = %v", got)
	}

	// Simulate a restart: a brand-new Manager loading the same store must
	// re-bind the SAME host port.
	m.CloseAll() // frees the listener but keeps the store (shutdown)
	m2 := New("127.0.0.1")
	if err := m2.Persist(store); err != nil {
		t.Fatal(err)
	}
	u2, err := m2.Expose("run1", 3000, newEchoConn(t))
	if err != nil {
		t.Fatal(err)
	}
	if hostPortOf(t, u2) != hp {
		t.Errorf("re-exposed host port = %d, want same %d (%s vs %s)", hostPortOf(t, u2), hp, u2, u1)
	}
	m2.CloseAll()
}

func TestExposeFallsBackWhenSavedPortTaken(t *testing.T) {
	store := newFakeStore()
	// Pretend run1:3000 was allocated host port P last time, but P is now
	// held by another process.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	taken := blocker.Addr().(*net.TCPAddr).Port
	store.rows["run1"] = map[int]int{3000: taken}

	m := New("127.0.0.1")
	if err := m.Persist(store); err != nil {
		t.Fatal(err)
	}
	u, err := m.Expose("run1", 3000, newEchoConn(t))
	if err != nil {
		t.Fatalf("expose must fall back, not fail: %v", err)
	}
	defer m.CloseAll()
	if hostPortOf(t, u) == taken {
		t.Errorf("must have picked a fresh port, not the taken %d", taken)
	}
	// And it persisted the NEW port.
	if store.rows["run1"][3000] == taken {
		t.Errorf("saved port must be the fresh one, not %d", taken)
	}
}

func TestCloseRunPrunesStoreButCloseAllKeepsIt(t *testing.T) {
	store := newFakeStore()
	m := New("127.0.0.1")
	_ = m.Persist(store)
	if _, err := m.Expose("keep", 8000, newEchoConn(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Expose("gone", 9000, newEchoConn(t)); err != nil {
		t.Fatal(err)
	}
	// CloseRun prunes the store (env destroyed).
	m.CloseRun("gone")
	if _, ok := store.rows["gone"]; ok {
		t.Error("CloseRun must delete the run's saved ports")
	}
	// CloseAll (shutdown) must NOT prune — the whole point is survival.
	m.CloseAll()
	if _, ok := store.rows["keep"]; !ok {
		t.Error("CloseAll must keep saved ports for the next restart")
	}
}

func TestCloseRunTearsDownListenersAndConns(t *testing.T) {
	m := New("127.0.0.1")
	pc := newEchoConn(t)
	u, err := m.Expose("run1", 8000, pc)
	if err != nil {
		t.Fatal(err)
	}
	m.CloseRun("run1")
	if !pc.closed.Load() {
		t.Error("PortConn not closed")
	}
	parsed, _ := url.Parse(u)
	if _, err := net.Dial("tcp", parsed.Host); err == nil {
		t.Error("listener still accepting after CloseRun")
	}
	if len(m.List("run1")) != 0 {
		t.Error("exposures survived CloseRun")
	}
}

var _ envport.PortConn = (*echoConn)(nil)

func TestHostRoutedAndReset(t *testing.T) {
	m := New("127.0.0.1")
	if m.HostRouted() {
		t.Error("a listener-mode manager is not host-routed")
	}
	m.RouteByHost("apps.example", "")
	if !m.HostRouted() || m.scheme != "https" {
		t.Errorf("default scheme = %q", m.scheme)
	}

	pc := &nopConn{}
	u, err := m.Expose("r1", 3000, pc)
	if err != nil {
		t.Fatal(err)
	}
	if u == "" {
		t.Fatal("no url")
	}
	if got := m.SavedPorts("r1"); len(got) != 1 || got[0] != 3000 {
		t.Errorf("SavedPorts = %v", got)
	}
	// Reset drops the run's exposures (a rebuild: the old relays are dead).
	m.Reset("r1")
	if _, _, _, ok := m.Lookup("3000-" + RunLabel("r1") + ".apps.example"); ok {
		t.Error("Lookup matched after Reset")
	}
	if !pc.closed {
		t.Error("Reset must close the exposure's conn")
	}
}

func TestUrlHostWildcardBinds(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "::", "[::]"} {
		if got := New(host).urlHost(); got != "localhost" {
			t.Errorf("urlHost(%q) = %q, want localhost", host, got)
		}
	}
	if got := New("10.0.0.5").urlHost(); got != "10.0.0.5" {
		t.Errorf("urlHost = %q", got)
	}
}

func TestLookupMalformedHosts(t *testing.T) {
	m := New("127.0.0.1")
	// Not host-routed: never matches.
	if _, _, _, ok := m.Lookup("3000-abc.apps.example"); ok {
		t.Error("no routing, no match")
	}
	m.RouteByHost("apps.example", "https")
	for _, host := range []string{
		"outside.other.com",           // wrong domain
		"a.b.apps.example",            // extra label
		"noport.apps.example",         // no "-"
		"xyz-run.apps.example",        // non-numeric port
		"3000-nosuchrun.apps.example", // nothing exposed
	} {
		if _, _, _, ok := m.Lookup(host); ok {
			t.Errorf("Lookup(%q) matched", host)
		}
	}
}

func TestPersistLoadsSavedAllocations(t *testing.T) {
	store := newFakeStore()
	store.rows["r1"] = map[int]int{8080: 41000}
	m := New("127.0.0.1")
	if err := m.Persist(store); err != nil {
		t.Fatal(err)
	}
	if got := m.SavedPorts("r1"); len(got) != 1 || got[0] != 8080 {
		t.Fatalf("SavedPorts = %v", got)
	}
}

func TestPersistLoadFailure(t *testing.T) {
	m := New("127.0.0.1")
	if err := m.Persist(failLoadStore{}); err == nil {
		t.Fatal("a load error must fail loud at boot")
	}
}

type failLoadStore struct{}

func (failLoadStore) SaveExposedPort(context.Context, string, int, int) error { return nil }
func (failLoadStore) DeleteExposedPorts(context.Context, string) error        { return nil }
func (failLoadStore) LoadExposedPorts(context.Context) (map[string]map[int]int, error) {
	return nil, errors.New("db down")
}

func TestRunLabel(t *testing.T) {
	if got := RunLabel("01HXYZABCDEF"); got != "xyzabcdef" && len(got) != 10 {
		t.Errorf("RunLabel = %q", got)
	}
	if got := RunLabel("SHORT"); got != "short" {
		t.Errorf("RunLabel short = %q", got)
	}
}

func TestForHost(t *testing.T) {
	wild := New("0.0.0.0")
	for _, c := range []struct{ raw, host, want string }{
		{"http://localhost:41234/", "100.1.2.3:8080", "http://100.1.2.3:41234/"},
		{"http://localhost:41234/x?y=1", "box.tail1234.ts.net", "http://box.tail1234.ts.net:41234/x?y=1"},
		{"http://localhost:41234/", "[fd7a::1]:8080", "http://[fd7a::1]:41234/"},
		{"http://localhost:41234/", "localhost:8080", "http://localhost:41234/"},
		{"http://localhost:41234/", "", "http://localhost:41234/"},
		{"http://127.0.0.1:41234/", "100.1.2.3", "http://127.0.0.1:41234/"},
		{"::bad", "100.1.2.3", "::bad"},
	} {
		if got := wild.ForHost(c.raw, c.host); got != c.want {
			t.Errorf("wildcard ForHost(%q, %q) = %q, want %q", c.raw, c.host, got, c.want)
		}
	}
	// A specific bind names its host already; host routing names its own.
	for _, m := range []*Manager{New("127.0.0.1"), New("localhost"), New("100.1.2.3")} {
		if got := m.ForHost("http://localhost:1/", "10.0.0.1"); got != "http://localhost:1/" {
			t.Errorf("%s: ForHost = %q", m.host, got)
		}
	}
	routed := New("0.0.0.0")
	routed.RouteByHost("apps.test", "http")
	if got := routed.ForHost("http://localhost:1/", "10.0.0.1"); got != "http://localhost:1/" {
		t.Errorf("host-routed ForHost = %q", got)
	}
}

// trackedConn counts the dials through the port Track wrapped.
type trackedConn struct {
	envport.PortConn
	dials *atomic.Int32
}

func (c trackedConn) Dial(ctx context.Context) (net.Conn, error) {
	c.dials.Add(1)
	return c.PortConn.Dial(ctx)
}

// roundTrip sends ping through u and returns what came back.
func roundTrip(t *testing.T, u string) string {
	t.Helper()
	c := dialURL(t, u)
	defer c.Close()
	_, _ = c.Write([]byte("ping"))
	_ = c.(*net.TCPConn).CloseWrite()
	got, _ := io.ReadAll(c)
	return string(got)
}

// An exposure outlives its workspace's sleep on the same URL: the first
// connection wakes the workspace and is served by the exposure the wake
// opens again — on a listener of its own, or routed by host.
func TestAnExposureWakesItsSleepingWorkspace(t *testing.T) {
	for _, routed := range []bool{false, true} {
		m := New("127.0.0.1")
		if routed {
			m.RouteByHost("apps.example.com", "http")
		}
		var woken atomic.Int32
		var u string
		next := newEchoConn(t)
		m.Wake = func(_ context.Context, runID string) error {
			woken.Add(1)
			// What the engine's wake does: the exposure opened again.
			m.Reset(runID)
			if again, err := m.Expose(runID, 3000, next); err != nil || again != u {
				t.Errorf("re-exposed on %q, %v", again, err)
			}
			return nil
		}
		old := newEchoConn(t)
		u, err := m.Expose("run1", 3000, old)
		if err != nil {
			t.Fatal(err)
		}
		m.Sleep("run1")
		m.Sleep("run1") // twice is once
		if !old.closed.Load() {
			t.Error("the sleeping workspace's path stayed open")
		}
		if l := m.List("run1"); len(l) != 1 || l[0].URL != u {
			t.Errorf("exposures while asleep = %v", l)
		}
		if routed {
			_, _, pc, ok := m.Lookup("3000-run1.apps.example.com")
			if !ok {
				t.Fatal("asleep: not routed")
			}
			c, err := pc.Dial(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			c.Close()
		} else if got := roundTrip(t, u); got != "pong-ping" {
			t.Errorf("through the woken exposure: %q", got)
		}
		if woken.Load() != 1 {
			t.Errorf("routed %v: woken %d times", routed, woken.Load())
		}
		m.CloseAll()
	}
}

// A dial into a sleeping workspace says why it cannot go through: no way
// to wake it, a wake that failed, a port the wake did not bring back.
func TestADormantExposureThatCannotWake(t *testing.T) {
	m := New("127.0.0.1")
	m.RouteByHost("apps.example.com", "http")
	if _, err := m.Expose("run1", 3000, &nopConn{}); err != nil {
		t.Fatal(err)
	}
	m.Sleep("run1")
	_, _, pc, _ := m.Lookup("3000-run1.apps.example.com")
	ctx := context.Background()
	if _, err := pc.Dial(ctx); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Errorf("no wake = %v", err)
	}
	m.Wake = func(context.Context, string) error { return errors.New("no node") }
	if _, err := pc.Dial(ctx); err == nil || !strings.Contains(err.Error(), "no node") {
		t.Errorf("failed wake = %v", err)
	}
	m.Wake = func(context.Context, string) error { return nil }
	if _, err := pc.Dial(ctx); err == nil || !strings.Contains(err.Error(), "did not come back") {
		t.Errorf("still dormant = %v", err)
	}
	m.Wake = func(_ context.Context, runID string) error { m.Reset(runID); return nil }
	if _, err := pc.Dial(ctx); err == nil || !strings.Contains(err.Error(), "did not come back") {
		t.Errorf("gone after the wake = %v", err)
	}
	if _, err := pc.Dial(ctx); err == nil || !strings.Contains(err.Error(), "no longer exposed") {
		t.Errorf("a dial after the exposure went = %v", err)
	}
}

// Track wraps each exposure's port before it is served: what stop-on-idle
// counts as use.
func TestExposeServesTheTrackedPort(t *testing.T) {
	m := New("127.0.0.1")
	defer m.CloseAll()
	var dials atomic.Int32
	var runs []string
	m.Track = func(runID string, pc envport.PortConn) envport.PortConn {
		runs = append(runs, runID)
		return trackedConn{PortConn: pc, dials: &dials}
	}
	u, err := m.Expose("run1", 3000, newEchoConn(t))
	if err != nil {
		t.Fatal(err)
	}
	c := dialURL(t, u)
	_, _ = c.Write([]byte("ping"))
	_ = c.(*net.TCPConn).CloseWrite()
	got, _ := io.ReadAll(c)
	c.Close()
	if string(got) != "pong-ping" || dials.Load() != 1 || len(runs) != 1 || runs[0] != "run1" {
		t.Errorf("got %q, %d dials, tracked %v", got, dials.Load(), runs)
	}
}
