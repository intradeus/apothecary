package telemetry

import (
	"context"
	"crypto/sha256"
	"net"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

// collector is a loopback OTLP/gRPC collector: what Setup's exporters really
// send over the wire.
type collector struct {
	coltrace.UnimplementedTraceServiceServer
	mu      sync.Mutex
	spans   []string
	metrics []string
}

func (c *collector) Export(_ context.Context, req *coltrace.ExportTraceServiceRequest) (*coltrace.ExportTraceServiceResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, s := range ss.GetSpans() {
				c.spans = append(c.spans, s.GetName())
			}
		}
	}
	return &coltrace.ExportTraceServiceResponse{}, nil
}

// metricsServer exists because every OTLP service calls its rpc Export.
type metricsServer struct {
	colmetric.UnimplementedMetricsServiceServer
	c *collector
}

func (m metricsServer) Export(_ context.Context, req *colmetric.ExportMetricsServiceRequest) (*colmetric.ExportMetricsServiceResponse, error) {
	m.c.mu.Lock()
	defer m.c.mu.Unlock()
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, mt := range sm.GetMetrics() {
				m.c.metrics = append(m.c.metrics, mt.GetName())
			}
		}
	}
	return &colmetric.ExportMetricsServiceResponse{}, nil
}

type logsServer struct {
	collogs.UnimplementedLogsServiceServer
}

func (logsServer) Export(context.Context, *collogs.ExportLogsServiceRequest) (*collogs.ExportLogsServiceResponse, error) {
	return &collogs.ExportLogsServiceResponse{}, nil
}

// restoreGlobals puts the process-wide providers back after a Setup.
func restoreGlobals(t *testing.T) {
	tp, mp, lp, prop := otel.GetTracerProvider(), otel.GetMeterProvider(), global.GetLoggerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(tp)
		otel.SetMeterProvider(mp)
		global.SetLoggerProvider(lp)
		otel.SetTextMapPropagator(prop)
	})
}

// Setup exports over the OTEL_* configured OTLP/gRPC endpoint through the
// globals, and Shutdown is what flushes them.
func TestSetupExportsToTheCollectorOnShutdown(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	col := &collector{}
	srv := grpc.NewServer()
	coltrace.RegisterTraceServiceServer(srv, col)
	colmetric.RegisterMetricsServiceServer(srv, metricsServer{c: col})
	collogs.RegisterLogsServiceServer(srv, logsServer{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.Stop()
		<-done
	})
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+lis.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_SDK_DISABLED", "")
	restoreGlobals(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	prov, err := Setup(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !prov.Enabled() {
		t.Fatal("an endpoint is set but telemetry is off")
	}
	_, span := otel.Tracer("test").Start(ctx, "run")
	span.End()
	counter, err := otel.Meter("test").Int64Counter("apothecary_runs_total")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(ctx, 1)
	if err := prov.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	col.mu.Lock()
	defer col.mu.Unlock()
	if len(col.spans) != 1 || col.spans[0] != "run" {
		t.Errorf("collector saw spans %v", col.spans)
	}
	sawRuns := false
	for _, m := range col.metrics {
		sawRuns = sawRuns || m == "apothecary_runs_total"
	}
	if !sawRuns {
		t.Errorf("collector saw no apothecary_runs_total in %v", col.metrics)
	}
}

// With no endpoint (or the SDK disabled) Setup leaves the no-op globals
// alone and shutting down is free.
func TestSetupDisabledTouchesNothing(t *testing.T) {
	for name, env := range map[string][2]string{
		"no endpoint":  {"", ""},
		"sdk disabled": {"http://127.0.0.1:1", "true"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", env[0])
			t.Setenv("OTEL_SDK_DISABLED", env[1])
			restoreGlobals(t)
			tp, mp, lp := otel.GetTracerProvider(), otel.GetMeterProvider(), global.GetLoggerProvider()
			prov, err := Setup(context.Background(), "test")
			if err != nil {
				t.Fatal(err)
			}
			if prov.Enabled() {
				t.Error("enabled without an endpoint")
			}
			if otel.GetTracerProvider() != tp || otel.GetMeterProvider() != mp || global.GetLoggerProvider() != lp {
				t.Error("disabled Setup replaced a global provider")
			}
			if _, ok := otel.GetTextMapPropagator().(propagation.TraceContext); ok {
				t.Error("disabled Setup installed a propagator")
			}
			if prov.LoggerProvider() == nil {
				t.Error("no logger provider")
			}
			if err := prov.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
}

// The IDs are the documented sha256 slices, so anyone holding a run ID can
// find its marker trace and every attempt's trace.
func TestRunIDsAreDerivedFromTheRunID(t *testing.T) {
	sum := sha256.Sum256([]byte("r1"))
	if got := RunTraceID("r1"); string(got[:]) != string(sum[0:16]) {
		t.Errorf("trace id = %s", got)
	}
	if got := RunSpanID("r1"); string(got[:]) != string(sum[16:24]) {
		t.Errorf("span id = %s", got)
	}
	stage := sha256.Sum256([]byte("r1\x00build\x002"))
	if got := StageTraceID("r1", "build", 2); string(got[:]) != string(stage[0:16]) {
		t.Errorf("stage trace id = %s", got)
	}
	if got := StageSpanID("r1", "build", 2); string(got[:]) != string(stage[16:24]) {
		t.Errorf("stage span id = %s", got)
	}
	if StageTraceID("r1", "build", 1) == StageTraceID("r1", "build", 2) {
		t.Error("iterations share a trace")
	}
	for _, c := range []struct {
		sc  trace.SpanContext
		tid trace.TraceID
		sid trace.SpanID
	}{
		{RunSpanContext("r1"), RunTraceID("r1"), RunSpanID("r1")},
		{StageSpanContext("r1", "build", 2), StageTraceID("r1", "build", 2), StageSpanID("r1", "build", 2)},
	} {
		if !c.sc.IsValid() || !c.sc.IsRemote() || !c.sc.IsSampled() || c.sc.TraceID() != c.tid || c.sc.SpanID() != c.sid {
			t.Errorf("span context %+v", c.sc)
		}
	}
}

// A WithIDs context forces the root's IDs and a child's span ID under that
// trace; anything else is random, and the forcing does not leak to children
// kept on a fresh context.
func TestIDGenForcesOnlyWhatWithIDsAsks(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans), sdktrace.WithIDGenerator(idGen{}))
	tr := tp.Tracer("test")
	tid, sid := RunTraceID("r1"), RunSpanID("r1")

	_, root := tr.Start(WithIDs(context.Background(), tid, sid), "run")
	if root.SpanContext().TraceID() != tid || root.SpanContext().SpanID() != sid {
		t.Errorf("root = %+v", root.SpanContext())
	}
	parent := trace.ContextWithSpan(context.Background(), root)
	stage := StageSpanID("r1", "build", 1)
	_, child := tr.Start(WithIDs(parent, tid, stage), "build")
	if child.SpanContext().TraceID() != tid || child.SpanContext().SpanID() != stage {
		t.Errorf("forced child = %+v", child.SpanContext())
	}
	_, free := tr.Start(parent, "free")
	if free.SpanContext().TraceID() != tid || free.SpanContext().SpanID() == sid || free.SpanContext().SpanID() == stage {
		t.Errorf("unforced child = %+v", free.SpanContext())
	}
	// A forced pair for another trace does not hijack this one's child.
	_, other := tr.Start(WithIDs(parent, RunTraceID("r2"), stage), "other")
	if other.SpanContext().SpanID() == stage {
		t.Error("forced span id leaked across traces")
	}
	_, random := tr.Start(context.Background(), "random")
	if random.SpanContext().TraceID() == tid || !random.SpanContext().IsValid() {
		t.Errorf("random root = %+v", random.SpanContext())
	}
}
