package telemetry

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	ptel "apothecary/internal/platform/telemetry"
	"apothecary/internal/run/event"
)

// rig is a Telemetry whose providers export to memory, so a test reads back
// exactly what a collector would have been sent.
type rig struct {
	*Telemetry
	tp     *sdktrace.TracerProvider
	spans  *tracetest.InMemoryExporter
	reader *sdkmetric.ManualReader
}

func newRig(t *testing.T) *rig {
	t.Helper()
	spans := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans), sdktrace.WithIDGenerator(ptel.IDGenerator()))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	tel, err := New(tp, mp)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{Telemetry: tel, tp: tp, spans: spans, reader: reader}
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

// A stage attempt is the root of its own trace, linked to the run's marker
// span (instant, at run_created), with the stage's attributes, its cost and
// turns on the histograms and its end at the event's time, not the clock's.
func TestStageAttemptIsItsOwnTraceLinkedToTheRun(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", 0, &event.RunCreated{FlowName: "delivery", TicketTitle: "add login"}))
	r.OnEvent(ev("r1", time.Second, &event.StageStarted{StageID: "build", Iteration: 2, Harness: "fake", Model: "fake/model"}))
	r.OnEvent(ev("r1", 3*time.Second, &event.StageCompleted{StageID: "build", TurnsUsed: 4, Usage: &event.Usage{CostUSD: 0.5}}))
	r.OnEvent(ev("r1", 4*time.Second, &event.RunCompleted{}))

	stage := r.ended(t, "build")
	run := r.ended(t, "run")
	if !run.StartTime.Equal(at) || !run.EndTime.Equal(at) {
		t.Errorf("run marker spans %v–%v, want the instant of run_created", run.StartTime, run.EndTime)
	}
	if stage.Parent.IsValid() || stage.SpanContext.TraceID() == run.SpanContext.TraceID() {
		t.Errorf("stage in trace %s under %s, want a root of its own", stage.SpanContext.TraceID(), stage.Parent.SpanID())
	}
	if len(stage.Links) != 1 || !stage.Links[0].SpanContext.Equal(trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: run.SpanContext.TraceID(), SpanID: run.SpanContext.SpanID(), TraceFlags: trace.FlagsSampled, Remote: true,
	})) {
		t.Errorf("stage links = %+v, want the run marker", stage.Links)
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
	if _, ok := spanAttr(run, "apothecary.ticket"); ok {
		t.Error("run span carries the ticket title")
	}

	m := r.metrics(t)
	if got := counted(t, m["apothecary_runs_total"], "state", "completed"); got != 1 {
		t.Errorf("runs{completed} = %d", got)
	}
	cost := m["apothecary_stage_cost_usd"].Data.(metricdata.Histogram[float64]).DataPoints
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
	turns := m["apothecary_stage_turns"].Data.(metricdata.Histogram[int64]).DataPoints
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
	if _, ok := m["apothecary_stage_cost_usd"]; ok {
		t.Errorf("a cost sample without usage: %+v", m["apothecary_stage_cost_usd"])
	}
	if _, ok := m["apothecary_stage_turns"]; ok {
		t.Errorf("a turns sample without turns: %+v", m["apothecary_stage_turns"])
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
			// The attempt left open ends with the run, naming its state and
			// why: the marker span ended at creation.
			stage := r.ended(t, "build")
			if stage.Status.Code != c.code || stage.Status.Description != c.message {
				t.Errorf("open stage status = %+v", stage.Status)
			}
			if got, _ := spanAttr(stage, "apothecary.run_state"); got != c.state {
				t.Errorf("run_state = %q", got)
			}
			if got := r.spanNames(); len(got) != 1 {
				t.Errorf("ended spans = %v", got)
			}
			if _, ok := r.runs["r1"]; ok {
				t.Error("a finished run stays tracked")
			}
			if got := counted(t, r.metrics(t)["apothecary_runs_total"], "state", c.state); got != 1 {
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
	if turns := r.metrics(t)["apothecary_stage_turns"].Data.(metricdata.Histogram[int64]).DataPoints; len(turns) != 1 || turns[0].Sum != 2 {
		t.Fatalf("turns = %+v", turns)
	}
}

// A run recovered at boot has no run_created this session: it gets no span
// of its own (its marker was exported when it was created), and its attempts
// still link to that marker by derived ID.
func TestRecoveredRunLinksItsAttemptsToTheMarker(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", time.Hour, &event.PingRaised{Type: "human_input"}))
	r.OnEvent(ev("r1", 2*time.Hour, &event.StageStarted{StageID: "build", Iteration: 3}))
	r.OnEvent(ev("r1", 3*time.Hour, &event.StageCompleted{StageID: "build"}))
	r.OnEvent(ev("r1", 3*time.Hour, &event.RunCompleted{}))
	if got := r.spanNames(); len(got) != 1 || got[0] != "build" {
		t.Fatalf("ended spans = %v, want only the attempt", got)
	}
	stage := r.ended(t, "build")
	if len(stage.Links) != 1 || stage.Links[0].SpanContext.TraceID() != ptel.RunTraceID("r1") ||
		stage.Links[0].SpanContext.SpanID() != ptel.RunSpanID("r1") {
		t.Errorf("stage links = %+v, want the run marker", stage.Links)
	}
}

func TestPingsAreCountedByType(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", 0, &event.PingRaised{Type: "human_input"}))
	r.OnEvent(ev("r2", 0, &event.PingRaised{Type: "human_input"}))
	r.OnEvent(ev("r1", 0, &event.PingRaised{Type: "hitl"}))
	m := r.metrics(t)["apothecary_pings_total"]
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
	// r2's marker ended when it was created; only r1's attempt was open.
	if names := seen.names(); len(names) != 1 || names[0] != "build" {
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

// The run marker and each stage attempt take the IDs derived from the run,
// so work spans the engine parents on StageSpanContext land under the
// attempt — as children with IDs of their own.
func TestSpansTakeTheRunsDerivedIDs(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", 0, &event.RunCreated{FlowName: "delivery", FlowHash: "h1", ProjectID: "p1", UserID: "u1",
		TicketID: "t1", TicketChangeType: "feature", By: "human", Skillsets: []event.SkillsetPin{{Name: "go", Seq: 3}}}))
	r.OnEvent(ev("r1", time.Second, &event.StageStarted{StageID: "build", Iteration: 2, Model: "m", Agent: "coder", AgentHash: "ah"}))
	// What the engine starts under the attempt before the sink exports it.
	_, invoke := r.tracer.Start(trace.ContextWithRemoteSpanContext(context.Background(), ptel.StageSpanContext("r1", "build", 2)), "agent.invoke")
	invoke.End()
	r.OnEvent(ev("r1", 2*time.Second, &event.StageCompleted{StageID: "build", Usage: &event.Usage{InputTokens: 10, OutputTokens: 5}}))
	r.OnEvent(ev("r1", 3*time.Second, &event.RunCompleted{}))

	run, stage, child := r.ended(t, "run"), r.ended(t, "build"), r.ended(t, "agent.invoke")
	if run.SpanContext.TraceID() != ptel.RunTraceID("r1") || run.SpanContext.SpanID() != ptel.RunSpanID("r1") {
		t.Errorf("run span = %s/%s", run.SpanContext.TraceID(), run.SpanContext.SpanID())
	}
	if run.Parent.IsValid() {
		t.Errorf("run span has a parent %s", run.Parent.SpanID())
	}
	if stage.SpanContext.TraceID() != ptel.StageTraceID("r1", "build", 2) || stage.SpanContext.SpanID() != ptel.StageSpanID("r1", "build", 2) {
		t.Errorf("stage span = %s/%s", stage.SpanContext.TraceID(), stage.SpanContext.SpanID())
	}
	if child.SpanContext.TraceID() != stage.SpanContext.TraceID() || child.Parent.SpanID() != stage.SpanContext.SpanID() ||
		child.SpanContext.SpanID() == stage.SpanContext.SpanID() {
		t.Errorf("agent.invoke %s/%s under %s, want a child of the attempt", child.SpanContext.TraceID(), child.SpanContext.SpanID(), child.Parent.SpanID())
	}
	for k, want := range map[string]string{
		"apothecary.project_id": "p1", "apothecary.user_id": "u1", "apothecary.ticket_id": "t1",
		"apothecary.flow_hash": "h1", "apothecary.ticket_change_type": "feature", "apothecary.started_by": "human",
		"apothecary.skillsets": `["go@3"]`,
	} {
		if got, _ := spanAttr(run, k); got != want {
			t.Errorf("run %s = %q, want %q", k, got, want)
		}
	}
	for k, want := range map[string]string{
		"gen_ai.request.model": "m", "gen_ai.agent.name": "coder", "apothecary.agent_hash": "ah",
		"gen_ai.usage.input_tokens": "10", "gen_ai.usage.output_tokens": "5",
	} {
		if got, _ := spanAttr(stage, k); got != want {
			t.Errorf("stage %s = %q, want %q", k, got, want)
		}
	}
}

// histCount is how many samples a histogram took with one attribute value
// ("" key = all).
func histCount(t *testing.T, m metricdata.Metrics, key, value string) (n uint64, sum float64) {
	t.Helper()
	switch d := m.Data.(type) {
	case metricdata.Histogram[float64]:
		for _, dp := range d.DataPoints {
			if v, ok := dp.Attributes.Value(attrKey(key)); key == "" || ok && v.AsString() == value {
				n, sum = n+dp.Count, sum+dp.Sum
			}
		}
	case metricdata.Histogram[int64]:
		for _, dp := range d.DataPoints {
			if v, ok := dp.Attributes.Value(attrKey(key)); key == "" || ok && v.AsString() == value {
				n, sum = n+dp.Count, sum+float64(dp.Sum)
			}
		}
	default:
		t.Fatalf("metric %s is %T", m.Name, m.Data)
	}
	return n, sum
}

// One run through the lifecycle records every A4 instrument from its events.
func TestLifecycleMetrics(t *testing.T) {
	r := newRig(t)
	r.OnEvent(ev("r1", 0, &event.RunCreated{}))
	r.OnEvent(ev("r1", 2*time.Second, &event.EnvRequested{}))
	r.OnEvent(ev("r1", 5*time.Second, &event.EnvReady{}))
	r.OnEvent(ev("r1", 6*time.Second, &event.StageStarted{StageID: "build", Iteration: 1, Harness: "h", Model: "m"}))
	r.OnEvent(ev("r1", 7*time.Second, &event.PingRaised{PingID: "p1", Type: "gate"}))
	r.OnEvent(ev("r1", 10*time.Second, &event.PingAnswered{PingID: "p1"}))
	r.OnEvent(ev("r1", 11*time.Second, &event.StagePaused{StageID: "build", Usage: &event.Usage{InputTokens: 1, OutputTokens: 2, CostUSD: 0.1}}))
	r.OnEvent(ev("r1", 12*time.Second, &event.HoldTaken{}))
	r.OnEvent(ev("r1", 16*time.Second, &event.StageFailed{StageID: "build", Reason: "timeout", Usage: &event.Usage{InputTokens: 3, OutputTokens: 4, CostUSD: 0.2}}))
	r.OnEvent(ev("r1", 16*time.Second, &event.EdgeTaken{Edge: "on_fail"}))
	r.OnEvent(ev("r1", 17*time.Second, &event.StageStarted{StageID: "build", Iteration: 2, Harness: "h", Model: "m"}))
	r.OnEvent(ev("r1", 18*time.Second, &event.StageCompleted{StageID: "build", Skipped: true}))
	r.OnEvent(ev("r1", 19*time.Second, &event.RunAbandoned{Reason: "budget exceeded: $1"}))
	m := r.metrics(t)

	if got := counted(t, m["apothecary.stage.attempts"], "stage", "build"); got != 2 {
		t.Errorf("attempts = %d", got)
	}
	if n, sum := histCount(t, m["apothecary.stage.duration"], "outcome", "failed"); n != 1 || sum != 10 {
		t.Errorf("failed duration n=%d sum=%v", n, sum)
	}
	if n, _ := histCount(t, m["apothecary.stage.duration"], "outcome", "skipped"); n != 1 {
		t.Errorf("skipped duration n=%d", n)
	}
	if got := counted(t, m["apothecary.stage.failures"], "reason", "timeout"); got != 1 {
		t.Errorf("failures = %d", got)
	}
	if got := counted(t, m["apothecary.edges"], "edge", "on_fail"); got != 1 {
		t.Errorf("edges = %d", got)
	}
	if got := m["apothecary.budget.stops"].Data.(metricdata.Sum[int64]).DataPoints[0].Value; got != 1 {
		t.Errorf("budget stops = %d", got)
	}
	if got := m["apothecary.holds"].Data.(metricdata.Sum[int64]).DataPoints[0].Value; got != 1 {
		t.Errorf("holds = %d", got)
	}
	if n, sum := histCount(t, m["gen_ai.client.token.usage"], "gen_ai.token.type", "input"); n != 2 || sum != 4 {
		t.Errorf("input tokens n=%d sum=%v", n, sum)
	}
	if n, sum := histCount(t, m["gen_ai.client.token.usage"], "gen_ai.token.type", "output"); n != 2 || sum != 6 {
		t.Errorf("output tokens n=%d sum=%v", n, sum)
	}
	cost := m["apothecary.llm.cost"].Data.(metricdata.Sum[float64]).DataPoints
	if len(cost) != 1 || cost[0].Value < 0.2999 || cost[0].Value > 0.3001 {
		t.Errorf("llm cost = %+v", cost)
	}
	if n, sum := histCount(t, m["apothecary.run.queue_wait"], "", ""); n != 1 || sum != 2 {
		t.Errorf("queue wait n=%d sum=%v", n, sum)
	}
	if n, sum := histCount(t, m["apothecary.workspace.provision"], "outcome", "ready"); n != 1 || sum != 3 {
		t.Errorf("provision n=%d sum=%v", n, sum)
	}
	if n, sum := histCount(t, m["apothecary.ping.answer_wait"], "type", "gate"); n != 1 || sum != 3 {
		t.Errorf("answer wait n=%d sum=%v", n, sum)
	}
}
