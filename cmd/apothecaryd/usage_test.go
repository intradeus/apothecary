package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"apothecary/internal/project/confstore"
)

type fakeUsage struct {
	flows []confstore.FlowRow
	autos []confstore.Automation
	asked []string // project ids the lists were asked for
}

func (f *fakeUsage) ListFlows(_ context.Context, p string) ([]confstore.FlowRow, error) {
	f.asked = append(f.asked, p)
	return f.flows, nil
}

func (f *fakeUsage) ListAutomations(_ context.Context, p string) ([]confstore.Automation, error) {
	f.asked = append(f.asked, p)
	return f.autos, nil
}

const usageFlow = `name: t
version: 5
parameters: { harness: fake, model: fake/model }
main:
  - { id: c, type: command, run: x }
  - { id: d, type: command, run: y }
  - { id: m, type: action, action: create_mr, input: { body: { from: repo:a.md, type: file } } }
`

func usageSrc() *fakeUsage {
	return &fakeUsage{
		flows: []confstore.FlowRow{
			{ProjectID: "p1", Name: "t", YAML: usageFlow},
			{ProjectID: "p2", Name: "broken", YAML: "name: [unterminated"},
		},
		autos: []confstore.Automation{{Kind: "schedule"}, {Kind: "schedule"}, {Kind: "mr_opened"}},
	}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestCountUsageTalliesEveryProjectsFlows(t *testing.T) {
	src := usageSrc()
	u, err := countUsage(context.Background(), src, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if len(src.asked) != 2 || src.asked[0] != "" || src.asked[1] != "" {
		t.Errorf("asked for projects %q, want every project (\"\")", src.asked)
	}
	for k, want := range map[string]int64{"stage.command": 2, "stage.action": 1, "action.create_mr": 1} {
		if u.features[k] != want {
			t.Errorf("features[%s] = %d, want %d (all: %v)", k, u.features[k], want, u.features)
		}
	}
	if u.features["run_if"] != 0 {
		t.Errorf("run_if = %d", u.features["run_if"])
	}
	if u.automations["schedule"] != 2 || u.automations["mr_opened"] != 1 {
		t.Errorf("automations = %v", u.automations)
	}
}

func TestUsageGaugesReportTheSnapshot(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	stop, err := startUsageMetrics(mp.Meter("test"), usageSrc(), quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatal(err)
		}
		got := map[string]int64{}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				for _, dp := range m.Data.(metricdata.Gauge[int64]).DataPoints {
					for _, kv := range dp.Attributes.ToSlice() {
						got[m.Name+"/"+kv.Value.AsString()] = dp.Value
					}
				}
			}
		}
		if got["apothecary.flows.features/stage.command"] == 2 && got["apothecary.automations/schedule"] == 2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("gauges = %v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
