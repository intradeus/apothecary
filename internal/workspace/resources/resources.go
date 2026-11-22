// Package resources samples the cpu/memory/network use of every active run's
// workspace into a rolling in-memory window behind the run-page graphs. It is
// observation, never history: nothing here enters a run's log, so it needs
// the run views and the resolver but not the engine.
package resources

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"apothecary/internal/platform/logs"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/workspace/wsname"
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
	// usage is each project's recent runs, newest first, at most usageRuns:
	// what their sandboxes reached, beside the sandbox size setting. Memory
	// only like buf, but kept past a run's end: it is the answer to "how big
	// does this project's sandbox need to be".
	usage map[string][]RunUsage
	// runtime is each sampled run's runtime, for the memory gauges' label.
	runtime map[string]envport.Runtime
}

// RunUsage is what one run's sandbox reached while it was sampled: its peak
// memory (working set), the limit it ran under, and the processes the kernel
// killed at that limit.
type RunUsage struct {
	RunID    string    `json:"run_id"`
	PeakMem  int64     `json:"peak_mem"`
	MemLimit int64     `json:"mem_limit"`
	OOMKills int       `json:"oom_kills"`
	At       time.Time `json:"at"` // the latest sample
}

// usageRuns bounds a project's usage record.
const usageRuns = 10

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
				s.log().Debug("resource sample: resolve failed", logs.RunID, id, logs.ProjectID, v.ProjectID, "err", err)
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
			s.log().Debug("resource sample failed", logs.RunID, id, "err", err)
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
		if s.runtime == nil {
			s.runtime = map[string]envport.Runtime{}
		}
		s.runtime[id] = res.WorkspaceRuntime
		s.recordUsage(v.ProjectID, id, sample)
		s.mu.Unlock()
	}
	s.mu.Lock()
	for id := range s.buf {
		if !live[id] {
			delete(s.buf, id)
			delete(s.runtime, id)
		}
	}
	s.mu.Unlock()
	return nil
}

// recordUsage folds a sample into its run's usage record (s.mu held).
func (s *Sampler) recordUsage(project, runID string, x envport.ResourceSample) {
	if s.usage == nil {
		s.usage = map[string][]RunUsage{}
	}
	runs := s.usage[project]
	i := slices.IndexFunc(runs, func(u RunUsage) bool { return u.RunID == runID })
	var u RunUsage
	if i >= 0 {
		u = runs[i]
		runs = slices.Delete(runs, i, i+1)
	}
	u.RunID, u.At, u.MemLimit = runID, x.At, x.MemLimit
	u.PeakMem = max(u.PeakMem, x.MemUsed)
	u.OOMKills = max(u.OOMKills, x.OOMKills)
	runs = append([]RunUsage{u}, runs...)
	if len(runs) > usageRuns {
		runs = runs[:usageRuns]
	}
	s.usage[project] = runs
}

// Usage is the project's recent runs' usage, newest first; empty since the
// engine started or for a project whose runs were never sampled.
func (s *Sampler) Usage(project string) []RunUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RunUsage{}, s.usage[project]...)
}

// Samples returns a snapshot copy of the run's resource-sample window
// (the run-page graphs). Empty means "nothing sampled yet": no env, a
// provisioner without the capability, or an engine restart.
func (s *Sampler) Samples(runID string) []envport.ResourceSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]envport.ResourceSample(nil), s.buf[runID]...)
}

// Latest returns the newest sample of the run's window, false when there is
// none — a cross-run snapshot (the war room) reads one sample per run
// without copying whole windows.
func (s *Sampler) Latest(runID string) (envport.ResourceSample, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	buf := s.buf[runID]
	if len(buf) == 0 {
		return envport.ResourceSample{}, false
	}
	return buf[len(buf)-1], true
}
