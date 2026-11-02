package pgstore

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"apothecary/internal/ports/egressport"
	"apothecary/internal/run/event"
)

// collected is one reading of every metric, by name: an Int64 gauge or sum's
// value, a Float64 sum's value, or a histogram's sample count.
type collected map[string]float64

// globalReader installs an in-memory global meter provider; Open reads the
// global, so call it before the store is opened.
func globalReader(t *testing.T) func() collected {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(prev) })
	return func() collected {
		t.Helper()
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatal(err)
		}
		out := collected{}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				switch d := m.Data.(type) {
				case metricdata.Gauge[int64]:
					for _, dp := range d.DataPoints {
						out[m.Name] = float64(dp.Value)
					}
				case metricdata.Sum[int64]:
					for _, dp := range d.DataPoints {
						out[m.Name] = float64(dp.Value)
					}
				case metricdata.Sum[float64]:
					for _, dp := range d.DataPoints {
						out[m.Name] = dp.Value
					}
				case metricdata.Histogram[float64]:
					for _, dp := range d.DataPoints {
						out[m.Name] += float64(dp.Count)
					}
				}
			}
		}
		return out
	}
}

// The store's instruments read what the database knows: the pool's
// occupancy, the runs holding a workspace, the denied egress — and time
// every append and rebuild.
func TestStoreMetrics(t *testing.T) {
	read := globalReader(t)
	s := open(t)
	// Preparing the test database may already have rebuilt once.
	rebuilds := read()["apothecary.projections.rebuild.duration"]
	ctx := context.Background()

	if _, err := s.Append(ctx, "r1", &event.RunCreated{FlowName: "f"}, &event.EnvReady{EnvID: "env-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "r2", &event.RunCreated{FlowName: "f"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.UpsertEgressAudit(ctx, []egressport.EgressAuditRow{
		{Run: "r1", Host: "a.example", Port: 443, Verdict: "deny", Count: 3, First: now, Last: now},
		{Run: "r1", Host: "b.example", Port: 443, Verdict: "deny", Count: 4, First: now, Last: now},
		{Run: "r1", Host: "c.example", Port: 443, Verdict: "allow", Count: 10, First: now, Last: now},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}

	got := read()
	if got["apothecary.db.pool.total"] < 1 || got["apothecary.db.pool.acquires"] < 1 {
		t.Errorf("pool = total %v acquires %v", got["apothecary.db.pool.total"], got["apothecary.db.pool.acquires"])
	}
	for _, name := range []string{"apothecary.db.pool.acquired", "apothecary.db.pool.idle",
		"apothecary.db.pool.empty_acquires", "apothecary.db.pool.acquire_wait"} {
		if _, ok := got[name]; !ok {
			t.Errorf("%s not reported", name)
		}
	}
	if got["apothecary.workspaces.live"] != 1 {
		t.Errorf("workspaces.live = %v, want 1", got["apothecary.workspaces.live"])
	}
	if got["apothecary.egress.denied"] != 7 {
		t.Errorf("egress.denied = %v, want 7", got["apothecary.egress.denied"])
	}
	if got["apothecary.events.append.duration"] != 2 {
		t.Errorf("append samples = %v, want 2", got["apothecary.events.append.duration"])
	}
	if got["apothecary.projections.rebuild.duration"] != rebuilds+1 {
		t.Errorf("rebuild samples = %v, want %v", got["apothecary.projections.rebuild.duration"], rebuilds+1)
	}

	// Closed, the store stops reporting: its callback is gone.
	s.Close()
	if _, ok := read()["apothecary.db.pool.total"]; ok {
		t.Error("a closed store still reports its pool")
	}
}

// A database that stops answering fails the collection rather than
// reporting a stale count.
func TestStoreMetricsSurfaceAQueryError(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(prev) })
	s := open(t)
	if _, err := s.pool.Exec(context.Background(), `DROP TABLE egress_audit`); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err == nil {
		t.Error("collection over a missing table succeeded")
	}
}
