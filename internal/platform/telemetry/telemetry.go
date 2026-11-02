// Package telemetry bootstraps OpenTelemetry for the daemon from the standard
// OTEL_* environment, so an operator points traces, metrics and logs at any
// OTLP backend without an apothecary-specific knob. It installs the process
// globals once; everything else (engine sink, HTTP and DB instrumentation,
// the slog bridge) reads those globals, which stay no-op when telemetry is
// off. It derives a run's and each stage attempt's trace IDs from the run ID,
// so the same span lands in the same trace across restarts and processes, and
// it wraps outbound HTTP so no URL credential reaches a span (http.go).
package telemetry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strconv"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	lognoop "go.opentelemetry.io/otel/log/noop"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
)

// Providers are the SDK providers Setup installed. The zero value (telemetry
// off) is valid: not enabled, a no-op logger provider, a no-op Shutdown.
type Providers struct {
	tp *sdktrace.TracerProvider
	mp *sdkmetric.MeterProvider
	lp *sdklog.LoggerProvider
}

// Enabled reports whether Setup installed exporting providers.
func (p *Providers) Enabled() bool { return p != nil && p.tp != nil }

// LoggerProvider is what the slog bridge exports through.
func (p *Providers) LoggerProvider() log.LoggerProvider {
	if !p.Enabled() {
		return lognoop.NewLoggerProvider()
	}
	return p.lp
}

// Shutdown flushes and stops every provider.
func (p *Providers) Shutdown(ctx context.Context) error {
	if !p.Enabled() {
		return nil
	}
	return errors.Join(p.tp.Shutdown(ctx), p.mp.Shutdown(ctx), p.lp.Shutdown(ctx))
}

// enabled is the on switch: an endpoint is configured and the SDK is not
// explicitly disabled.
func enabled() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" && os.Getenv("OTEL_SDK_DISABLED") != "true"
}

// Setup installs the global tracer, meter and logger providers and the
// W3C propagators from OTEL_* (exporter protocol, headers, sampler, resource
// attributes). With telemetry off it changes nothing.
func Setup(ctx context.Context, version string) (*Providers, error) {
	if !enabled() {
		return &Providers{}, nil
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName("apothecaryd"), semconv.ServiceVersion(version)),
		resource.WithFromEnv(), // OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES win
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}
	sexp, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("otel span exporter: %w", err)
	}
	reader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("otel metric reader: %w", err)
	}
	lexp, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("otel log exporter: %w", err)
	}
	p := &Providers{
		tp: sdktrace.NewTracerProvider(sdktrace.WithBatcher(sexp), sdktrace.WithResource(res), sdktrace.WithIDGenerator(idGen{})),
		mp: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithResource(res)),
		lp: sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(lexp)), sdklog.WithResource(res)),
	}
	otel.SetTracerProvider(p.tp)
	otel.SetMeterProvider(p.mp)
	global.SetLoggerProvider(p.lp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if err := runtime.Start(runtime.WithMeterProvider(p.mp)); err != nil {
		return nil, errors.Join(fmt.Errorf("otel runtime metrics: %w", err), p.Shutdown(ctx))
	}
	return p, nil
}

// RunTraceID is the run's marker trace: one instant "run" span carrying the
// run's attributes, which every stage attempt's trace links to. A run lives
// for days; its work is traced per attempt, never in one ever-growing trace.
func RunTraceID(runID string) trace.TraceID {
	sum := sha256.Sum256([]byte(runID))
	return trace.TraceID(sum[0:16])
}

// RunSpanID is the run marker span.
func RunSpanID(runID string) trace.SpanID {
	sum := sha256.Sum256([]byte(runID))
	return trace.SpanID(sum[16:24])
}

// StageTraceID is one stage attempt's own trace: stable for a (stage,
// iteration), so the engine's work under the attempt and the attempt span
// the sink exports from the event stream meet in it, across restarts too.
func StageTraceID(runID, stageID string, iteration int) trace.TraceID {
	sum := stageSum(runID, stageID, iteration)
	return trace.TraceID(sum[0:16])
}

// StageSpanID is the root span of the attempt's trace.
func StageSpanID(runID, stageID string, iteration int) trace.SpanID {
	sum := stageSum(runID, stageID, iteration)
	return trace.SpanID(sum[16:24])
}

func stageSum(runID, stageID string, iteration int) [32]byte {
	return sha256.Sum256([]byte(runID + "\x00" + stageID + "\x00" + strconv.Itoa(iteration)))
}

// RunSpanContext is the run marker span, as a link target or remote parent.
func RunSpanContext(runID string) trace.SpanContext {
	return remote(RunTraceID(runID), RunSpanID(runID))
}

// StageSpanContext is a stage attempt's root span as a remote parent.
func StageSpanContext(runID, stageID string, iteration int) trace.SpanContext {
	return remote(StageTraceID(runID, stageID, iteration), StageSpanID(runID, stageID, iteration))
}

func remote(tid trace.TraceID, sid trace.SpanID) trace.SpanContext {
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled, Remote: true,
	})
}

type forcedKey struct{}

type forced struct {
	tid trace.TraceID
	sid trace.SpanID
}

// WithIDs makes the next span started from ctx take exactly these IDs.
// Keep the started span on a fresh context (trace.ContextWithSpan(
// context.Background(), span)), never on ctx: children of ctx would be
// forced too.
func WithIDs(ctx context.Context, tid trace.TraceID, sid trace.SpanID) context.Context {
	return context.WithValue(ctx, forcedKey{}, forced{tid, sid})
}

// IDGenerator is the generator Setup installs: a tracer provider built
// elsewhere (a test's in-memory one) honours WithIDs only with it.
func IDGenerator() sdktrace.IDGenerator { return idGen{} }

// idGen hands out the IDs WithIDs forced, random ones otherwise.
type idGen struct{}

func (idGen) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	if f, ok := ctx.Value(forcedKey{}).(forced); ok {
		return f.tid, f.sid
	}
	var tid trace.TraceID
	var sid trace.SpanID
	_, _ = rand.Read(tid[:])
	_, _ = rand.Read(sid[:])
	return tid, sid
}

func (idGen) NewSpanID(ctx context.Context, tid trace.TraceID) trace.SpanID {
	if f, ok := ctx.Value(forcedKey{}).(forced); ok && f.tid == tid {
		return f.sid
	}
	var sid trace.SpanID
	_, _ = rand.Read(sid[:])
	return sid
}
