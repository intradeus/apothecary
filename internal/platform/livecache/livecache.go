// Package livecache is a stale-while-revalidate cache for values read live
// from a remote: a copy younger than its TTL is served as it is, an older
// one is served while one background load replaces it, and a miss loads
// once for every caller asking at the time.
//
// Entries live in groups (one per remote) under string keys. The Cache holds
// them untyped and Get and Bypass type them for the caller, rather than one
// typed Cache per kind of value: a remote's hold (it asked to be left alone)
// and a prefix invalidation (a write dropped an issue and every list page)
// then reach all the kinds of value its group holds. A key holds one type,
// so callers namespace their keys by kind ("issue/…", "list/…").
//
// A group is held when Config.Hold classifies a load's error: until the hold
// ends no load of the group starts, copies are served at any age, and a key
// with no copy answers a *HeldError.
package livecache

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultIdle is how long an entry may go unread before it is dropped.
	DefaultIdle = 30 * time.Minute
	// DefaultLoadTimeout bounds a load: a remote call or two, each under its
	// client's own timeout.
	DefaultLoadTimeout = time.Minute
)

// Config tunes a Cache; every field has a default.
type Config struct {
	// Now is the clock (nil = time.Now).
	Now func() time.Time
	// Hold classifies a load's error: ok means the remote asked to be left
	// alone (a rate limit), and the load's group is held for d from the
	// error; a longer hold already set stays. nil = no error holds.
	Hold func(err error) (d time.Duration, ok bool)
	// Idle is how long an entry may go unread before it is dropped (0 =
	// DefaultIdle).
	Idle time.Duration
	// LoadTimeout bounds every load (0 = DefaultLoadTimeout).
	LoadTimeout time.Duration
}

// Cache holds the copies; safe for concurrent use.
type Cache struct {
	now     func() time.Time
	hold    func(error) (time.Duration, bool)
	idle    time.Duration
	timeout time.Duration
	loads   sync.WaitGroup

	mu     sync.Mutex
	groups map[string]*group
	swept  time.Time // the last sweep for unread entries
}

type group struct {
	entries map[string]*entry
	until   time.Time // held until then; past = not held
	cause   error     // the load error that set the hold
}

// entry is a key's copy and the load in flight for it. An entry without a
// copy exists only while its first load runs.
type entry struct {
	val    any
	has    bool      // val is a loaded copy
	loaded time.Time // when val was stored
	read   time.Time // the last Get or Bypass of the key
	flight *flight   // nil = no load running
}

// flight is one load shared by every caller waiting on it; val and err are
// set before done closes.
type flight struct {
	done chan struct{}
	val  any
	err  error
}

type loader func(context.Context) (any, error)

// HeldError answers a read of a held group that has no copy to serve: no
// load was made.
type HeldError struct {
	Until time.Time // the hold ends then
	Err   error     // the load error that set the hold
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("%v (held until %s)", e.Err, e.Until.Format(time.RFC3339))
}

func (e *HeldError) Unwrap() error { return e.Err }

// StaleError comes with the copy a Bypass fell back to: Err is why nothing
// newer could be read (the load's error, or a *HeldError when the group is
// held and no load was made).
type StaleError struct {
	Loaded time.Time // when the copy served was stored
	Err    error
}

func (e *StaleError) Error() string {
	return fmt.Sprintf("%v (serving the copy from %s)", e.Err, e.Loaded.Format(time.RFC3339))
}

func (e *StaleError) Unwrap() error { return e.Err }

// New builds an empty cache.
func New(cfg Config) *Cache {
	c := &Cache{now: cfg.Now, hold: cfg.Hold, idle: cfg.Idle, timeout: cfg.LoadTimeout, groups: map[string]*group{}}
	if c.now == nil {
		c.now = time.Now
	}
	if c.hold == nil {
		c.hold = func(error) (time.Duration, bool) { return 0, false }
	}
	if c.idle <= 0 {
		c.idle = DefaultIdle
	}
	if c.timeout <= 0 {
		c.timeout = DefaultLoadTimeout
	}
	return c
}

// Get returns group's copy of key. One stored less than ttl ago is served
// as it is; an older one is served too, while a background load (one at a
// time per key) replaces it for the next Get. With no copy, Get waits for a
// load that every concurrent caller of the key shares; a failed load stores
// nothing and its error goes to each of them. ctx ending ends the wait, not
// the load, which runs under ctx's values only.
func Get[V any](ctx context.Context, c *Cache, group, key string, ttl time.Duration, load func(context.Context) (V, error)) (V, error) {
	v, err := c.get(ctx, group, key, ttl, false, erase(load))
	return typed[V](group, key, v, err)
}

// Bypass loads group's key now (joining a load of it already running) and
// stores the result. When the load fails, or the group is held and none is
// made, the copy held is returned with a *StaleError wrapping why; with no
// copy, the error alone.
func Bypass[V any](ctx context.Context, c *Cache, group, key string, load func(context.Context) (V, error)) (V, error) {
	v, err := c.get(ctx, group, key, 0, true, erase(load))
	return typed[V](group, key, v, err)
}

// Invalidate drops group's key. A load of it running still answers its
// callers but stores nothing, so the next read loads anew.
func (c *Cache) Invalidate(group, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if g := c.groups[group]; g != nil {
		delete(g.entries, key)
	}
}

// InvalidatePrefix drops every key of group starting with prefix ("" = all
// of them), as Invalidate does. The group's hold stays.
func (c *Cache) InvalidatePrefix(group, prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	g := c.groups[group]
	if g == nil {
		return
	}
	for key := range g.entries {
		if strings.HasPrefix(key, prefix) {
			delete(g.entries, key)
		}
	}
}

// Wait blocks until no load runs: a test settles a background refresh with
// it before reading what the refresh stored.
func (c *Cache) Wait() { c.loads.Wait() }

func (c *Cache) get(ctx context.Context, name, key string, ttl time.Duration, bypass bool, load loader) (any, error) {
	c.mu.Lock()
	now := c.now()
	c.sweep(now)
	g := c.group(name)
	e := g.entries[key]
	if e != nil && c.unread(e, now) {
		delete(g.entries, key)
		e = nil
	}
	if e != nil {
		e.read = now
	}
	if now.Before(g.until) {
		held := &HeldError{Until: g.until, Err: g.cause}
		if e == nil || !e.has {
			c.mu.Unlock()
			return nil, held
		}
		v, loaded := e.val, e.loaded
		c.mu.Unlock()
		if bypass {
			return v, &StaleError{Loaded: loaded, Err: held}
		}
		return v, nil
	}
	if e != nil && e.has && !bypass {
		if now.Sub(e.loaded) >= ttl && e.flight == nil {
			c.start(ctx, name, g, key, e, load)
		}
		v := e.val
		c.mu.Unlock()
		return v, nil
	}
	if e == nil {
		e = &entry{read: now}
		g.entries[key] = e
	}
	f := e.flight
	if f == nil {
		f = c.start(ctx, name, g, key, e, load)
	}
	c.mu.Unlock()
	select {
	case <-f.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if f.err == nil || !bypass {
		return f.val, f.err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if g.entries[key] != e || !e.has {
		return nil, f.err
	}
	return e.val, &StaleError{Loaded: e.loaded, Err: f.err}
}

// start runs e's load on a goroutine of its own (c.mu held), detached from
// ctx's cancellation so that a caller leaving fails none of the others
// sharing the load, and bounded by LoadTimeout instead.
func (c *Cache) start(ctx context.Context, name string, g *group, key string, e *entry, load loader) *flight {
	f := &flight{done: make(chan struct{})}
	e.flight = f
	c.loads.Add(1)
	go func() {
		defer c.loads.Done()
		defer close(f.done)
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
		defer cancel()
		v, err := run(lctx, load)
		c.finish(name, g, key, e, f, v, err)
	}()
	return f
}

// finish settles f: a value becomes e's copy unless e was invalidated
// meanwhile; an error keeps the copy, reaches the callers with no value,
// and holds the group when Hold says so.
func (c *Cache) finish(name string, g *group, key string, e *entry, f *flight, v any, err error) {
	var d time.Duration
	held := false
	if err != nil {
		d, held = c.hold(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	f.err = err
	e.flight = nil
	current := g.entries[key] == e
	if err == nil {
		f.val = v
		if current {
			e.val, e.has, e.loaded = v, true, now
		}
		return
	}
	if current && !e.has {
		delete(g.entries, key)
	}
	if !held {
		return
	}
	if h, until := c.group(name), now.Add(d); until.After(h.until) {
		h.until, h.cause = until, err
	}
}

// group returns name's group, creating it (c.mu held).
func (c *Cache) group(name string) *group {
	g := c.groups[name]
	if g == nil {
		g = &group{entries: map[string]*entry{}}
		c.groups[name] = g
	}
	return g
}

// unread: e went unread for Idle and is not served again. A load running
// keeps it.
func (c *Cache) unread(e *entry, now time.Time) bool {
	return e.flight == nil && now.Sub(e.read) >= c.idle
}

// sweep drops the unread entries and the groups left with neither entries
// nor a hold (c.mu held). It runs during Get at most once per Idle, so an
// unread entry's memory goes within twice Idle.
func (c *Cache) sweep(now time.Time) {
	if now.Sub(c.swept) < c.idle {
		return
	}
	c.swept = now
	for name, g := range c.groups {
		for key, e := range g.entries {
			if c.unread(e, now) {
				delete(g.entries, key)
			}
		}
		if len(g.entries) == 0 && !now.Before(g.until) {
			delete(c.groups, name)
		}
	}
}

// run calls load, turning a panic into its error: the load runs on a
// goroutine of its own, where a panic would end the daemon rather than the
// one request.
func run(ctx context.Context, load loader) (v any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("livecache: load panicked: %v", r)
		}
	}()
	return load(ctx)
}

func erase[V any](load func(context.Context) (V, error)) loader {
	return func(ctx context.Context) (any, error) {
		v, err := load(ctx)
		return v, err
	}
}

// typed gives v back as a V; a key read as another type than it holds is
// the caller's bug, answered as an error.
func typed[V any](group, key string, v any, err error) (V, error) {
	t, ok := v.(V)
	if !ok && v != nil {
		return t, fmt.Errorf("livecache: %s %s holds a %T", group, key, v)
	}
	return t, err
}
