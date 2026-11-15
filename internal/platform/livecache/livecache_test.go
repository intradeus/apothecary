package livecache

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

const ttl = time.Minute

var t0 = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// clock is the fake clock the tests move.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// source is the remote: a load answers val or err as they are when it
// starts, and counts; while a gate is set the load waits for it to close.
type source struct {
	mu    sync.Mutex
	val   string
	err   error
	gate  chan struct{}
	loads int
}

func (s *source) load(ctx context.Context) (string, error) {
	s.mu.Lock()
	s.loads++
	val, err, gate := s.val, s.err, s.gate
	s.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return val, err
}

func (s *source) set(val string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.val, s.err = val, err
}

// block makes the loads from now on wait until the gate returned closes.
func (s *source) block() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gate = make(chan struct{})
	return s.gate
}

func (s *source) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads
}

func newCache(cfg Config) (*Cache, *clock) {
	clk := &clock{t: t0}
	cfg.Now = clk.Now
	return New(cfg), clk
}

// get is a Get that must succeed.
func get(t *testing.T, c *Cache, group, key string, s *source) string {
	t.Helper()
	v, err := Get(t.Context(), c, group, key, ttl, s.load)
	if err != nil {
		t.Fatalf("Get(%s, %s): %v", group, key, err)
	}
	return v
}

func wantLoads(t *testing.T, s *source, n int) {
	t.Helper()
	if got := s.count(); got != n {
		t.Errorf("loads = %d, want %d", got, n)
	}
}

// limited is a remote's refusal for its rate limit.
type limited struct{ wait time.Duration }

func (l *limited) Error() string { return "rate limited" }

func holdLimited(err error) (time.Duration, bool) {
	var l *limited
	if errors.As(err, &l) {
		return l.wait, true
	}
	return 0, false
}

func TestNewDefaults(t *testing.T) {
	c := New(Config{})
	if c.now == nil || c.idle != DefaultIdle || c.timeout != DefaultLoadTimeout {
		t.Errorf("New(Config{}) = now set %v, idle %s, timeout %s; want the defaults", c.now != nil, c.idle, c.timeout)
	}
	if _, ok := c.hold(errors.New("down")); ok {
		t.Error("with no Hold, an error held the group")
	}
}

// TestFreshHitDoesNotLoad: a copy younger than the TTL is served as it is.
func TestFreshHitDoesNotLoad(t *testing.T) {
	c, clk := newCache(Config{})
	s := &source{val: "v1"}
	if v := get(t, c, "jira", "issue/1", s); v != "v1" {
		t.Fatalf("first Get = %q, want v1", v)
	}
	s.set("v2", nil)
	clk.add(ttl - time.Second)
	if v := get(t, c, "jira", "issue/1", s); v != "v1" {
		t.Errorf("fresh Get = %q, want the copy v1", v)
	}
	wantLoads(t, s, 1)
}

// TestStaleServedWhileOneRefreshRuns: past its TTL the copy is still served
// while one background load replaces it, and the next Get gets the new value.
func TestStaleServedWhileOneRefreshRuns(t *testing.T) {
	c, clk := newCache(Config{})
	s := &source{val: "v1"}
	get(t, c, "jira", "issue/1", s)
	s.set("v2", nil)
	gate := s.block()
	clk.add(ttl)
	for range 3 {
		if v := get(t, c, "jira", "issue/1", s); v != "v1" {
			t.Fatalf("Get while refreshing = %q, want the old copy v1", v)
		}
	}
	close(gate)
	c.Wait()
	wantLoads(t, s, 2)
	if v := get(t, c, "jira", "issue/1", s); v != "v2" {
		t.Errorf("Get after the refresh = %q, want v2", v)
	}
	wantLoads(t, s, 2)
}

// TestMissLoadsOnce: concurrent Gets of a key with no copy share one load.
func TestMissLoadsOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := newCache(Config{})
		s := &source{val: "v1"}
		gate := s.block()
		got := make([]string, 8)
		errs := make([]error, len(got))
		var wg sync.WaitGroup
		for i := range got {
			wg.Go(func() { got[i], errs[i] = Get(t.Context(), c, "jira", "issue/1", ttl, s.load) })
		}
		synctest.Wait() // every Get waits on the one load, held at the gate
		close(gate)
		wg.Wait()
		for i := range got {
			if got[i] != "v1" || errs[i] != nil {
				t.Errorf("Get %d = %q, %v; want v1", i, got[i], errs[i])
			}
		}
		wantLoads(t, s, 1)
	})
}

// TestBypassLoadsAndFallsBack: a bypass loads and stores; when the load
// fails it returns the copy with a StaleError, with no copy the error.
func TestBypassLoadsAndFallsBack(t *testing.T) {
	c, _ := newCache(Config{})
	s := &source{val: "v1"}
	get(t, c, "jira", "issue/1", s)
	s.set("v2", nil)
	if v, err := Bypass(t.Context(), c, "jira", "issue/1", s.load); v != "v2" || err != nil {
		t.Fatalf("Bypass = %q, %v; want a load (v2)", v, err)
	}
	if v := get(t, c, "jira", "issue/1", s); v != "v2" {
		t.Errorf("Get after a bypass = %q, want its copy v2", v)
	}

	down := errors.New("tracker down")
	s.set("v3", down)
	var stale *StaleError
	v, err := Bypass(t.Context(), c, "jira", "issue/1", s.load)
	if v != "v2" || !errors.As(err, &stale) || !errors.Is(err, down) || !stale.Loaded.Equal(t0) {
		t.Errorf("Bypass of a failing load = %q, %v; want the copy v2 with a StaleError wrapping the failure", v, err)
	}
	if err != nil && !strings.Contains(err.Error(), "tracker down") {
		t.Errorf("StaleError = %q, want the failure named", err)
	}
	if v, err := Bypass(t.Context(), c, "jira", "issue/2", s.load); v != "" || !errors.Is(err, down) || errors.As(err, &stale) {
		t.Errorf("Bypass of a failing load with no copy = %q, %v; want the failure alone", v, err)
	}
	if v := get(t, c, "jira", "issue/1", s); v != "v2" {
		t.Errorf("Get after a failed bypass = %q, want the copy v2", v)
	}
	wantLoads(t, s, 4)
}

// TestInvalidate: a key or a prefix of a group is dropped and loads anew;
// other keys and groups keep their copies, and a refresh running for a
// dropped key stores nothing.
func TestInvalidate(t *testing.T) {
	c, clk := newCache(Config{})
	s := &source{val: "v1"}
	for _, k := range [][2]string{{"jira", "issue/1"}, {"jira", "issue/2"}, {"jira", "list/a"}, {"github", "issue/1"}} {
		get(t, c, k[0], k[1], s)
	}
	s.set("v2", nil)
	c.InvalidatePrefix("jira", "issue/")
	c.InvalidatePrefix("gitlab", "")
	for _, tc := range []struct{ group, key, want string }{
		{"jira", "issue/1", "v2"},
		{"jira", "issue/2", "v2"},
		{"jira", "list/a", "v1"},
		{"github", "issue/1", "v1"},
	} {
		if v := get(t, c, tc.group, tc.key, s); v != tc.want {
			t.Errorf("after InvalidatePrefix(jira, issue/), Get(%s, %s) = %q, want %q", tc.group, tc.key, v, tc.want)
		}
	}
	c.Invalidate("jira", "list/a")
	c.Invalidate("gitlab", "list/a")
	if v := get(t, c, "jira", "list/a", s); v != "v2" {
		t.Errorf("after Invalidate, Get = %q, want a load (v2)", v)
	}
	wantLoads(t, s, 7)

	clk.add(ttl)
	s.set("v3", nil)
	gate := s.block()
	get(t, c, "github", "issue/1", s) // starts a refresh, held at the gate
	c.Invalidate("github", "issue/1")
	close(gate)
	c.Wait()
	s.set("v4", nil)
	if v := get(t, c, "github", "issue/1", s); v != "v4" {
		t.Errorf("Get after a refresh of a dropped key = %q, want a new load (v4)", v)
	}
}

// TestHold: a refresh refused for a rate limit holds its group. Until the
// hold ends copies are served at any age and a key with no copy answers a
// HeldError, all with no load; other groups load as usual. Then loads resume.
func TestHold(t *testing.T) {
	c, clk := newCache(Config{Hold: holdLimited})
	s := &source{val: "v1"}
	get(t, c, "jira", "issue/1", s)
	refused := &limited{wait: time.Minute}
	s.set("v2", refused)
	clk.add(ttl)
	get(t, c, "jira", "issue/1", s) // its refresh is refused
	c.Wait()
	wantLoads(t, s, 2)
	until := t0.Add(ttl + time.Minute)

	s.set("v2", nil)
	clk.add(30 * time.Second)
	if v := get(t, c, "jira", "issue/1", s); v != "v1" {
		t.Errorf("Get of a held group = %q, want the copy v1", v)
	}
	var held *HeldError
	_, err := Get(t.Context(), c, "jira", "issue/2", ttl, s.load)
	if !errors.As(err, &held) || !errors.Is(err, refused) || !held.Until.Equal(until) {
		t.Errorf("Get of a held group with no copy: %v; want a HeldError until %s wrapping the refusal", err, until)
	}
	if err != nil && !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("HeldError = %q, want the refusal named", err)
	}
	var stale *StaleError
	v, err := Bypass(t.Context(), c, "jira", "issue/1", s.load)
	if v != "v1" || !errors.As(err, &stale) || !errors.As(err, &held) {
		t.Errorf("Bypass of a held group = %q, %v; want the copy v1 with a StaleError wrapping a HeldError", v, err)
	}
	wantLoads(t, s, 2)
	if v := get(t, c, "github", "issue/1", s); v != "v2" {
		t.Errorf("Get of another group = %q, want a load (v2)", v)
	}
	wantLoads(t, s, 3)

	clk.add(30 * time.Second)
	if v := get(t, c, "jira", "issue/2", s); v != "v2" {
		t.Errorf("Get after the hold = %q, want a load (v2)", v)
	}
	if v := get(t, c, "jira", "issue/1", s); v != "v1" {
		t.Errorf("Get of the old copy after the hold = %q, want v1 while it refreshes", v)
	}
	c.Wait()
	if v := get(t, c, "jira", "issue/1", s); v != "v2" {
		t.Errorf("Get after the refresh = %q, want v2", v)
	}
	wantLoads(t, s, 5)
}

// TestHeldByAMiss: the Get whose load was refused gets the refusal itself;
// the next one a HeldError, with no load.
func TestHeldByAMiss(t *testing.T) {
	c, _ := newCache(Config{Hold: holdLimited})
	refused := &limited{wait: time.Minute}
	s := &source{err: refused}
	var held *HeldError
	if _, err := Get(t.Context(), c, "jira", "issue/1", ttl, s.load); !errors.Is(err, refused) || errors.As(err, &held) {
		t.Errorf("refused Get: %v; want the refusal itself", err)
	}
	if _, err := Get(t.Context(), c, "jira", "issue/1", ttl, s.load); !errors.As(err, &held) {
		t.Errorf("Get after a refusal: %v; want a HeldError", err)
	}
	wantLoads(t, s, 1)
}

// TestLongerHoldStays: a shorter hold set by a load that ends later does not
// cut a longer one short.
func TestLongerHoldStays(t *testing.T) {
	c, clk := newCache(Config{Hold: holdLimited})
	long, short := &source{val: "v1"}, &source{val: "v1"}
	get(t, c, "jira", "issue/1", long)
	get(t, c, "jira", "issue/2", short)
	clk.add(ttl)
	long.set("v2", &limited{wait: time.Minute})
	short.set("v2", &limited{wait: 10 * time.Second})
	longGate, shortGate := long.block(), short.block()
	get(t, c, "jira", "issue/1", long)
	get(t, c, "jira", "issue/2", short)
	c.mu.Lock()
	first := c.groups["jira"].entries["issue/1"].flight
	c.mu.Unlock()
	close(longGate)
	<-first.done
	close(shortGate)
	c.Wait()

	clk.add(30 * time.Second)
	var held *HeldError
	if _, err := Get(t.Context(), c, "jira", "issue/3", ttl, long.load); !errors.As(err, &held) || !held.Until.Equal(t0.Add(ttl+time.Minute)) {
		t.Errorf("Get after a short refusal ended a long one: %v; want held until %s", err, t0.Add(ttl+time.Minute))
	}
}

// TestIdleEviction: an entry unread for Idle is not served again and goes at
// the next sweep, as does a group left empty; a read keeps an entry.
func TestIdleEviction(t *testing.T) {
	c, clk := newCache(Config{Idle: 30 * time.Minute})
	s := &source{val: "v1"}
	read := func(group, key string) string {
		t.Helper()
		v, err := Get(t.Context(), c, group, key, 2*time.Hour, s.load) // no refresh here
		if err != nil {
			t.Fatalf("Get(%s, %s): %v", group, key, err)
		}
		return v
	}
	kept := func(group, key string) bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		g := c.groups[group]
		return g != nil && g.entries[key] != nil
	}
	read("jira", "a")
	read("jira", "b")
	read("github", "c")
	clk.add(20 * time.Minute)
	read("jira", "a")
	clk.add(15 * time.Minute) // a sweep: b and c unread for 35 minutes, a for 15
	read("jira", "d")
	if !kept("jira", "a") || kept("jira", "b") {
		t.Errorf("after the sweep: a kept %v, b kept %v; want a only", kept("jira", "a"), kept("jira", "b"))
	}
	c.mu.Lock()
	_, ok := c.groups["github"]
	c.mu.Unlock()
	if ok {
		t.Error("the sweep kept a group left empty")
	}
	wantLoads(t, s, 4)

	s.set("v2", nil)
	clk.add(20 * time.Minute) // a unread for 35 minutes, the last sweep 20 ago
	if v := read("jira", "a"); v != "v2" {
		t.Errorf("Get of an entry unread for Idle = %q, want a new load (v2)", v)
	}
	wantLoads(t, s, 5)
}

// TestLoadOutlivesItsCaller: a caller leaving gets its context's error, the
// load runs on under LoadTimeout and stores its copy.
func TestLoadOutlivesItsCaller(t *testing.T) {
	c, _ := newCache(Config{LoadTimeout: time.Second})
	s := &source{val: "v1"}
	gate := s.block()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Get(ctx, c, "jira", "issue/1", ttl, s.load); !errors.Is(err, context.Canceled) {
		t.Errorf("Get of a caller gone: %v; want its context's error", err)
	}
	close(gate)
	c.Wait()
	if v := get(t, c, "jira", "issue/1", s); v != "v1" {
		t.Errorf("Get after the caller left = %q, want the load's copy v1", v)
	}
	wantLoads(t, s, 1)

	_, err := Get(t.Context(), c, "jira", "issue/2", ttl, func(ctx context.Context) (string, error) {
		dl, ok := ctx.Deadline()
		if !ok || time.Until(dl) > time.Second {
			return "", fmt.Errorf("deadline %v (set %v), want within LoadTimeout", dl, ok)
		}
		return "v1", nil
	})
	if err != nil {
		t.Error(err)
	}
}

// TestPanicIsAnError: a load that panics fails its Get and stores nothing.
func TestPanicIsAnError(t *testing.T) {
	c, _ := newCache(Config{})
	_, err := Get(t.Context(), c, "jira", "issue/1", ttl, func(context.Context) (string, error) { panic("client bug") })
	if err == nil || !strings.Contains(err.Error(), "client bug") {
		t.Errorf("Get of a panicking load: %v; want an error naming the panic", err)
	}
	if v := get(t, c, "jira", "issue/1", &source{val: "v1"}); v != "v1" {
		t.Errorf("Get after the panic = %q, want a load (v1)", v)
	}
}

// TestKeyHoldsOneType: reading a key as another type than it holds is an
// error, not a panic.
func TestKeyHoldsOneType(t *testing.T) {
	c, _ := newCache(Config{})
	get(t, c, "jira", "issue/1", &source{val: "v1"})
	n, err := Get(t.Context(), c, "jira", "issue/1", ttl, func(context.Context) (int, error) { return 1, nil })
	if n != 0 || err == nil {
		t.Errorf("Get[int] of a string = %d, %v; want an error", n, err)
	}
}
