package engine

import (
	"context"
	"sort"
	"time"

	"apothecary/internal/ports"
	"apothecary/internal/run"
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
	shim     []ports.ObservedPort
	siblings []ports.ObservedPort
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
func (e *Engine) ReportObservedPorts(runID string, obs []ports.ObservedPort) {
	now := e.clock()
	e.observedMu.Lock()
	r := e.observed[runID]
	if r == nil {
		r = &observedRun{autoExposed: map[int]bool{}}
		e.observed[runID] = r
	}
	r.shim = obs
	r.at = now
	dueSiblings := !r.sibBusy && now.Sub(r.sibAt) >= siblingRefreshInterval
	if dueSiblings {
		r.sibBusy = true
	}
	e.observedMu.Unlock()

	if dueSiblings {
		go e.refreshSiblingPorts(runID)
	}
	if e.cfg.PortDiscoveryAutoExpose {
		go e.autoExposeObserved(runID)
	}
}

// ObservedPorts returns the run's discovered, not-yet-exposed listeners
// (shim ∪ siblings), or nil once the snapshot goes stale. Ports already
// live in the port relay are filtered out by the caller (getRunPorts) since
// only it knows the exposure registry.
func (e *Engine) ObservedPorts(runID string) []ports.ObservedPort {
	e.observedMu.Lock()
	defer e.observedMu.Unlock()
	r := e.observed[runID]
	if r == nil || e.clock().Sub(r.at) > observedTTL {
		return nil
	}
	seen := map[int]bool{}
	var out []ports.ObservedPort
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

// forgetObserved drops a run's snapshot (called when its env is destroyed).
func (e *Engine) forgetObserved(runID string) {
	e.observedMu.Lock()
	delete(e.observed, runID)
	e.observedMu.Unlock()
}

// refreshSiblingPorts resolves the run's provisioner and, if it supports
// sibling scanning, refreshes the compose-sibling listener set. Runs in its
// own goroutine; failures are silent (best-effort discovery).
func (e *Engine) refreshSiblingPorts(runID string) {
	defer func() {
		e.observedMu.Lock()
		if r := e.observed[runID]; r != nil {
			r.sibBusy = false
		}
		e.observedMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	envID, scanner, release, ok := e.resolveScanner(ctx, runID)
	if !ok {
		return
	}
	defer release()
	sibs := scanner.ScanSiblingPorts(ctx, envID)

	e.observedMu.Lock()
	if r := e.observed[runID]; r != nil {
		r.siblings = sibs
		r.sibAt = e.clock()
	}
	e.observedMu.Unlock()
}

// resolveScanner resolves a run to its EnvID and sibling-port scanner, or
// ok=false when the run has no env or the provisioner lacks the capability.
func (e *Engine) resolveScanner(ctx context.Context, runID string) (ports.EnvID, ports.SiblingPortScanner, func(), bool) {
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil || len(evs) == 0 {
		return "", nil, nil, false
	}
	v := run.Reduce(evs)
	if v.EnvID == "" || v.ProjectID == "" {
		return "", nil, nil, false
	}
	res, err := e.resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return "", nil, nil, false
	}
	scanner, ok := res.Envp.(ports.SiblingPortScanner)
	if !ok {
		res.Close()
		return "", nil, nil, false
	}
	return ports.EnvID(v.EnvID), scanner, res.Close, true
}

// autoExposeObserved exposes discovered ports not yet exposed, up to the
// per-run cap. Mark-once (no retry storms): a failed expose stays clickable
// in the UI. Only invoked when PortDiscoveryAutoExpose is set.
func (e *Engine) autoExposeObserved(runID string) {
	obs := e.ObservedPorts(runID)
	if len(obs) == 0 {
		return
	}
	e.observedMu.Lock()
	r := e.observed[runID]
	if r == nil {
		e.observedMu.Unlock()
		return
	}
	var todo []int
	for _, p := range obs {
		if len(r.autoExposed) >= e.cfg.PortDiscoveryMaxAuto {
			break
		}
		if r.autoExposed[p.Port] {
			continue
		}
		if e.portRelay != nil && e.portRelay.Existing(runID, p.Port) != "" {
			continue // already exposed (manual, or a prior auto pass)
		}
		r.autoExposed[p.Port] = true
		todo = append(todo, p.Port)
	}
	e.observedMu.Unlock()

	for _, port := range todo {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if _, err := e.ExposeRunPort(ctx, runID, port); err != nil {
			e.log.Warn("auto-expose of discovered port failed", "run", runID, "port", port, "err", err)
		}
		cancel()
	}
}
