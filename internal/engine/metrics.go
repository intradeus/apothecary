package engine

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
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
	if err := errors.Join(err1, err2, err3); err != nil {
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
		return nil
	}, active, queued, slots); err != nil {
		e.log.Warn("engine metrics", "err", err)
	}
}
