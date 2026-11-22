package engine

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/ports/envport"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
	"apothecary/internal/workspace/idle"
)

// sleepClock is a settable clock for the idle tracker.
type sleepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *sleepClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *sleepClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// idleRun is a completed run with a running workspace, on a tracker whose
// clock the test moves, under an idle period of 30 minutes.
func idleRun(t *testing.T) (*harness, string, *sleepClock) {
	t.Helper()
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	id := completedWithEnv(t, h)
	c := &sleepClock{t: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	h.e.use = idle.NewAt(c.now)
	h.res.WorkspaceIdleAfter = 30 * time.Minute
	h.e.envState.SetStatus(id, envport.EnvRunning)
	h.e.use.Touch(id)
	return h, id, c
}

// An unused workspace sleeps after its period, its run's facts untouched, and
// a person's wake starts it again on the same workspace.
func TestAnIdleWorkspaceSleepsAndWakes(t *testing.T) {
	h, id, c := idleRun(t)
	ctx := context.Background()
	envID := envport.EnvID(h.view(t, id).EnvID)
	facts := h.view(t, id).Facts

	c.advance(29 * time.Minute)
	if slept := h.e.SleepIdle(ctx); len(slept) != 0 {
		t.Fatalf("slept %v before its period", slept)
	}
	c.advance(time.Minute)
	if slept := h.e.SleepIdle(ctx); !slices.Equal(slept, []string{id}) {
		t.Fatalf("slept %v, want %s", slept, id)
	}
	if !slices.Equal(h.envp.Slept, []envport.EnvID{envID}) {
		t.Errorf("provisioner slept %v", h.envp.Slept)
	}
	if st := h.e.envState.Statuses()[id]; st != envport.EnvAsleep {
		t.Errorf("badge status %s", st)
	}
	if evs, _ := h.store.Events(ctx, id, 0); !hasEvent(evs, event.TypeEnvSlept) {
		t.Error("no env_slept")
	}
	if got := h.view(t, id).Facts; !reflect.DeepEqual(got, facts) {
		t.Errorf("sleeping moved the run's facts: %+v → %+v", facts, got)
	}
	// Asleep: the next sweep leaves it.
	c.advance(time.Hour)
	if slept := h.e.SleepIdle(ctx); len(slept) != 0 {
		t.Errorf("slept again: %v", slept)
	}

	if err := h.e.WakeEnv(ctx, id, WokeByPerson); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.envp.Woken, []envport.EnvID{envID}) {
		t.Errorf("woken %v", h.envp.Woken)
	}
	if st := h.e.envState.Statuses()[id]; st != envport.EnvRunning {
		t.Errorf("after the wake: %s", st)
	}
	if h.view(t, id).EnvID != string(envID) {
		t.Error("the wake replaced the workspace")
	}
	var woke *event.EnvWoke
	evs, _ := h.store.Events(ctx, id, 0)
	for _, e := range evs {
		if p, ok := e.Payload.(*event.EnvWoke); ok {
			woke = p
		}
	}
	if woke == nil || woke.By != WokeByPerson || woke.Error != "" {
		t.Errorf("env_woke = %+v", woke)
	}
	// Awake and just used: not idle.
	if slept := h.e.SleepIdle(ctx); len(slept) != 0 {
		t.Errorf("slept right after waking: %v", slept)
	}
	// A wake of an awake workspace does nothing.
	if err := h.e.WakeEnv(ctx, id, WokeByTerminal); err != nil || len(h.envp.Woken) != 1 {
		t.Errorf("second wake: %v, woken %v", err, h.envp.Woken)
	}
}

// What keeps a workspace up: a period of never, use, a live loop, an op in
// flight, a provisioner that cannot keep a stopped one.
func TestWhatKeepsAWorkspaceUp(t *testing.T) {
	ctx := context.Background()
	for name, arm := range map[string]func(h *harness, id string){
		"never":     func(h *harness, id string) { h.res.WorkspaceIdleAfter = 0 },
		"in use":    func(h *harness, id string) { h.e.use.Busy(id) },
		"loop live": func(h *harness, id string) { h.e.mu.Lock(); h.e.handles[id] = &runHandle{}; h.e.mu.Unlock() },
		"op":        func(h *harness, id string) { _, _ = h.e.envState.Begin(id, "rebuilding") },
		"emptydir":  func(h *harness, id string) { h.envp.CannotSleep = errors.New("emptydir") },
	} {
		t.Run(name, func(t *testing.T) {
			h, id, c := idleRun(t)
			arm(h, id)
			defer func() { h.e.mu.Lock(); delete(h.e.handles, id); h.e.mu.Unlock() }()
			c.advance(2 * time.Hour)
			if slept := h.e.SleepIdle(ctx); len(slept) != 0 || len(h.envp.Slept) != 0 {
				t.Errorf("slept %v", slept)
			}
		})
	}
}

// A stop that fails is retried by the next sweep; a wake that fails is on
// the record and the workspace stays asleep.
func TestSleepAndWakeFailures(t *testing.T) {
	h, id, c := idleRun(t)
	ctx := context.Background()
	h.envp.SleepErr = errors.New("api down")
	c.advance(time.Hour)
	if slept := h.e.SleepIdle(ctx); len(slept) != 0 {
		t.Fatalf("slept %v", slept)
	}
	h.envp.SleepErr = nil
	if slept := h.e.SleepIdle(ctx); len(slept) != 1 {
		t.Fatalf("the retry slept %v", slept)
	}
	h.envp.WakeErr = errors.New("no node")
	if err := h.e.WakeEnv(ctx, id, WokeByEditor); err == nil || !strings.Contains(err.Error(), "no node") {
		t.Fatalf("wake = %v", err)
	}
	evs, _ := h.store.Events(ctx, id, 0)
	if p, ok := evs[len(evs)-1].Payload.(*event.EnvWoke); !ok || p.Error == "" || p.By != WokeByEditor {
		t.Errorf("last event = %+v", evs[len(evs)-1].Payload)
	}
}

// A stage's wake (ensureEnv) waits out a sleep still in flight, then starts
// the workspace on the run's spec.
func TestAStageWakesAfterASleepInFlight(t *testing.T) {
	h, id, c := idleRun(t)
	ctx := context.Background()
	c.advance(time.Hour)
	h.e.SleepIdle(ctx)
	v := h.view(t, id)
	rel, err := h.e.envState.Begin(id, "sleeping")
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(50 * time.Millisecond); rel(nil) }()
	x := &exec{e: h.e, rs: &runcore.State{RunID: id, View: v, EnvID: envport.EnvID(v.EnvID)}, res: h.res, ctx: ctx, bg: ctx}
	if err := x.wakeInLoop(); err != nil {
		t.Fatal(err)
	}
	if len(h.envp.Woken) != 1 || h.e.envState.InFlight(id) {
		t.Errorf("woken %v, op in flight %v", h.envp.Woken, h.e.envState.InFlight(id))
	}
}
