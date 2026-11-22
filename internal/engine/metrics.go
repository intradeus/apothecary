package engine

import (
	"context"
	"errors"

	"apothecary/internal/ports/envport"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// tracer starts the engine's work spans (provisioning, agent invocations)
// on the global provider: a no-op until platform/telemetry installs one.
var tracer = otel.Tracer("apothecaryd")

// registerMetrics reports the scheduler's occupancy as observable gauges on
// meter (New passes the global one, no-op while telemetry is off): how many
// runs hold a slot, how many wait for one, and how many slots there are.
// Read under e.mu at collection time, so nothing is recorded on the hot path.
func (e *Engine) registerMetrics(meter metric.Meter) {
	active, err1 := meter.Int64ObservableGauge("apothecary.engine.runs.active",
		metric.WithDescription("runs holding a concurrency slot"))
	queued, err2 := meter.Int64ObservableGauge("apothecary.engine.runs.queued",
		metric.WithDescription("pending runs waiting for a concurrency slot"))
	slots, err3 := meter.Int64ObservableGauge("apothecary.engine.runs.slots",
		metric.WithDescription("concurrency slots (max_concurrent_runs); absent when uncapped"))
	sandboxes, err4 := meter.Int64ObservableGauge("apothecary.sandboxes",
		metric.WithDescription("run sandboxes by runtime and state (the workspace badge: running, asleep, sleeping, waking, …)"))
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		e.log.Warn("engine metrics", "err", err)
		return
	}
	if _, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		e.mu.Lock()
		busy, waiting, lim := e.busyLocked(), len(e.queue), e.lastLimits
		e.mu.Unlock()
		o.ObserveInt64(active, int64(busy))
		o.ObserveInt64(queued, int64(waiting))
		if lim.RunsCapped() && lim.MaxConcurrentRuns > 0 {
			o.ObserveInt64(slots, int64(lim.MaxConcurrentRuns))
		}
		for k, n := range sandboxCounts(e.envState.Badges(), e.envState.Runtimes()) {
			o.ObserveInt64(sandboxes, n, metric.WithAttributes(
				attribute.String("runtime", k.runtime), attribute.String("state", k.state)))
		}
		return nil
	}, active, queued, slots, sandboxes); err != nil {
		e.log.Warn("engine metrics", "err", err)
	}
}

type sandboxKey struct{ runtime, state string }

// sandboxCounts counts sandboxes by runtime and badge state. Both are closed
// sets (the runtimes the engine knows, the badge words), never a stored
// value: "default" is a provisioner that offers no choice, "unreconciled" a
// run not yet probed.
func sandboxCounts(badges map[string]string, runtimes map[string]envport.Runtime) map[sandboxKey]int64 {
	out := map[sandboxKey]int64{}
	for runID, state := range badges {
		if state == string(envport.EnvAbsent) {
			continue // nothing left to count
		}
		rt, ok := runtimes[runID]
		name := string(rt)
		switch {
		case !ok:
			name = "unreconciled"
		case rt == "":
			name = "default"
		}
		out[sandboxKey{runtime: name, state: state}]++
	}
	return out
}
