package resources

import (
	"context"
	"errors"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

// samplerEnvp adds the OPTIONAL ResourceSampler capability to the fake.
type samplerEnvp struct {
	*fake.EnvProvisioner
	cpu  float64
	fail bool
}

func (s samplerEnvp) SampleResources(context.Context, envport.EnvID) (envport.ResourceSample, error) {
	if s.fail {
		return envport.ResourceSample{}, errors.New("docker stats: container churning")
	}
	return envport.ResourceSample{CPUPerc: s.cpu, MemUsed: 100, MemLimit: 200}, nil
}

const fixtureProject = "p1"

// fixture is a store, a fake provisioner, and a sampler over a resolution
// the test can swap the provisioner of.
type fixture struct {
	store *event.MemStore
	envp  *fake.EnvProvisioner
	res   *resolution.Resolution
	s     *Sampler
}

func newFixture() *fixture {
	h := &fixture{store: event.NewMemStore(), envp: fake.NewEnvProvisioner()}
	h.res = &resolution.Resolution{ProjectID: fixtureProject, Envp: h.envp}
	h.s = New(&run.Reader{Store: h.store}, resolution.StaticResolver{R: h.res}, nil, nil)
	return h
}

func TestSampleOnce(t *testing.T) {
	h := newFixture()
	ctx := context.Background()

	// An active (Running, non-terminal) run with a live env.
	envID, _ := h.envp.Provision(ctx, envport.EnvSpec{RunID: "r-live"})
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "live", FlowName: "loop", ProjectID: fixtureProject},
		&event.EnvRequested{}, &event.EnvReady{EnvID: string(envID)},
	} {
		if _, err := h.store.Append(ctx, "r-live", p); err != nil {
			t.Fatal(err)
		}
	}
	h.res.Envp = samplerEnvp{EnvProvisioner: h.envp, cpu: 42}

	if got := h.s.Samples("r-live"); len(got) != 0 {
		t.Fatalf("samples before any pass: %v", got)
	}
	for pass := 1; pass <= 2; pass++ {
		if err := h.s.SampleOnce(ctx); err != nil {
			t.Fatal(err)
		}
		got := h.s.Samples("r-live")
		if len(got) != pass {
			t.Fatalf("after pass %d: %d samples, want %d", pass, len(got), pass)
		}
		if got[pass-1].CPUPerc != 42 || got[pass-1].At.IsZero() {
			t.Fatalf("sample not recorded/stamped: %+v", got[pass-1])
		}
	}

	// A failed probe is a GAP: nothing appended, history kept — never a
	// carried-forward value.
	h.res.Envp = samplerEnvp{EnvProvisioner: h.envp, fail: true}
	if err := h.s.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.s.Samples("r-live"); len(got) != 2 {
		t.Fatalf("failed probe changed the buffer: %d samples, want 2", len(got))
	}

	// A terminal run's series is dropped at the end of the next pass.
	if _, err := h.store.Append(ctx, "r-live", &event.RunCompleted{}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.s.Samples("r-live"); len(got) != 0 {
		t.Fatalf("terminal run kept %d samples, want 0", len(got))
	}
}

// A provisioner without the capability degrades honestly: no samples, no error.
func TestSampleOnceWithoutCapability(t *testing.T) {
	h := newFixture()
	ctx := context.Background()

	envID, _ := h.envp.Provision(ctx, envport.EnvSpec{RunID: "r-nocap"})
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "nocap", FlowName: "loop", ProjectID: fixtureProject},
		&event.EnvRequested{}, &event.EnvReady{EnvID: string(envID)},
	} {
		if _, err := h.store.Append(ctx, "r-nocap", p); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.s.Samples("r-nocap"); len(got) != 0 {
		t.Fatalf("fake has no ResourceSampler, got %d samples", len(got))
	}
}

// failResolver fails Resolve for a project.
type failResolver struct {
	inner resolution.Resolver
	fail  string
}

func (r failResolver) Resolve(ctx context.Context, p string) (*resolution.Resolution, error) {
	if p == r.fail {
		return nil, errors.New("project gone")
	}
	return r.inner.Resolve(ctx, p)
}

// A terminal or pending run is not sampled, and its buffer is dropped.
func TestSampleOnceSkipsAndEvicts(t *testing.T) {
	h := newFixture()
	ctx := context.Background()
	h.res.Envp = samplerEnvp{EnvProvisioner: h.envp, cpu: 10}

	// A live run that gets a sample.
	liveEnv, _ := h.envp.Provision(ctx, envport.EnvSpec{RunID: "live"})
	appendRun(t, h, "live", string(liveEnv))
	// A pending run (created, no env) — skipped.
	if _, err := h.store.Append(ctx, "pending", &event.RunCreated{TicketTitle: "p", FlowName: "loop", ProjectID: fixtureProject}); err != nil {
		t.Fatal(err)
	}

	if err := h.s.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.s.Samples("live")) != 1 || len(h.s.Samples("pending")) != 0 {
		t.Fatalf("live=%d pending=%d", len(h.s.Samples("live")), len(h.s.Samples("pending")))
	}

	// The run completes: next pass drops its buffer (graphs are feedback, not history).
	if _, err := h.store.Append(ctx, "live", &event.RunCompleted{}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.s.Samples("live"); len(got) != 0 {
		t.Fatalf("a completed run kept %d samples", len(got))
	}
}

func TestSampleOnceResolveFailureIsAGap(t *testing.T) {
	h := newFixture()
	ctx := context.Background()
	h.res.Envp = samplerEnvp{EnvProvisioner: h.envp, cpu: 10}
	env, _ := h.envp.Provision(ctx, envport.EnvSpec{RunID: "r1"})
	appendRun(t, h, "r1", string(env))
	h.s.Resolver = failResolver{inner: h.s.Resolver, fail: fixtureProject}
	if err := h.s.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.s.Samples("r1"); len(got) != 0 {
		t.Fatalf("a failed resolve left %d samples, want a gap", len(got))
	}
}

func TestSampleOnceSampleFailureIsAGap(t *testing.T) {
	h := newFixture()
	ctx := context.Background()
	h.res.Envp = samplerEnvp{EnvProvisioner: h.envp, fail: true}
	env, _ := h.envp.Provision(ctx, envport.EnvSpec{RunID: "r1"})
	appendRun(t, h, "r1", string(env))
	if err := h.s.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.s.Samples("r1"); len(got) != 0 {
		t.Fatalf("a failed sample left %d samples", len(got))
	}
}

func TestSampleWindowIsBounded(t *testing.T) {
	h := newFixture()
	ctx := context.Background()
	h.res.Envp = samplerEnvp{EnvProvisioner: h.envp, cpu: 1}
	env, _ := h.envp.Provision(ctx, envport.EnvSpec{RunID: "r1"})
	appendRun(t, h, "r1", string(env))
	for range resourceWindow + 50 {
		if err := h.s.SampleOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(h.s.Samples("r1")); got != resourceWindow {
		t.Fatalf("window = %d, want the cap of %d", got, resourceWindow)
	}
}

func TestSampleOnceStoreFailure(t *testing.T) {
	h := newFixture()
	h.s.Runs = &run.Reader{Store: failAllStore{h.store, errors.New("db down")}}
	if err := h.s.SampleOnce(context.Background()); err == nil {
		t.Fatal("a store failure must surface")
	}
}

type failAllStore struct {
	*event.MemStore
	err error
}

func (f failAllStore) RunIDs(context.Context) ([]string, error) { return nil, f.err }

func TestRunLoop(t *testing.T) {
	h := newFixture()
	ctx := context.Background()
	h.res.Envp = samplerEnvp{EnvProvisioner: h.envp, cpu: 7}
	env, _ := h.envp.Provision(ctx, envport.EnvSpec{RunID: "r1"})
	appendRun(t, h, "r1", string(env))
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); h.s.Run(rctx, time.Millisecond) }()
	deadline := time.Now().Add(10 * time.Second)
	for len(h.s.Samples("r1")) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("Run never sampled twice")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestNilClockAndLog(t *testing.T) {
	s := &Sampler{}
	if s.now().IsZero() || s.log() == nil {
		t.Error("nil clock/log must fall back")
	}
}

func TestSamplesIsACopy(t *testing.T) {
	h := newFixture()
	ctx := context.Background()
	h.res.Envp = samplerEnvp{EnvProvisioner: h.envp, cpu: 3}
	env, _ := h.envp.Provision(ctx, envport.EnvSpec{RunID: "r1"})
	appendRun(t, h, "r1", string(env))
	if err := h.s.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	snap := h.s.Samples("r1")
	snap[0].CPUPerc = 999
	if h.s.Samples("r1")[0].CPUPerc == 999 {
		t.Fatal("Samples returned the live buffer")
	}
}

// Latest is the newest sample of the window; none for an unknown run and
// none once the run's buffer was evicted.
func TestLatest(t *testing.T) {
	h := newFixture()
	ctx := context.Background()
	env, _ := h.envp.Provision(ctx, envport.EnvSpec{RunID: "r1"})
	appendRun(t, h, "r1", string(env))
	if _, ok := h.s.Latest("r1"); ok {
		t.Fatal("a sample before any pass")
	}
	for _, cpu := range []float64{5, 9} {
		h.res.Envp = samplerEnvp{EnvProvisioner: h.envp, cpu: cpu}
		if err := h.s.SampleOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok := h.s.Latest("r1"); !ok || got.CPUPerc != 9 {
		t.Fatalf("Latest = %+v, %v; want the newest (cpu 9)", got, ok)
	}
	if _, ok := h.s.Latest("nope"); ok {
		t.Fatal("an unknown run has a sample")
	}
	if _, err := h.store.Append(ctx, "r1", &event.RunCompleted{}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.s.Latest("r1"); ok {
		t.Fatal("an evicted run still has a sample")
	}
}

func appendRun(t *testing.T, h *fixture, runID, envID string) {
	t.Helper()
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "t", FlowName: "loop", ProjectID: fixtureProject},
		&event.EnvRequested{}, &event.EnvReady{EnvID: envID},
	} {
		if _, err := h.store.Append(context.Background(), runID, p); err != nil {
			t.Fatal(err)
		}
	}
}
