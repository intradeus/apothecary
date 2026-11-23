package idle

import (
	"context"
	"net"
	"testing"
	"time"

	"apothecary/internal/ports/envport"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func tracker(c *clock) *Tracker          { t := New(); t.now = c.now; return t }
func newClock() *clock                   { return &clock{t: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)} }

// A run is idle from the last time something let it go, never while
// something holds it, and from the first question when never seen.
func TestTrackerIdleFor(t *testing.T) {
	c := newClock()
	tr := tracker(c)
	if d := tr.IdleFor("r"); d != 0 {
		t.Errorf("never seen: idle %s, want counted from now", d)
	}
	c.advance(10 * time.Minute)
	if d := tr.IdleFor("r"); d != 10*time.Minute {
		t.Errorf("idle %s", d)
	}
	release := tr.Busy("r")
	other := tr.Busy("r")
	c.advance(time.Hour)
	if d := tr.IdleFor("r"); d != 0 {
		t.Errorf("held: idle %s", d)
	}
	release()
	release() // twice is once
	if d := tr.IdleFor("r"); d != 0 {
		t.Errorf("still held by the other: idle %s", d)
	}
	other()
	c.advance(5 * time.Minute)
	if d := tr.IdleFor("r"); d != 5*time.Minute {
		t.Errorf("after release: idle %s", d)
	}
	tr.Touch("r")
	if d := tr.IdleFor("r"); d != 0 {
		t.Errorf("touched: idle %s", d)
	}
	c.advance(time.Minute)
	tr.Forget("r")
	if d := tr.IdleFor("r"); d != 0 {
		t.Errorf("forgotten: starts afresh, idle %s", d)
	}
}

type pipePort struct{}

func (pipePort) Dial(context.Context) (net.Conn, error) { a, _ := net.Pipe(); return a, nil }
func (pipePort) Close() error                           { return nil }

type failPort struct{ pipePort }

func (failPort) Dial(context.Context) (net.Conn, error) { return nil, net.ErrClosed }

// A connection to a run's port holds the run until it closes.
func TestPortConnHoldsTheRun(t *testing.T) {
	c := newClock()
	tr := tracker(c)
	conn, err := tr.PortConn("r", pipePort{}).Dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c.advance(time.Hour)
	if tr.IdleFor("r") != 0 {
		t.Error("an open connection is use")
	}
	if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	c.advance(time.Minute)
	if d := tr.IdleFor("r"); d != time.Minute {
		t.Errorf("closed: idle %s", d)
	}
	if _, err := tr.PortConn("r", failPort{}).Dial(context.Background()); err == nil {
		t.Error("a failed dial is the dial's error")
	}
	if tr.IdleFor("r") != time.Minute {
		t.Error("a failed dial holds nothing")
	}
}

func TestDue(t *testing.T) {
	base := Candidate{RunID: "r", Status: envport.EnvRunning, After: 30 * time.Minute}
	if !Due(base, 30*time.Minute) {
		t.Error("idle for its whole period: due")
	}
	for name, c := range map[string]Candidate{
		"never":       {RunID: "r", Status: envport.EnvRunning},
		"loop live":   {RunID: "r", Status: envport.EnvRunning, After: time.Minute, LoopLive: true},
		"op":          {RunID: "r", Status: envport.EnvRunning, After: time.Minute, OpInFlight: true},
		"asleep":      {RunID: "r", Status: envport.EnvAsleep, After: time.Minute},
		"not running": {RunID: "r", Status: envport.EnvStopped, After: time.Minute},
	} {
		if Due(c, time.Hour) {
			t.Errorf("%s: due", name)
		}
	}
	if Due(base, 29*time.Minute) {
		t.Error("short of its period: not due")
	}
}
