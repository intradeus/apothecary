// Package idle keeps track of what uses a run's sandbox, and decides which
// sandboxes have sat unused long enough to stop (stop-on-idle,
// docs/sandbox-runtimes-plan.md phase 6).
//
// Use is whatever holds the sandbox: an agent turn, an ask thread's turn, a
// terminal, an SSH session, a connection to an exposed port (the editor is
// one). Each holds it from Busy until its release; Touch marks a moment of
// use. A run is idle once nothing holds it, measured from the last release
// or touch; a run the tracker has never seen (an engine restart) is idle
// from the first time it is asked about, never from before.
package idle

import (
	"context"
	"net"
	"sync"
	"time"

	"apothecary/internal/ports/envport"
)

// Tracker is the per-run use count and the time of the last use.
type Tracker struct {
	mu   sync.Mutex
	now  func() time.Time
	runs map[string]*use
}

type use struct {
	busy int
	last time.Time
}

// New is an empty tracker on the wall clock.
func New() *Tracker { return NewAt(time.Now) }

// NewAt is an empty tracker on the given clock.
func NewAt(now func() time.Time) *Tracker { return &Tracker{now: now, runs: map[string]*use{}} }

func (t *Tracker) get(runID string) *use {
	u := t.runs[runID]
	if u == nil {
		u = &use{last: t.now()}
		t.runs[runID] = u
	}
	return u
}

// Busy holds the run until release, which is safe to call more than once.
func (t *Tracker) Busy(runID string) (release func()) {
	t.mu.Lock()
	u := t.get(runID)
	u.busy++
	u.last = t.now()
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			if u.busy > 0 {
				u.busy--
			}
			u.last = t.now()
			t.mu.Unlock()
		})
	}
}

// Touch marks a moment of use.
func (t *Tracker) Touch(runID string) {
	t.mu.Lock()
	t.get(runID).last = t.now()
	t.mu.Unlock()
}

// IdleFor is how long nothing has used the run: 0 while something holds it.
func (t *Tracker) IdleFor(runID string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	u := t.get(runID)
	if u.busy > 0 {
		return 0
	}
	return t.now().Sub(u.last)
}

// Forget drops a run (its sandbox destroyed, or put to sleep: a woken one
// starts afresh).
func (t *Tracker) Forget(runID string) {
	t.mu.Lock()
	delete(t.runs, runID)
	t.mu.Unlock()
}

// PortConn wraps a run's port: each connection holds the run until it is
// closed.
func (t *Tracker) PortConn(runID string, pc envport.PortConn) envport.PortConn {
	return trackedPort{PortConn: pc, t: t, runID: runID}
}

type trackedPort struct {
	envport.PortConn
	t     *Tracker
	runID string
}

func (p trackedPort) Dial(ctx context.Context) (net.Conn, error) {
	c, err := p.PortConn.Dial(ctx)
	if err != nil {
		return nil, err
	}
	return &heldConn{Conn: c, release: p.t.Busy(p.runID)}, nil
}

type heldConn struct {
	net.Conn
	release func()
}

func (c *heldConn) Close() error {
	c.release()
	return c.Conn.Close()
}

// CloseWrite keeps half-close working through the wrapper.
func (c *heldConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// Candidate is one run with a sandbox, as the sweep sees it.
type Candidate struct {
	RunID string
	// Status is the sandbox's last known status.
	Status envport.EnvStatus
	// LoopLive: the run's loop is running. A live loop's workspace is
	// never stopped under it: the loop ensures it once, and a stage would
	// find it gone.
	LoopLive bool
	// OpInFlight: provisioning, a rebuild, a destroy, a wake.
	OpInFlight bool
	// After is the project's idle period (0: never).
	After time.Duration
}

// Due says whether a candidate's sandbox should be stopped now: running,
// nothing else happening to it, and unused for its whole period.
func Due(c Candidate, idleFor time.Duration) bool {
	return c.After > 0 && c.Status == envport.EnvRunning && !c.LoopLive && !c.OpInFlight && idleFor >= c.After
}
