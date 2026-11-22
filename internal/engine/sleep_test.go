package engine

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
	"apothecary/internal/workspace/envstate"
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
// flight, a provisioner that cannot keep a stopped one (its storage, the
// run's runtime, none that sleeps at all). None of them is a warning: the
// sweep passes over them every minute, and an instance-wide idle default
// reaches sandboxes their provisioner cannot keep.
func TestWhatKeepsAWorkspaceUp(t *testing.T) {
	ctx := context.Background()
	for name, arm := range map[string]func(h *harness, id string){
		"never":      func(h *harness, id string) { h.res.WorkspaceIdleAfter = 0 },
		"in use":     func(h *harness, id string) { h.e.use.Busy(id) },
		"loop live":  func(h *harness, id string) { h.e.mu.Lock(); h.e.handles[id] = &runHandle{}; h.e.mu.Unlock() },
		"op":         func(h *harness, id string) { _, _ = h.e.envState.Begin(id, "rebuilding") },
		"emptydir":   func(h *harness, id string) { h.envp.CannotSleep = errors.New("emptydir") },
		"no runtime": func(h *harness, id string) { h.res.WorkspaceRuntimeErr = errors.New("not approved") },
		"no sleeper": func(h *harness, id string) {
			h.res.Envp = struct{ envport.EnvProvisioner }{h.envp}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, id, c := idleRun(t)
			var logged strings.Builder
			h.e.log = slog.New(slog.NewTextHandler(&logged, nil))
			arm(h, id)
			defer func() { h.e.mu.Lock(); delete(h.e.handles, id); h.e.mu.Unlock() }()
			c.advance(2 * time.Hour)
			for range 2 {
				if slept := h.e.SleepIdle(ctx); len(slept) != 0 || len(h.envp.Slept) != 0 {
					t.Errorf("slept %v", slept)
				}
			}
			if logged.Len() != 0 {
				t.Errorf("the sweep said: %s", logged.String())
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

// onResolve calls hook the first time the engine resolves a run's project:
// the moment between a sweep's look at a run and its claim on it.
type onResolve struct {
	resolution.Resolver
	once sync.Once
	hook func()
}

func (r *onResolve) Resolve(ctx context.Context, projectID string) (*resolution.Resolution, error) {
	r.once.Do(r.hook)
	return r.Resolver.Resolve(ctx, projectID)
}

// A use or a loop that starts after the sweep looked at the run, but before
// it claimed the sleep, is seen under the claim: the workspace stays up, and
// nothing is left claimed or failed.
func TestASweepLeavesAWorkspaceUsedSinceItLooked(t *testing.T) {
	ctx := context.Background()
	for name, use := range map[string]func(h *harness, id string){
		"a use":  func(h *harness, id string) { h.e.use.Busy(id) },
		"a loop": func(h *harness, id string) { h.e.mu.Lock(); h.e.handles[id] = &runHandle{}; h.e.mu.Unlock() },
	} {
		t.Run(name, func(t *testing.T) {
			h, id, c := idleRun(t)
			defer func() { h.e.mu.Lock(); delete(h.e.handles, id); h.e.mu.Unlock() }()
			h.e.resolver = &onResolve{Resolver: h.e.resolver, hook: func() { use(h, id) }}
			c.advance(time.Hour)
			if slept := h.e.SleepIdle(ctx); len(slept) != 0 || len(h.envp.Slept) != 0 {
				t.Errorf("slept %v under %s", slept, name)
			}
			if h.e.envState.InFlight(id) || h.e.envState.Badges()[id] != string(envport.EnvRunning) {
				t.Errorf("left behind: in flight %v, badge %q", h.e.envState.InFlight(id), h.e.envState.Badges()[id])
			}
		})
	}
}

// A person who opens the workspace while a sweep is stopping it meets it
// started again, not the dying pod: the use waits out the sleep, then wakes
// it, and holds it until released.
func TestAUseDuringASleepWakesTheWorkspaceAfterIt(t *testing.T) {
	h, id, c := idleRun(t)
	ctx := context.Background()
	type used struct {
		release func()
		err     error
	}
	done := make(chan used, 1)
	h.envp.SleepHook = func(envport.EnvID) {
		h.envp.SleepHook = nil
		go func() {
			release, err := h.e.UseEnv(ctx, id, WokeByTerminal)
			done <- used{release, err}
		}()
	}
	c.advance(time.Hour)
	if slept := h.e.SleepIdle(ctx); len(slept) != 1 {
		t.Fatalf("slept %v", slept)
	}
	u := <-done
	if u.err != nil {
		t.Fatal(u.err)
	}
	if len(h.envp.Woken) != 1 || h.e.envState.Statuses()[id] != envport.EnvRunning {
		t.Fatalf("woken %v, status %s", h.envp.Woken, h.e.envState.Statuses()[id])
	}
	c.advance(time.Hour)
	if slept := h.e.SleepIdle(ctx); len(slept) != 0 {
		t.Errorf("slept while held: %v", slept)
	}
	u.release()
	c.advance(time.Hour)
	if slept := h.e.SleepIdle(ctx); len(slept) != 1 {
		t.Errorf("released and idle, slept %v", slept)
	}
}

// A stage whose loop starts while a sweep is stopping the workspace reads
// its status after the sleep, and wakes it, rather than entering the pod
// being deleted.
func TestAStageStartingDuringASleepWakesTheWorkspace(t *testing.T) {
	h, id, c := idleRun(t)
	ctx := context.Background()
	v := h.view(t, id)
	fl, err := h.e.RunFlow(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	h.envp.SleepHook = func(envport.EnvID) {
		h.envp.SleepHook = nil
		h.e.mu.Lock()
		h.e.handles[id] = &runHandle{}
		h.e.mu.Unlock()
		go func() {
			x := &exec{e: h.e, rs: &runcore.State{RunID: id, View: v, Flow: fl, EnvID: envport.EnvID(v.EnvID), EnvRequested: true}, res: h.res, ctx: ctx, bg: ctx}
			done <- x.ensureEnv()
		}()
	}
	defer func() { h.e.mu.Lock(); delete(h.e.handles, id); h.e.mu.Unlock() }()
	c.advance(time.Hour)
	if slept := h.e.SleepIdle(ctx); len(slept) != 1 {
		t.Fatalf("slept %v", slept)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(h.envp.Woken) != 1 || h.view(t, id).EnvID != v.EnvID {
		t.Errorf("woken %v, env %s → %s", h.envp.Woken, v.EnvID, h.view(t, id).EnvID)
	}
}

// A second use while a wake is in flight waits for it and finds the
// workspace up: one wake, no "try again".
func TestASecondUseWaitsForTheWakeInFlight(t *testing.T) {
	h, id, c := idleRun(t)
	ctx := context.Background()
	c.advance(time.Hour)
	if slept := h.e.SleepIdle(ctx); len(slept) != 1 {
		t.Fatalf("slept %v", slept)
	}
	second := make(chan error, 1)
	h.envp.WakeHook = func(envport.EnvID) {
		h.envp.WakeHook = nil
		go func() { second <- h.e.WakeEnv(ctx, id, WokeByEditor) }()
	}
	if err := h.e.WakeEnv(ctx, id, WokeByTerminal); err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatalf("the second use = %v", err)
	}
	if len(h.envp.Woken) != 1 {
		t.Errorf("woken %v", h.envp.Woken)
	}
	// Bounded by the caller: a use that gives up while a sleep is in
	// flight is its ctx's cause, and holds nothing.
	rel, _ := h.e.envState.Begin(id, opSleeping)
	defer rel(nil)
	cctx, cancel := context.WithCancelCause(ctx)
	cancel(errors.New("the person left"))
	c.advance(time.Minute)
	if _, err := h.e.UseEnv(cctx, id, WokeByTerminal); err == nil || !strings.Contains(err.Error(), "the person left") {
		t.Errorf("use under a sleep, cancelled = %v", err)
	}
	c.advance(time.Minute)
	if h.e.use.IdleFor(id) == 0 {
		t.Error("a failed use left its hold")
	}
}

// Another op on a sleeping workspace (a rebuild, a destroy) is not waited
// for: a wake says so, and a stage's wake too.
func TestAWakeUnderAnotherOpIsRefused(t *testing.T) {
	h, id, c := idleRun(t)
	ctx := context.Background()
	c.advance(time.Hour)
	h.e.SleepIdle(ctx)
	rel, err := h.e.envState.Begin(id, "destroying")
	if err != nil {
		t.Fatal(err)
	}
	defer rel(nil)
	if err := h.e.WakeEnv(ctx, id, WokeByPerson); !errors.Is(err, envstate.ErrInFlight) {
		t.Errorf("wake under a destroy = %v", err)
	}
	v := h.view(t, id)
	x := &exec{e: h.e, rs: &runcore.State{RunID: id, View: v, EnvID: envport.EnvID(v.EnvID)}, res: h.res, ctx: ctx, bg: ctx}
	if err := x.wakeInLoop(); !errors.Is(err, envstate.ErrInFlight) {
		t.Errorf("a stage's wake under a destroy = %v", err)
	}
	if len(h.envp.Woken) != 0 {
		t.Errorf("woken %v", h.envp.Woken)
	}
}

// A stage that found its workspace asleep, but holds the claim only after
// someone else woke it, provisions nothing more.
func TestAStageWakeOfAWokenWorkspaceDoesNothing(t *testing.T) {
	h, id, _ := idleRun(t)
	ctx := context.Background()
	v := h.view(t, id)
	x := &exec{e: h.e, rs: &runcore.State{RunID: id, View: v, EnvID: envport.EnvID(v.EnvID)}, res: h.res, ctx: ctx, bg: ctx}
	if err := x.wakeInLoop(); err != nil {
		t.Fatal(err)
	}
	if len(h.envp.Woken) != 0 || h.envp.Env("env-2") != nil {
		t.Errorf("woken %v, a second workspace %v", h.envp.Woken, h.envp.Env("env-2"))
	}
}

// An op claimed after the sweep looked (a rebuild) wins: the sweep lets
// the run be, quietly, and the op is the rebuild's.
func TestASweepYieldsToAnOpClaimedSinceItLooked(t *testing.T) {
	h, id, c := idleRun(t)
	ctx := context.Background()
	var rel func(error)
	h.e.resolver = &onResolve{Resolver: h.e.resolver, hook: func() { rel, _ = h.e.envState.Begin(id, "rebuilding") }}
	c.advance(time.Hour)
	if slept := h.e.SleepIdle(ctx); len(slept) != 0 || len(h.envp.Slept) != 0 {
		t.Errorf("slept %v", slept)
	}
	if h.e.envState.Op(id) != "rebuilding" {
		t.Errorf("op = %q", h.e.envState.Op(id))
	}
	rel(nil)
}

// statusOnce answers the first Status with st, then asks the provisioner.
type statusOnce struct {
	*fake.EnvProvisioner
	once sync.Once
	st   envport.EnvStatus
}

func (s *statusOnce) Status(ctx context.Context, id envport.EnvID) (envport.EnvStatus, error) {
	first := false
	s.once.Do(func() { first = true })
	if first {
		return s.st, nil
	}
	return s.EnvProvisioner.Status(ctx, id)
}

// A wake that holds its claim only after another one finished finds the
// workspace up and provisions nothing.
func TestAWakeOfAWokenWorkspaceDoesNothing(t *testing.T) {
	h, id, _ := idleRun(t)
	h.e.envState.SetStatus(id, envport.EnvAsleep) // as last seen
	h.res.Envp = &statusOnce{EnvProvisioner: h.envp, st: envport.EnvAsleep}
	if err := h.e.WakeEnv(context.Background(), id, WokeByPerson); err != nil {
		t.Fatal(err)
	}
	if len(h.envp.Woken) != 0 || h.envp.Env("env-2") != nil {
		t.Errorf("woken %v, a second workspace %v", h.envp.Woken, h.envp.Env("env-2"))
	}
}

// A stage's wake waiting out a sleep gives up with its run's ctx.
func TestAStageWakeGivesUpWithItsRun(t *testing.T) {
	h, id, _ := idleRun(t)
	rel, _ := h.e.envState.Begin(id, opSleeping)
	defer rel(nil)
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("paused")
	cancel(cause)
	v := h.view(t, id)
	x := &exec{e: h.e, rs: &runcore.State{RunID: id, View: v, EnvID: envport.EnvID(v.EnvID)}, res: h.res, ctx: ctx, bg: ctx}
	if err := x.wakeInLoop(); !errors.Is(err, cause) {
		t.Errorf("wake = %v", err)
	}
	if err := x.ensureEnv(); !errors.Is(err, cause) {
		t.Errorf("ensureEnv = %v", err)
	}
}

// The sweep runs on its period until its ctx ends; Use is the tracker the
// surfaces report to.
func TestRunIdleSweep(t *testing.T) {
	h, id, c := idleRun(t)
	if h.e.Use() != h.e.use {
		t.Error("Use is not the engine's tracker")
	}
	c.advance(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.e.RunIdleSweep(ctx, time.Millisecond); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for h.e.envState.Statuses()[id] != envport.EnvAsleep && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if h.e.envState.Statuses()[id] != envport.EnvAsleep {
		t.Error("the sweep never slept the idle workspace")
	}
}

// notSleeper is a provisioner without stop-on-idle.
type notSleeper struct{ envport.EnvProvisioner }

// What the sweep and a wake cannot read is left alone, or reported: a run
// gone from the log, a project that does not resolve, a provisioner that
// cannot sleep.
func TestSleepAndWakeOfWhatCannotBeRead(t *testing.T) {
	ctx := context.Background()
	h, id, c := idleRun(t)
	h.e.envState.SetStatus("ghost", envport.EnvRunning)
	h.e.use.Touch("ghost")
	c.advance(time.Hour)
	h.e.resolver = failingResolver{err: errors.New("project gone")}
	if slept := h.e.SleepIdle(ctx); len(slept) != 0 {
		t.Errorf("slept %v", slept)
	}
	h.e.envState.SetStatus(id, envport.EnvAsleep)
	if err := h.e.WakeEnv(ctx, id, WokeByPerson); err == nil || !strings.Contains(err.Error(), "project gone") {
		t.Errorf("wake, unresolved = %v", err)
	}
	h.e.envState.SetStatus("ghost", envport.EnvAsleep)
	if err := h.e.WakeEnv(ctx, "ghost", WokeByPerson); err == nil {
		t.Error("woke a run that is not in the log")
	}
	h.e.resolver = resolution.StaticResolver{R: h.res}
	h.res.Envp = notSleeper{h.envp}
	h.e.envState.SetStatus(id, envport.EnvRunning)
	if _, err := canSleep(h.res); !errors.Is(err, errCannotSleep) {
		t.Errorf("sleep on a provisioner without it = %v", err)
	}
}

// recordingHook records what a sleep tells a teardown hook.
type recordingHook struct {
	mu            sync.Mutex
	forgot, slept []string
}

func (s *recordingHook) Forget(runID string) {
	s.mu.Lock()
	s.forgot = append(s.forgot, runID)
	s.mu.Unlock()
}
func (*recordingHook) BeforeDestroy(context.Context, run.View, *resolution.Resolution) {}

type keepingHook struct{ *recordingHook }

func (k keepingHook) Slept(runID string) {
	k.mu.Lock()
	k.slept = append(k.slept, runID)
	k.mu.Unlock()
}

// A sleep tells the hooks that keep something across it Slept, and the
// others Forget.
func TestASleepTellsItsHooks(t *testing.T) {
	h, id, c := idleRun(t)
	ctx := context.Background()
	plain, keeping := &recordingHook{}, keepingHook{&recordingHook{}}
	h.e.teardown = []EnvTeardown{plain, keeping}
	c.advance(time.Hour)
	if slept := h.e.SleepIdle(ctx); len(slept) != 1 {
		t.Fatalf("slept %v", slept)
	}
	if !slices.Equal(plain.forgot, []string{id}) || len(keeping.forgot) != 0 || !slices.Equal(keeping.slept, []string{id}) {
		t.Errorf("plain forgot %v; keeping forgot %v, slept %v", plain.forgot, keeping.forgot, keeping.slept)
	}
}
