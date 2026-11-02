package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"apothecary/internal/flow"
	"apothecary/internal/platform/logs"
	"apothecary/internal/project/confstore"
)

// usageEvery is how often the feature-usage snapshot is recomputed: flows
// and automations change at human speed, and the scan reads every flow.
const usageEvery = 10 * time.Minute

// usageSource is what the snapshot reads: every project's flows and
// automations ("" lists them all).
type usageSource interface {
	ListFlows(ctx context.Context, projectID string) ([]confstore.FlowRow, error)
	ListAutomations(ctx context.Context, projectID string) ([]confstore.Automation, error)
}

// featureUsage is the last snapshot: how many saved flows use each
// flow-language feature, and how many automations of each kind exist — what
// an operator reads to know which features are load-bearing.
type featureUsage struct {
	features    map[string]int64
	automations map[string]int64
}

// countUsage tallies one snapshot. A flow that no longer parses (saved
// under an older grammar) is skipped, not fatal.
func countUsage(ctx context.Context, src usageSource, log *slog.Logger) (featureUsage, error) {
	flows, err := src.ListFlows(ctx, "")
	if err != nil {
		return featureUsage{}, err
	}
	autos, err := src.ListAutomations(ctx, "")
	if err != nil {
		return featureUsage{}, err
	}
	u := featureUsage{features: map[string]int64{}, automations: map[string]int64{}}
	for _, row := range flows {
		f, err := flow.Parse(row.Name, []byte(row.YAML))
		if err != nil {
			log.Debug("feature usage: flow does not parse", logs.ProjectID, row.ProjectID, "flow", row.Name, "err", err)
			continue
		}
		for _, st := range f.Stages {
			u.features["stage."+string(st.Type)]++
			if st.Type == flow.StageAction {
				u.features["action."+st.Action]++
			}
			if st.RunIf != nil {
				u.features["run_if"]++
			}
		}
		if f.Budget.MaxCostUSD > 0 || f.Budget.MaxTotalTurns > 0 {
			u.features["budget"]++
		}
		u.features["phase"] += int64(len(f.Phases))
		u.features["mcp_server"] += int64(len(f.MCPServers))
		u.features["operates_on."+f.OperatesOn]++
	}
	for _, a := range autos {
		u.automations[a.Kind]++
	}
	return u, nil
}

// startUsageMetrics recomputes the snapshot now and every usageEvery, and
// reports it as the apothecary.flows.features and apothecary.automations
// gauges. The returned stop ends the goroutine and drops the callback.
func startUsageMetrics(meter metric.Meter, src usageSource, log *slog.Logger) (stop func(), err error) {
	features, err := meter.Int64ObservableGauge("apothecary.flows.features",
		metric.WithDescription("saved flows using each flow-language feature (stages: per stage)"))
	if err != nil {
		return nil, err
	}
	automations, err := meter.Int64ObservableGauge("apothecary.automations",
		metric.WithDescription("automations, by kind"))
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	var last featureUsage
	reg, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		mu.Lock()
		defer mu.Unlock()
		for k, n := range last.features {
			o.ObserveInt64(features, n, metric.WithAttributes(attribute.String("feature", k)))
		}
		for k, n := range last.automations {
			o.ObserveInt64(automations, n, metric.WithAttributes(attribute.String("kind", k)))
		}
		return nil
	}, features, automations)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(usageEvery)
		defer tick.Stop()
		for {
			u, err := countUsage(ctx, src, log)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Warn("feature usage", "err", err)
			} else {
				mu.Lock()
				last = u
				mu.Unlock()
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
		_ = reg.Unregister()
	}, nil
}
