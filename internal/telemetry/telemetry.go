// Package telemetry implements §7: the engine emits OTel — one span per
// run, a child span per stage attempt, and the four factory metrics — all
// DERIVED FROM THE EVENT STREAM, so harness-native telemetry is never
// load-bearing and the emission is a pure projection of the source of
// truth. OTLP/gRPC to the configured collector; disabled when no endpoint.
package telemetry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"factory/internal/event"
)

type Telemetry struct {
	tp *sdktrace.TracerProvider
	mp *sdkmetric.MeterProvider

	tracer trace.Tracer

	runsTotal  metric.Int64Counter
	pingsTotal metric.Int64Counter
	stageCost  metric.Float64Histogram
	stageTurns metric.Int64Histogram

	mu   sync.Mutex
	runs map[string]*runTrace
}

type runTrace struct {
	ctx   context.Context // carries the run span
	span  trace.Span
	stage trace.Span // open stage attempt, if any
	// attrs of the open stage (from stage_started), reused on completion.
	stageID string
	harness string
	model   string
}

// New connects to the OTLP/gRPC endpoint (§13 otel.endpoint). Callers must
// not construct one when the endpoint is empty — telemetry is then simply
// absent (nil sink).
func New(ctx context.Context, endpoint string) (*Telemetry, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName("factoryd")),
	)
	if err != nil {
		return nil, err
	}
	texp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("otel trace exporter: %w", err)
	}
	mexp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint), otlpmetricgrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("otel metric exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(texp), sdktrace.WithResource(res))
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp, sdkmetric.WithInterval(15*time.Second))),
		sdkmetric.WithResource(res),
	)

	t := &Telemetry{tp: tp, mp: mp, tracer: tp.Tracer("factoryd"), runs: map[string]*runTrace{}}
	meter := mp.Meter("factoryd")
	if t.runsTotal, err = meter.Int64Counter("factory_runs_total",
		metric.WithDescription("runs reaching a terminal state, by state")); err != nil {
		return nil, err
	}
	if t.pingsTotal, err = meter.Int64Counter("factory_pings_total",
		metric.WithDescription("pings raised, by type")); err != nil {
		return nil, err
	}
	if t.stageCost, err = meter.Float64Histogram("factory_stage_cost_usd",
		metric.WithDescription("cost per completed stage attempt")); err != nil {
		return nil, err
	}
	if t.stageTurns, err = meter.Int64Histogram("factory_stage_turns",
		metric.WithDescription("turns per completed stage attempt")); err != nil {
		return nil, err
	}
	return t, nil
}

// Shutdown flushes both providers.
func (t *Telemetry) Shutdown(ctx context.Context) {
	t.mu.Lock()
	for id, rt := range t.runs {
		if rt.stage != nil {
			rt.stage.End()
		}
		rt.span.End()
		delete(t.runs, id)
	}
	t.mu.Unlock()
	_ = t.tp.Shutdown(ctx)
	_ = t.mp.Shutdown(ctx)
}

// run returns (lazily creating) the run trace. Recovered runs whose
// run_created predates this boot still get a span, starting at the first
// event seen this session.
func (t *Telemetry) run(e *event.Event) *runTrace {
	if rt, ok := t.runs[e.RunID]; ok {
		return rt
	}
	ctx, span := t.tracer.Start(context.Background(), "run",
		trace.WithTimestamp(e.TS),
		trace.WithAttributes(attribute.String("factory.run_id", e.RunID)))
	rt := &runTrace{ctx: ctx, span: span}
	t.runs[e.RunID] = rt
	return rt
}

// OnEvent is the engine sink (§7): every freshly appended event flows
// through here exactly once.
func (t *Telemetry) OnEvent(e event.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ctx := context.Background()
	rt := t.run(&e)

	switch p := e.Payload.(type) {
	case *event.RunCreated:
		rt.span.SetAttributes(
			attribute.String("factory.flow", p.FlowName),
			attribute.String("factory.ticket", p.TicketTitle),
		)
	case *event.StageStarted:
		if rt.stage != nil { // safety: an attempt left open
			rt.stage.End(trace.WithTimestamp(e.TS))
		}
		_, span := t.tracer.Start(rt.ctx, p.StageID,
			trace.WithTimestamp(e.TS),
			trace.WithAttributes(
				attribute.String("factory.run_id", e.RunID),
				attribute.String("factory.stage", p.StageID),
				attribute.Int("factory.iteration", p.Iteration),
				attribute.String("factory.harness", p.Harness),
				attribute.String("factory.model", p.Model),
			))
		rt.stage, rt.stageID, rt.harness, rt.model = span, p.StageID, p.Harness, p.Model
	case *event.StageCompleted:
		attrs := metric.WithAttributes(
			attribute.String("stage", p.StageID),
			attribute.String("harness", rt.harness),
			attribute.String("model", rt.model),
		)
		if p.Usage != nil {
			t.stageCost.Record(ctx, p.Usage.CostUSD, attrs)
		}
		if p.TurnsUsed > 0 {
			t.stageTurns.Record(ctx, int64(p.TurnsUsed), attrs)
		}
		t.endStage(rt, e.TS, codes.Ok, "")
	case *event.StageFailed:
		t.endStage(rt, e.TS, codes.Error, p.Reason)
	case *event.PingRaised:
		t.pingsTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("type", p.Type)))
	case *event.RunCompleted:
		t.endRun(e.RunID, rt, e.TS, "completed", codes.Ok, "")
	case *event.RunAbandoned:
		t.endRun(e.RunID, rt, e.TS, "abandoned", codes.Error, p.Reason)
	case *event.RunFailed:
		t.endRun(e.RunID, rt, e.TS, "failed", codes.Error, p.Reason)
	}
}

func (t *Telemetry) endStage(rt *runTrace, ts time.Time, code codes.Code, desc string) {
	if rt.stage == nil {
		return
	}
	rt.stage.SetStatus(code, desc)
	rt.stage.End(trace.WithTimestamp(ts))
	rt.stage = nil
}

func (t *Telemetry) endRun(runID string, rt *runTrace, ts time.Time, state string, code codes.Code, desc string) {
	t.runsTotal.Add(context.Background(), 1, metric.WithAttributes(attribute.String("state", state)))
	t.endStage(rt, ts, code, "run "+state)
	rt.span.SetStatus(code, desc)
	rt.span.End(trace.WithTimestamp(ts))
	delete(t.runs, runID)
}
