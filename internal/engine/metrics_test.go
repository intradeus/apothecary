package engine

import (
	"context"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"apothecary/internal/run/event"
)

// gauges registers e's scheduler gauges on a private in-memory provider (not
// the global, which every other test's engine also registers on) and returns
// a reader of its Int64 gauges.
func gauges(t *testing.T, e *Engine) func() map[string]int64 {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	e.registerMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test"))
	return func() map[string]int64 {
		t.Helper()
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatal(err)
		}
		out := map[string]int64{}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if g, ok := m.Data.(metricdata.Gauge[int64]); ok {
					for _, dp := range g.DataPoints {
						out[m.Name] = dp.Value
					}
				}
			}
		}
		return out
	}
}

// The scheduler gauges read the engine's live state: resident loops holding
// a slot (not ones idling between phases), the FIFO of pending runs, and the
// cap the last admission ran under — absent while nothing was scheduled or
// when uncapped.
func TestSchedulerGauges(t *testing.T) {
	e := New(Deps{Store: event.NewMemStore()})
	read := gauges(t, e)

	got := read()
	if got["apothecary.engine.runs.active"] != 0 || got["apothecary.engine.runs.queued"] != 0 {
		t.Errorf("idle engine = %v", got)
	}
	if _, ok := got["apothecary.engine.runs.slots"]; ok {
		t.Errorf("slots reported before any admission: %v", got)
	}

	idle := &runHandle{}
	idle.idle.Store(true)
	e.mu.Lock()
	e.handles["busy"] = &runHandle{}
	e.handles["parked"] = idle
	e.queue = []string{"q1", "q2"}
	e.scheduleLocked(Limits{MaxConcurrentRuns: 1}) // full: admits nothing
	e.mu.Unlock()

	got = read()
	for name, want := range map[string]int64{
		"apothecary.engine.runs.active": 1,
		"apothecary.engine.runs.queued": 2,
		"apothecary.engine.runs.slots":  1,
	} {
		if got[name] != want {
			t.Errorf("%s = %d, want %d (all: %v)", name, got[name], want, got)
		}
	}

	e.mu.Lock()
	e.lastLimits = Limits{MaxConcurrentRuns: -1}
	e.mu.Unlock()
	if _, ok := read()["apothecary.engine.runs.slots"]; ok {
		t.Error("slots reported while uncapped")
	}
}
