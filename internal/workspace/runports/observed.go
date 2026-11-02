package runports

import (
	"context"
	"sort"
	"time"

	"apothecary/internal/platform/logs"
	"apothecary/internal/ports/envport"
)

// Runtime port discovery: the in-workspace shim reports its listeners via
// ReportObservedPorts (POST /observed-ports); the engine additionally scans
// compose SIBLING containers (a db the workspace scanner can't see) on the
// shim's heartbeat. Both feed one per-run set the run page lists with
// click-to-expose. Memory-only + TTL-aged: a discovered listener is external
// runtime state, never an event-logged transition.

const (
	// observedTTL is how long a snapshot stays live without a fresh shim
	// heartbeat (~3 missed 30s heartbeats). Past it ObservedPorts returns
	// nothing — the scanner (or the whole workspace) is gone; showing stale
	// ports would be a lie.
	observedTTL = 90 * time.Second
	// siblingRefreshInterval throttles the engine-side sibling scan (a docker
	// exec per sibling) — driven by shim heartbeats, not the UI poll.
	siblingRefreshInterval = 10 * time.Second
)

type observedRun struct {
	shim     []envport.ObservedPort
	siblings []envport.ObservedPort
	at       time.Time // last shim report (drives the TTL)
	sibAt    time.Time // last sibling scan
	sibBusy  bool      // a sibling refresh goroutine is in flight
	// autoExposed records ports we already attempted to auto-expose, so a
	// steady stream of identical reports does not re-expose (and the cap is
	// counted once per port).
	autoExposed map[int]bool
}

// ReportObservedPorts records a run's in-workspace listener snapshot (the
// shim scanner). It also opportunistically refreshes the compose-sibling
// scan (throttled) and, in auto-expose mode, exposes newly discovered ports
// up to the configured cap. Both side effects run asynchronously so the
// shim's POST never blocks on a docker exec or an expose.
func (s *Service) ReportObservedPorts(runID string, obs []envport.ObservedPort) {
	now := s.now()
	s.mu.Lock()
	if s.observed == nil {
		s.observed = map[string]*observedRun{}
	}
	r := s.observed[runID]
	if r == nil {
		r = &observedRun{autoExposed: map[int]bool{}}
		s.observed[runID] = r
	}
	r.shim = obs
	r.at = now
	dueSiblings := !r.sibBusy && now.Sub(r.sibAt) >= siblingRefreshInterval
	if dueSiblings {
		r.sibBusy = true
	}
	s.mu.Unlock()

	if dueSiblings {
		go s.refreshSiblingPorts(runID)
	}
	if s.Config.AutoExpose {
		go s.autoExposeObserved(runID)
	}
}

// ObservedPorts returns the run's discovered, not-yet-exposed listeners
// (shim ∪ siblings), or nil once the snapshot goes stale. Ports already
// live in the port relay are filtered out by the caller (getRunPorts) since
// only it knows the exposure registry.
func (s *Service) ObservedPorts(runID string) []envport.ObservedPort {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.observed[runID]
	if r == nil || s.now().Sub(r.at) > observedTTL {
		return nil
	}
	seen := map[int]bool{}
	var out []envport.ObservedPort
	for _, p := range r.shim { // workspace's own listeners win the port on a tie
		if !seen[p.Port] {
			seen[p.Port] = true
			out = append(out, p)
		}
	}
	for _, p := range r.siblings {
		if !seen[p.Port] {
			seen[p.Port] = true
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// forgetObserved drops a run's snapshot (its env is destroyed).
func (s *Service) forgetObserved(runID string) {
	s.mu.Lock()
	delete(s.observed, runID)
	s.mu.Unlock()
}

// refreshSiblingPorts resolves the run's provisioner and, if it supports
// sibling scanning, refreshes the compose-sibling listener set. Runs in its
// own goroutine; failures are silent (best-effort discovery).
func (s *Service) refreshSiblingPorts(runID string) {
	defer func() {
		s.mu.Lock()
		if r := s.observed[runID]; r != nil {
			r.sibBusy = false
		}
		s.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	envID, scanner, release, ok := s.resolveScanner(ctx, runID)
	if !ok {
		return
	}
	defer release()
	sibs := scanner.ScanSiblingPorts(ctx, envID)

	s.mu.Lock()
	if r := s.observed[runID]; r != nil {
		r.siblings = sibs
		r.sibAt = s.now()
	}
	s.mu.Unlock()
}

// resolveScanner resolves a run to its EnvID and sibling-port scanner, or
// ok=false when the run has no env or the provisioner lacks the capability.
func (s *Service) resolveScanner(ctx context.Context, runID string) (envport.EnvID, envport.SiblingPortScanner, func(), bool) {
	v, err := s.Runs.View(ctx, runID)
	if err != nil {
		return "", nil, nil, false
	}
	if v.EnvID == "" || v.ProjectID == "" {
		return "", nil, nil, false
	}
	res, err := s.Resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return "", nil, nil, false
	}
	scanner, ok := res.Envp.(envport.SiblingPortScanner)
	if !ok {
		res.Close()
		return "", nil, nil, false
	}
	return envport.EnvID(v.EnvID), scanner, res.Close, true
}

// autoExposeObserved exposes discovered ports not yet exposed, up to the
// per-run cap. Mark-once (no retry storms): a failed expose stays clickable
// in the UI. Only invoked when Config.AutoExpose is set.
func (s *Service) autoExposeObserved(runID string) {
	obs := s.ObservedPorts(runID)
	if len(obs) == 0 {
		return
	}
	s.mu.Lock()
	r := s.observed[runID]
	if r == nil {
		s.mu.Unlock()
		return
	}
	var todo []int
	for _, p := range obs {
		if len(r.autoExposed) >= s.Config.maxAuto() {
			break
		}
		if r.autoExposed[p.Port] {
			continue
		}
		if s.Relay != nil && s.Relay.Existing(runID, p.Port) != "" {
			continue // already exposed (manual, or a prior auto pass)
		}
		r.autoExposed[p.Port] = true
		todo = append(todo, p.Port)
	}
	s.mu.Unlock()

	for _, port := range todo {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if _, err := s.Expose(ctx, runID, port); err != nil {
			s.log().Warn("auto-expose of discovered port failed", logs.RunID, runID, "port", port, "err", err)
		}
		cancel()
	}
}
