package resources

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"apothecary/internal/ports/envport"
	"apothecary/internal/run/event"
)

// The memory gauges sum each live run's latest sample per runtime, and drop
// a run once its series is gone.
func TestMemoryGaugesByRuntime(t *testing.T) {
	h := newFixture()
	ctx := context.Background()
	for _, id := range []string{"r1", "r2"} {
		envID, _ := h.envp.Provision(ctx, envport.EnvSpec{RunID: id, Name: id})
		for _, p := range []event.Payload{
			&event.RunCreated{TicketTitle: id, FlowName: "loop", ProjectID: fixtureProject},
			&event.EnvRequested{}, &event.EnvReady{EnvID: string(envID)},
		} {
			if _, err := h.store.Append(ctx, id, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.res.Envp = samplerEnvp{EnvProvisioner: h.envp}
	h.res.WorkspaceRuntime = envport.RuntimeKata

	reader := sdkmetric.NewManualReader()
	if err := h.s.RegisterMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")); err != nil {
		t.Fatal(err)
	}
	read := func() map[string]int64 {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(ctx, &rm); err != nil {
			t.Fatal(err)
		}
		out := map[string]int64{}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				for _, dp := range m.Data.(metricdata.Gauge[int64]).DataPoints {
					rt, _ := dp.Attributes.Value(attribute.Key("runtime"))
					out[m.Name+"/"+rt.AsString()] = dp.Value
				}
			}
		}
		return out
	}
	if got := read(); len(got) != 0 {
		t.Errorf("before any sample: %v", got)
	}
	if err := h.s.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	got := read()
	if got["apothecary.sandboxes.memory.used/kata"] != 200 || got["apothecary.sandboxes.memory.limit/kata"] != 400 {
		t.Errorf("two kata sandboxes = %v", got)
	}

	// A provisioner offering no choice is "default". A run that ended still
	// holds its sandbox until it is reaped: counted, without graphs.
	h.res.WorkspaceRuntime = ""
	if _, err := h.store.Append(ctx, "r2", &event.RunCompleted{}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	got = read()
	if got["apothecary.sandboxes.memory.used/default"] != 200 || got["apothecary.sandboxes.memory.limit/default"] != 400 || len(got) != 2 {
		t.Errorf("two default sandboxes, one finished = %v", got)
	}
	if len(h.s.Samples("r2")) != 0 {
		t.Error("a finished run's graphs went on")
	}
	// Reaped, it drops out.
	if _, err := h.store.Append(ctx, "r2", &event.EnvDestroyed{}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got = read(); got["apothecary.sandboxes.memory.used/default"] != 100 {
		t.Errorf("after the reap = %v", got)
	}
	// A sandbox that does not answer (stopped when idle) holds nothing: its
	// last sample is not carried forward.
	h.res.Envp = samplerEnvp{EnvProvisioner: h.envp, fail: true}
	if err := h.s.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got = read(); len(got) != 0 {
		t.Errorf("sleeping sandboxes counted: %v", got)
	}
}
