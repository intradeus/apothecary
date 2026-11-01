package envstate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

func TestBadgesOverlayAndTTL(t *testing.T) {
	clk := time.Unix(1000, 0)
	s := New(nil, nil, func() time.Time { return clk }, nil)
	s.SetStatus("r1", envport.EnvStopped)

	// An in-flight op overlays the reconciled docker status.
	rel, err := s.Begin("r1", "rebuilding")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Badges()["r1"]; got != "rebuilding" {
		t.Errorf("badge = %q, want rebuilding", got)
	}
	// Failing the op leaves an "error" badge...
	rel(errors.New("boom"))
	if got := s.Badges()["r1"]; got != "error" {
		t.Errorf("badge = %q, want error", got)
	}
	// ...which ages out after the TTL, falling back to the docker status.
	clk = clk.Add(ErrorTTL + time.Second)
	if got := s.Badges()["r1"]; got != "stopped" {
		t.Errorf("badge after TTL = %q, want stopped (fallback)", got)
	}
}

const fixtureProject = "p1"

// countingResolver counts Resolve calls and closes, and can fail by project.
type countingResolver struct {
	res     *resolution.Resolution
	calls   int
	closed  *int
	failFor map[string]error
}

func (c *countingResolver) Resolve(_ context.Context, projectID string) (*resolution.Resolution, error) {
	c.calls++
	if err := c.failFor[projectID]; err != nil {
		return nil, err
	}
	r := *c.res
	r.ProjectID = projectID
	r.Release = func() { *c.closed++ }
	return &r, nil
}

// flakyEnvp makes Status and ConfigDirty fail for named envs.
type flakyEnvp struct {
	*fake.EnvProvisioner
	statusErr map[envport.EnvID]error
	dirtyErr  map[envport.EnvID]error
}

func (f flakyEnvp) Status(ctx context.Context, id envport.EnvID) (envport.EnvStatus, error) {
	if err := f.statusErr[id]; err != nil {
		return "", err
	}
	return f.EnvProvisioner.Status(ctx, id)
}

func (f flakyEnvp) ConfigDirty(ctx context.Context, id envport.EnvID) (bool, error) {
	if err := f.dirtyErr[id]; err != nil {
		return false, err
	}
	return f.EnvProvisioner.ConfigDirty(ctx, id)
}

type fixture struct {
	store    *event.MemStore
	envp     *fake.EnvProvisioner
	resolver *countingResolver
	s        *Store
	closed   int
	now      time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := &fixture{store: event.NewMemStore(), envp: fake.NewEnvProvisioner(), now: time.Unix(1_000, 0)}
	h.envp.Dirty = map[envport.EnvID]bool{}
	h.resolver = &countingResolver{res: &resolution.Resolution{Envp: h.envp}, closed: &h.closed, failFor: map[string]error{}}
	h.s = New(&run.Reader{Store: h.store}, h.resolver, func() time.Time { return h.now }, slog.New(slog.DiscardHandler))
	return h
}

// addRun creates a run with a live env in project, returning the env id.
func (h *fixture) addRun(t *testing.T, runID, project string) envport.EnvID {
	t.Helper()
	ctx := context.Background()
	id, err := h.envp.Provision(ctx, envport.EnvSpec{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "t", FlowName: "loop", ProjectID: project},
		&event.EnvRequested{}, &event.EnvReady{EnvID: string(id)},
	} {
		if _, err := h.store.Append(ctx, runID, p); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestReconcileRecordsLiveStatusAndDirty(t *testing.T) {
	h := newFixture(t)
	a := h.addRun(t, "a", fixtureProject)
	b := h.addRun(t, "b", fixtureProject)
	h.envp.Dirty[b] = true
	h.envp.Env(a).Status = envport.EnvStopped

	if err := h.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := h.s.Statuses()
	if st["a"] != envport.EnvStopped || st["b"] != envport.EnvRunning {
		t.Fatalf("statuses = %v", st)
	}
	if d := h.s.Dirty(); d["a"] || !d["b"] {
		t.Fatalf("dirty = %v", d)
	}
	// Many runs of one project share a resolution within a pass.
	if h.resolver.calls != 1 {
		t.Errorf("resolved %d times, want once per project", h.resolver.calls)
	}
	if h.closed != 1 {
		t.Errorf("closed %d resolutions, want 1", h.closed)
	}
}

// The whole point of Unknown: a failed probe must never carry a stale "running".
func TestReconcileFailedProbesAreUnknownNotStale(t *testing.T) {
	h := newFixture(t)
	envA := h.addRun(t, "a", fixtureProject)
	h.addRun(t, "b", "p2")
	h.s.SetStatus("a", envport.EnvRunning)
	h.s.SetStatus("b", envport.EnvRunning)

	flaky := flakyEnvp{EnvProvisioner: h.envp, statusErr: map[envport.EnvID]error{envA: errors.New("expired credentials")}}
	h.resolver.res.Envp = flaky
	h.resolver.failFor["p2"] = errors.New("project deleted")

	if err := h.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := h.s.Statuses()
	if st["a"] != envport.EnvUnknown || st["b"] != envport.EnvUnknown {
		t.Fatalf("statuses = %v, want unknown for both", st)
	}
}

func TestReconcileDropsADirtyHintItCannotConfirm(t *testing.T) {
	h := newFixture(t)
	env := h.addRun(t, "a", fixtureProject)
	h.envp.Dirty[env] = true
	if err := h.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !h.s.Dirty()["a"] {
		t.Fatal("control: dirty not recorded")
	}
	h.resolver.res.Envp = flakyEnvp{EnvProvisioner: h.envp, dirtyErr: map[envport.EnvID]error{env: errors.New("no")}}
	if err := h.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.s.Dirty()["a"]; ok {
		t.Fatal("a failed check must drop the hint, not carry it forward")
	}
}

func TestReconcileSkipsRunsItCannotOrMustNotProbe(t *testing.T) {
	h := newFixture(t)
	h.addRun(t, "busy", fixtureProject)
	h.addRun(t, "failed-op", fixtureProject)
	ctx := context.Background()
	// No env yet, and no project: nothing to report on.
	if _, err := h.store.Append(ctx, "no-env", &event.RunCreated{TicketTitle: "t", FlowName: "loop", ProjectID: fixtureProject}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Append(ctx, "no-project", &event.RunCreated{TicketTitle: "t", FlowName: "loop"}); err != nil {
		t.Fatal(err)
	}
	rel, err := h.s.Begin("busy", "rebuilding")
	if err != nil {
		t.Fatal(err)
	}
	defer rel(nil)
	relF, err := h.s.Begin("failed-op", "rebuilding")
	if err != nil {
		t.Fatal(err)
	}
	relF(errors.New("failed")) // a finished op is no reason not to look

	if err := h.s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	st := h.s.Statuses()
	if _, ok := st["busy"]; ok {
		t.Errorf("a run with an op in flight was probed: %v", st)
	}
	if st["failed-op"] != envport.EnvRunning {
		t.Errorf("statuses = %v", st)
	}
	if len(st) != 1 {
		t.Errorf("statuses = %v, want only failed-op", st)
	}
}

// A run whose env went away drops out, rather than keeping its old status.
func TestReconcileRebuildsTheWholeMap(t *testing.T) {
	h := newFixture(t)
	h.s.SetStatus("long-gone", envport.EnvRunning)
	if err := h.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := h.s.Statuses(); len(st) != 0 {
		t.Fatalf("statuses = %v", st)
	}
}

type failingStore struct {
	event.Store
	err error
}

func (f failingStore) RunIDs(context.Context) ([]string, error) { return nil, f.err }

func TestReconcileReportsAStoreFailureAndKeepsTheOldMap(t *testing.T) {
	h := newFixture(t)
	h.s.SetStatus("a", envport.EnvRunning)
	boom := errors.New("db down")
	h.s.Runs = &run.Reader{Store: failingStore{h.store, boom}}
	if err := h.s.Reconcile(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Reconcile = %v", err)
	}
	if h.s.Statuses()["a"] != envport.EnvRunning {
		t.Fatal("a pass that never ran must not touch the map")
	}
}

func TestRunReconcilesAtOnceThenUntilCancelled(t *testing.T) {
	h := newFixture(t)
	h.addRun(t, "a", fixtureProject)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.s.Run(ctx, time.Millisecond)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for h.s.Statuses()["a"] != envport.EnvRunning {
		if time.Now().After(deadline) {
			t.Fatal("Run never reconciled")
		}
		time.Sleep(time.Millisecond)
	}
	// A failing pass does not stop the loop: the next tick tries again.
	h.s.SetStatus("a", envport.EnvUnknown)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
}

func TestRunSurvivesAFailedPass(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	h := newFixture(t)
	h.s.Log = slog.New(slog.NewTextHandler(lockedWriter{&mu, &buf}, nil))
	h.s.Runs = &run.Reader{Store: failingStore{h.store, errors.New("db down")}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.s.Run(ctx, time.Millisecond)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := strings.Count(buf.String(), "env reconcile pass failed")
		mu.Unlock()
		if n >= 2 { // logged, and tried again
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the loop stopped after a failed pass")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestSetStatusAndClearDirty(t *testing.T) {
	h := newFixture(t)
	env := h.addRun(t, "a", fixtureProject)
	h.envp.Dirty[env] = true
	if err := h.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.s.ClearDirty("a")
	if h.s.Dirty()["a"] {
		t.Fatal("ClearDirty left the hint")
	}
	h.s.SetStatus("a", envport.EnvStopped)
	if h.s.Statuses()["a"] != envport.EnvStopped {
		t.Fatal("SetStatus lost")
	}
	// Snapshots are copies: a reader cannot corrupt the store.
	snap := h.s.Statuses()
	snap["a"] = envport.EnvRunning
	if h.s.Statuses()["a"] != envport.EnvStopped {
		t.Fatal("Statuses returned the live map")
	}
	d := h.s.Dirty()
	d["x"] = true
	if h.s.Dirty()["x"] {
		t.Fatal("Dirty returned the live map")
	}
}

func TestBeginGatesOneOpPerRun(t *testing.T) {
	h := newFixture(t)
	rel, err := h.s.Begin("a", "rebuilding")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Begin("a", "destroying"); !errors.Is(err, ErrInFlight) {
		t.Fatalf("second Begin = %v", err)
	}
	if _, err := h.s.Begin("b", "destroying"); err != nil {
		t.Fatalf("another run must not be blocked: %v", err)
	}

	rel(nil)
	rel(errors.New("late")) // idempotent: the second release is ignored
	if got := h.s.Badges()["a"]; got != "" {
		t.Fatalf("badge after success = %q", got)
	}

	rel2, err := h.s.Begin("a", "rebuilding")
	if err != nil {
		t.Fatalf("Begin after release: %v", err)
	}
	rel2(errors.New("boom"))
	if got := h.s.Badges()["a"]; got != "error" {
		t.Fatalf("badge after failure = %q", got)
	}
	// A failed entry does not block the retry.
	if _, err := h.s.Begin("a", "rebuilding"); err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
}

func TestBadgesOverlayEveryKind(t *testing.T) {
	h := newFixture(t)
	h.s.SetStatus("plain", envport.EnvRunning)
	h.s.SetStatus("busy", envport.EnvRunning)
	h.s.SetStatus("failed", envport.EnvRunning)
	if _, err := h.s.Begin("busy", "destroying"); err != nil {
		t.Fatal(err)
	}
	rel, err := h.s.Begin("failed", "rebuilding")
	if err != nil {
		t.Fatal(err)
	}
	rel(errors.New("boom"))

	b := h.s.Badges()
	if b["plain"] != "running" || b["busy"] != "destroying" || b["failed"] != "error" {
		t.Fatalf("badges = %v", b)
	}

	// At exactly the TTL the error still shows; past it, the status returns.
	h.now = h.now.Add(ErrorTTL)
	if got := h.s.Badges()["failed"]; got != "error" {
		t.Fatalf("badge at the TTL = %q", got)
	}
	h.now = h.now.Add(time.Second)
	b = h.s.Badges()
	if b["failed"] != "running" {
		t.Fatalf("badge past the TTL = %q", b["failed"])
	}
	if b["busy"] != "destroying" {
		t.Fatal("an in-flight op must not age out")
	}
}

func TestNilClockAndLogFallBack(t *testing.T) {
	s := &Store{}
	if s.now().IsZero() {
		t.Error("nil clock must fall back to the wall clock")
	}
	if s.log() == nil {
		t.Error("nil log must fall back")
	}
}
