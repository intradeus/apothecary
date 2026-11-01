package telemetry

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	colmetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"

	"apothecary/internal/run/event"
)

// rig is a Telemetry whose providers export to memory, so a test reads back
// exactly what a collector would have been sent.
type rig struct {
	*Telemetry
	spans  *tracetest.InMemoryExporter
	reader *sdkmetric.ManualReader
}

func newRig(t *testing.T) *rig {
	t.Helper()
	spans := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	tel := &Telemetry{tp: tp, mp: mp, tracer: tp.Tracer("test"), runs: map[string]*runTrace{}}
	meter := mp.Meter("test")
	var err error
	if tel.runsTotal, err = meter.Int64Counter("runs"); err != nil {
		t.Fatal(err)
	}
	if tel.pingsTotal, err = meter.Int64Counter("pings"); err != nil {
		t.Fatal(err)
	}
	if tel.stageCost, err = meter.Float64Histogram("cost"); err != nil {
		t.Fatal(err)
	}
	if tel.stageTurns, err = meter.Int64Histogram("turns"); err != nil {
		t.Fatal(err)
	}
	return &rig{Telemetry: tel, spans: spans, reader: reader}
}

func (r *rig) ended(t *testing.T, name string) tracetest.SpanStub {
	t.Helper()
	for _, s := range r.spans.GetSpans() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no ended span %q in %v", name, r.spanNames())
	return tracetest.SpanStub{}
}

func (r *rig) spanNames() []string {
	var out []string
	for _, s := range r.spans.GetSpans() {
		out = append(out, s.Name)
	}
	return out
}

// metrics reads every metric the rig collected, by name.
func (r *rig) metrics(t *testing.T) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

// counted is the sum of one attribute value of an Int64 counter.
func counted(t *testing.T, m metricdata.Metrics, key, value string) int64 {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %s is %T", m.Name, m.Data)
	}
	var n int64
	for _, dp := range sum.DataPoints {
		if v, ok := dp.Attributes.Value(attrKey(key)); ok && v.AsString() == value {
			n += dp.Value
		}
	}
	return n
}

func attrKey(k string) attribute.Key { return attribute.Key(k) }

var at = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func ev(runID string, offset time.Duration, p event.Payload) event.Event {
	return event.Event{RunID: runID, TS: at.Add(offset), Type: p.EventType(), Payload: p}
}

func spanAttr(s tracetest.SpanStub, key string) (string, bool) {
	for _, a := range s.Attributes {
		if string(a.Key) == key {
			return a.Value.Emit(), true
		}
	}
	return "", false
}

// A run's stage attempt is a child of the run's span, with the stage's
// attributes, its cost and turns on the histograms and its end at the event's
// time, not at the clock's.
func TestStageAttemptIsAChildSpanOfTheRun(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", 0, &event.RunCreated{FlowName: "delivery", TicketTitle: "add login"}))
	r.OnEvent(ev("r1", time.Second, &event.StageStarted{StageID: "build", Iteration: 2, Harness: "fake", Model: "fake/model"}))
	r.OnEvent(ev("r1", 3*time.Second, &event.StageCompleted{StageID: "build", TurnsUsed: 4, Usage: &event.Usage{CostUSD: 0.5}}))
	r.OnEvent(ev("r1", 4*time.Second, &event.RunCompleted{}))

	stage := r.ended(t, "build")
	run := r.ended(t, "run")
	if stage.Parent.SpanID() != run.SpanContext.SpanID() {
		t.Errorf("stage's parent = %s, want the run span %s", stage.Parent.SpanID(), run.SpanContext.SpanID())
	}
	if stage.Status.Code != codes.Ok {
		t.Errorf("stage status = %v, want ok", stage.Status)
	}
	if !stage.StartTime.Equal(at.Add(time.Second)) || !stage.EndTime.Equal(at.Add(3*time.Second)) {
		t.Errorf("stage ran %v–%v, want the events' times", stage.StartTime, stage.EndTime)
	}
	for k, want := range map[string]string{
		"apothecary.stage": "build", "apothecary.harness": "fake", "apothecary.model": "fake/model",
		"apothecary.iteration": "2", "apothecary.run_id": "r1",
	} {
		if got, _ := spanAttr(stage, k); got != want {
			t.Errorf("stage %s = %q, want %q", k, got, want)
		}
	}
	if got, _ := spanAttr(run, "apothecary.flow"); got != "delivery" {
		t.Errorf("run flow = %q", got)
	}
	if got, _ := spanAttr(run, "apothecary.ticket"); got != "add login" {
		t.Errorf("run ticket = %q", got)
	}

	m := r.metrics(t)
	if got := counted(t, m["runs"], "state", "completed"); got != 1 {
		t.Errorf("runs{completed} = %d", got)
	}
	cost := m["cost"].Data.(metricdata.Histogram[float64]).DataPoints
	if len(cost) != 1 || cost[0].Sum != 0.5 {
		t.Fatalf("cost histogram = %+v", cost)
	}
	// The stage's metrics carry what stage_started said, which
	// stage_completed does not repeat.
	if v, _ := cost[0].Attributes.Value(attrKey("harness")); v.AsString() != "fake" {
		t.Errorf("cost harness = %q", v.AsString())
	}
	if v, _ := cost[0].Attributes.Value(attrKey("model")); v.AsString() != "fake/model" {
		t.Errorf("cost model = %q", v.AsString())
	}
	turns := m["turns"].Data.(metricdata.Histogram[int64]).DataPoints
	if len(turns) != 1 || turns[0].Sum != 4 {
		t.Fatalf("turns histogram = %+v", turns)
	}
}

// A stage that spent nothing records no zero sample: an attempt with no usage
// and no turns would drag every percentile down.
func TestStageWithoutUsageRecordsNoSample(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", 0, &event.StageStarted{StageID: "verify"}))
	r.OnEvent(ev("r1", time.Second, &event.StageCompleted{StageID: "verify"}))
	m := r.metrics(t)
	if _, ok := m["cost"]; ok {
		t.Errorf("a cost sample without usage: %+v", m["cost"])
	}
	if _, ok := m["turns"]; ok {
		t.Errorf("a turns sample without turns: %+v", m["turns"])
	}
}

func TestStageFailureMarksTheSpanAndKeepsTheRunOpen(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", 0, &event.StageStarted{StageID: "build"}))
	r.OnEvent(ev("r1", time.Second, &event.StageFailed{StageID: "build", Reason: "exploded"}))
	s := r.ended(t, "build")
	if s.Status.Code != codes.Error || s.Status.Description != "exploded" {
		t.Errorf("status = %+v", s.Status)
	}
	if got := r.spanNames(); len(got) != 1 {
		t.Errorf("ended spans = %v: a failed stage does not end the run", got)
	}
	if _, ok := r.runs["r1"]; !ok {
		t.Error("the run's trace was dropped by a stage failure")
	}
}

func TestRunsEndWithTheirState(t *testing.T) {
	cases := []struct {
		name    string
		p       event.Payload
		state   string
		code    codes.Code
		message string
	}{
		{"completed", &event.RunCompleted{}, "completed", codes.Ok, ""},
		{"abandoned", &event.RunAbandoned{Reason: "by a person"}, "abandoned", codes.Error, "by a person"},
		{"failed", &event.RunFailed{Reason: "out of budget"}, "failed", codes.Error, "out of budget"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.OnEvent(ev("r1", 0, &event.StageStarted{StageID: "build"}))
			r.OnEvent(ev("r1", time.Second, c.p))
			run := r.ended(t, "run")
			if run.Status.Code != c.code || run.Status.Description != c.message {
				t.Errorf("run status = %+v", run.Status)
			}
			// The stage left open ends with the run, naming why.
			stage := r.ended(t, "build")
			if stage.Status.Code != c.code || (c.code == codes.Error && stage.Status.Description != "run "+c.state) {
				t.Errorf("open stage status = %+v", stage.Status)
			}
			if _, ok := r.runs["r1"]; ok {
				t.Error("a finished run stays tracked")
			}
			if got := counted(t, r.metrics(t)["runs"], "state", c.state); got != 1 {
				t.Errorf("runs{%s} = %d", c.state, got)
			}
		})
	}
}

// An attempt the log left open (a retried stage after a crash) is closed by
// the next one, not leaked until the run ends.
func TestANewAttemptClosesTheOneLeftOpen(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", 0, &event.StageStarted{StageID: "build", Iteration: 1}))
	r.OnEvent(ev("r1", time.Second, &event.StageStarted{StageID: "build", Iteration: 2}))
	spans := r.spans.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %v, want the abandoned attempt", r.spanNames())
	}
	if got, _ := spanAttr(spans[0], "apothecary.iteration"); got != "1" {
		t.Errorf("ended iteration %s, want the first", got)
	}
	if !spans[0].EndTime.Equal(at.Add(time.Second)) {
		t.Errorf("ended at %v", spans[0].EndTime)
	}
	if r.runs["r1"].stage == nil {
		t.Error("the new attempt has no open span")
	}
}

// A stage_completed with no stage_started seen (a run recovered mid-stage)
// has nothing to close and still counts.
func TestCompletionWithoutAnOpenStage(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", 0, &event.StageCompleted{StageID: "build", TurnsUsed: 2}))
	if got := r.spanNames(); len(got) != 0 {
		t.Errorf("ended spans = %v", got)
	}
	if turns := r.metrics(t)["turns"].Data.(metricdata.Histogram[int64]).DataPoints; len(turns) != 1 || turns[0].Sum != 2 {
		t.Fatalf("turns = %+v", turns)
	}
}

// A run recovered at boot has no run_created this session: the first event
// seen opens its span, at that event's time.
func TestRecoveredRunGetsASpanFromTheFirstEventSeen(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", time.Hour, &event.PingRaised{Type: "human_input"}))
	r.OnEvent(ev("r1", 2*time.Hour, &event.RunCompleted{}))
	run := r.ended(t, "run")
	if !run.StartTime.Equal(at.Add(time.Hour)) {
		t.Errorf("run span starts %v", run.StartTime)
	}
	if got, _ := spanAttr(run, "apothecary.run_id"); got != "r1" {
		t.Errorf("run id = %q", got)
	}
}

func TestPingsAreCountedByType(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", 0, &event.PingRaised{Type: "human_input"}))
	r.OnEvent(ev("r2", 0, &event.PingRaised{Type: "human_input"}))
	r.OnEvent(ev("r1", 0, &event.PingRaised{Type: "hitl"}))
	m := r.metrics(t)["pings"]
	if got := counted(t, m, "type", "human_input"); got != 2 {
		t.Errorf("human_input = %d", got)
	}
	if got := counted(t, m, "type", "hitl"); got != 1 {
		t.Errorf("hitl = %d", got)
	}
}

// Runs do not share traces: two at once keep their own open stage.
func TestRunsAreTrackedApart(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", 0, &event.StageStarted{StageID: "one"}))
	r.OnEvent(ev("r2", 0, &event.StageStarted{StageID: "two"}))
	r.OnEvent(ev("r1", time.Second, &event.StageCompleted{StageID: "one"}))
	if got := r.spanNames(); len(got) != 1 || got[0] != "one" {
		t.Errorf("ended = %v", got)
	}
	if r.runs["r2"].stage == nil {
		t.Error("r2's stage ended with r1's")
	}
}

// Shutdown closes whatever is open: a stopping daemon loses no span.
func TestShutdownEndsOpenSpans(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", 0, &event.StageStarted{StageID: "build"}))
	r.OnEvent(ev("r2", 0, &event.RunCreated{}))
	// The in-memory exporter empties itself on shutdown: read it as it goes.
	seen := &recordingProcessor{}
	r.tp.RegisterSpanProcessor(seen)
	r.Shutdown(context.Background())
	if len(r.runs) != 0 {
		t.Errorf("runs left tracked: %v", r.runs)
	}
	names := seen.names()
	want := map[string]int{"build": 1, "run": 2}
	got := map[string]int{}
	for _, n := range names {
		got[n]++
	}
	if len(got) != len(want) || got["build"] != 1 || got["run"] != 2 {
		t.Errorf("ended at shutdown = %v", names)
	}
}

type recordingProcessor struct {
	mu    sync.Mutex
	ended []string
}

func (p *recordingProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (p *recordingProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ended = append(p.ended, s.Name())
}
func (p *recordingProcessor) Shutdown(context.Context) error   { return nil }
func (p *recordingProcessor) ForceFlush(context.Context) error { return nil }
func (p *recordingProcessor) names() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.ended...)
}

// collector is a loopback OTLP/gRPC collector: what New's exporters really
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

// metricsServer exists because both OTLP services call their rpc Export.
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

// New exports over OTLP/gRPC under the four metric names dashboards read, and
// Shutdown is what flushes them.
func TestNewExportsToTheCollectorOnShutdown(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	col := &collector{}
	srv := grpc.NewServer()
	coltrace.RegisterTraceServiceServer(srv, col)
	colmetric.RegisterMetricsServiceServer(srv, metricsServer{c: col})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.Stop()
		<-done
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tel, err := New(ctx, lis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	tel.OnEvent(ev("r1", 0, &event.RunCreated{FlowName: "delivery"}))
	tel.OnEvent(ev("r1", time.Second, &event.StageStarted{StageID: "build", Harness: "fake", Model: "fake/model"}))
	tel.OnEvent(ev("r1", 2*time.Second, &event.StageCompleted{StageID: "build", TurnsUsed: 1, Usage: &event.Usage{CostUSD: 0.1}}))
	tel.OnEvent(ev("r1", 2*time.Second, &event.PingRaised{Type: "human_input"}))
	tel.OnEvent(ev("r1", 3*time.Second, &event.RunCompleted{}))
	tel.Shutdown(ctx)

	col.mu.Lock()
	defer col.mu.Unlock()
	gotSpans := map[string]bool{}
	for _, s := range col.spans {
		gotSpans[s] = true
	}
	if !gotSpans["run"] || !gotSpans["build"] {
		t.Errorf("collector saw spans %v", col.spans)
	}
	gotMetrics := map[string]bool{}
	for _, m := range col.metrics {
		gotMetrics[m] = true
	}
	for _, want := range []string{"apothecary_runs_total", "apothecary_pings_total", "apothecary_stage_cost_usd", "apothecary_stage_turns"} {
		if !gotMetrics[want] {
			t.Errorf("collector saw no %s in %v", want, col.metrics)
		}
	}
}

// An endpoint gRPC cannot parse is refused at boot, named for the exporter
// that refused it, rather than dropping every span silently.
func TestNewRefusesAnEndpointItCannotParse(t *testing.T) {
	_, err := New(context.Background(), "collector:\x00")
	if err == nil || !strings.Contains(err.Error(), "otel trace exporter") {
		t.Fatalf("err = %v", err)
	}
}

// An event landing after a run ended (its env_destroyed from the reaper, its
// run_archived) opens no new trace: nothing would ever end it.
func TestAnEventAfterTheEndOpensNoSpan(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", 0, &event.RunCompleted{}))
	r.OnEvent(ev("r1", time.Hour, &event.EnvDestroyed{EnvID: "env-1"}))
	r.OnEvent(ev("r1", 2*time.Hour, &event.RunArchived{}))
	if _, tracked := r.runs["r1"]; tracked {
		t.Error("a finished run is tracked again")
	}
}

// A retry is new work on a finished run: it gets a trace, unlike the
// housekeeping that lands after the end.
func TestARetryAfterTheEndOpensATrace(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", 0, &event.RunFailed{Reason: "boom"}))
	r.OnEvent(ev("r1", time.Minute, &event.EnvRebuildCompleted{EnvID: "env-1"}))
	if _, tracked := r.runs["r1"]; tracked {
		t.Fatal("housekeeping opened a trace")
	}
	r.OnEvent(ev("r1", time.Hour, &event.RunRetried{StageID: "s"}))
	if _, tracked := r.runs["r1"]; !tracked {
		t.Error("a retried run has no trace")
	}
	// Housekeeping during a live run lands on its trace as before.
	r.OnEvent(ev("r1", 2*time.Hour, &event.EnvRebuildStarted{EnvID: "env-1"}))
	if _, tracked := r.runs["r1"]; !tracked {
		t.Error("housekeeping closed a live trace")
	}
}
