package portrelay

import (
	"apothecary/internal/ports/envport"
	"context"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
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
