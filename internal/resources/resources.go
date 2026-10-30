// Package resources samples the cpu/memory/network use of every active run's
// workspace into a rolling in-memory window behind the run-page graphs. It is
// observation, never history: nothing here enters a run's log, so it needs
// the run views and the resolver but not the engine.
package resources

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"apothecary/internal/ports/envport"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
	"apothecary/internal/wsname"
)

// Sampler keeps each active run's window of resource samples.
type Sampler struct {
	Runs     *run.Reader
	Resolver resolution.Resolver
	// Clock stamps samples; nil is time.Now.
	Clock func() time.Time
	// Log is nil for slog.Default.
	Log *slog.Logger

	mu sync.Mutex
	// buf is the per-run rolling window of workspace resource samples
	// (cpu/mem/net), appended by SampleOnce and served to the
	// run-page graphs. Memory-only like the reconciled env status: usage is external state,
	// not a run-authored transition, so it never enters the event log. A
	// failed sample appends NOTHING — a gap in the series is honest, a
	// carried-forward value is the stale lie the reconciler comment warns
	// against.
	buf map[string][]envport.ResourceSample
}

func New(runs *run.Reader, resolver resolution.Resolver, clock func() time.Time, log *slog.Logger) *Sampler {
	return &Sampler{Runs: runs, Resolver: resolver, Clock: clock, Log: log}
}

func (s *Sampler) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Sampler) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Run takes a sample pass at once and then every interval until ctx ends.
// A failed pass is logged; the next one tries again.
func (s *Sampler) Run(ctx context.Context, every time.Duration) {
	pass := func() {
		if err := s.SampleOnce(ctx); err != nil && ctx.Err() == nil {
			s.log().Error("resource sample pass failed", "err", err)
		}
	}
	pass()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			pass()
		case <-ctx.Done():
			return
		}
	}
}

// resourceWindow caps the per-run sample buffer: at the ~10s sampler cadence,
// 360 samples ≈ the last hour — plenty for "what is this run costing right
// now" graphs without unbounded growth.
const resourceWindow = 360

// SampleOnce takes one resource reading (cpu/mem/net) of every
// active run's environment via the provisioner's OPTIONAL ResourceSampler
// capability, feeding the per-run in-memory window behind the run-page
// graphs. Unlike the reconciler it does NOT skip runs with an op in flight:
// provisioning/rebuilding is exactly when the operator watches the graphs,
// and a probe failure here is just a gap, never a spurious badge. Buffers of
// terminal (or vanished) runs are dropped at the end of the pass — the
// workspace may outlive the run for inspection, but the graphs are working
// feedback, not history.
func (s *Sampler) SampleOnce(ctx context.Context) error {
	views, err := s.Runs.All(ctx)
	if err != nil {
		return err
	}
	live := map[string]bool{}
	// Resolutions are cached per project within the pass, as in the env reconciler.
	resolved := map[string]*resolution.Resolution{}
	defer func() {
		for _, res := range resolved {
			res.Close()
		}
	}()
	for _, v := range views {
		id := v.RunID
		// Pending runs have no container yet; pre-v2 runs with no project
		// can't be resolved to a provisioner.
		if v.ProjectID == "" || v.State == run.Pending || v.State.Terminal() {
			continue
		}
		live[id] = true
		res, ok := resolved[v.ProjectID]
		if !ok {
			res, err = s.Resolver.Resolve(ctx, v.ProjectID)
			if err != nil {
				s.log().Debug("resource sample: resolve failed", "run", id, "project", v.ProjectID, "err", err)
				continue
			}
			resolved[v.ProjectID] = res
		}
		sampler, ok := res.Envp.(envport.ResourceSampler)
		if !ok {
			continue
		}
		sample, err := sampler.SampleResources(ctx, envport.EnvID(wsname.ForRun(v, id)))
		if err != nil {
			s.log().Debug("resource sample failed", "run", id, "err", err)
			continue
		}
		sample.At = s.now()
		s.mu.Lock()
		if s.buf == nil {
			s.buf = map[string][]envport.ResourceSample{}
		}
		buf := append(s.buf[id], sample)
		if len(buf) > resourceWindow {
			buf = buf[len(buf)-resourceWindow:]
		}
		s.buf[id] = buf
		s.mu.Unlock()
	}
	s.mu.Lock()
	for id := range s.buf {
		if !live[id] {
			delete(s.buf, id)
		}
	}
	s.mu.Unlock()
	return nil
}

// Samples returns a snapshot copy of the run's resource-sample window
// (the run-page graphs). Empty means "nothing sampled yet": no env, a
// provisioner without the capability, or an engine restart.
func (s *Sampler) Samples(runID string) []envport.ResourceSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]envport.ResourceSample(nil), s.buf[runID]...)
}
