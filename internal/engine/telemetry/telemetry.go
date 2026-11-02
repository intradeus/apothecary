// Package telemetry implements §7: the engine emits OTel — a marker span
// per run, a trace per stage attempt, and the apothecary metrics — all
// DERIVED FROM THE EVENT STREAM, so harness-native telemetry is never
// load-bearing and the emission is a pure projection of the source of
// truth. The providers are the process globals platform/telemetry installs
// (no-op when OTEL_* is unset), so the sink is always wired.
//
// A run lasts hours or days, parked on humans; one trace over it would grow
// without bound and only export at the end. So the run is an instant "run"
// span (its attributes, at run_created) in a trace of its own, and each stage
// attempt is the root of its own trace, linked to the run span. IDs are
// derived from the run (platform/telemetry): the engine's work spans under an
// attempt (agent invocations, queries, outbound calls) parent on the attempt
// before the sink has exported it, and a restart changes no ID.
package telemetry

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	ptel "apothecary/internal/platform/telemetry"
	"apothecary/internal/run/event"
)

type Telemetry struct {
	tracer trace.Tracer

	runsTotal  metric.Int64Counter
	pingsTotal metric.Int64Counter
	stageCost  metric.Float64Histogram
	stageTurns metric.Int64Histogram

	stageAttempts metric.Int64Counter
	stageDuration metric.Float64Histogram
	stageFailures metric.Int64Counter
	edges         metric.Int64Counter
	budgetStops   metric.Int64Counter
	tokenUsage    metric.Int64Histogram
	llmCost       metric.Float64Counter
	queueWait     metric.Float64Histogram
	provision     metric.Float64Histogram
	answerWait    metric.Float64Histogram
	holds         metric.Int64Counter

	mu   sync.Mutex
	runs map[string]*runTrace
}

type runTrace struct {
	stage trace.Span // open stage attempt, if any
	// attrs of the open stage (from stage_started), reused on completion.
	stageID        string
	harness        string
	model          string
	stageStartedAt time.Time
	// usage the open attempt has banked so far, across its paused legs.
	inputTokens  int
	outputTokens int

	createdAt      time.Time // run_created seen this session; zero for a recovered run
	envRequestedAt time.Time // the open provisioning, if any
	pings          map[string]pingClock
}

type pingClock struct {
	typ string
	at  time.Time
}

// New builds the sink on the given providers. Exporting, flushing and the
// no-op fallback are the providers' business, not the sink's.
func New(tp trace.TracerProvider, mp metric.MeterProvider) (*Telemetry, error) {
	t := &Telemetry{tracer: tp.Tracer("apothecaryd"), runs: map[string]*runTrace{}}
	m := mp.Meter("apothecaryd")
	var errs []error
	i64c := func(name, unit, desc string) metric.Int64Counter {
		c, err := m.Int64Counter(name, metric.WithUnit(unit), metric.WithDescription(desc))
		errs = append(errs, err)
		return c
	}
	f64h := func(name, unit, desc string) metric.Float64Histogram {
		h, err := m.Float64Histogram(name, metric.WithUnit(unit), metric.WithDescription(desc))
		errs = append(errs, err)
		return h
	}
	t.runsTotal = i64c("apothecary_runs_total", "", "runs reaching a terminal state, by state")
	t.pingsTotal = i64c("apothecary_pings_total", "", "pings raised, by type")
	t.stageCost = f64h("apothecary_stage_cost_usd", "", "cost per completed stage attempt")
	var err error
	t.stageTurns, err = m.Int64Histogram("apothecary_stage_turns", metric.WithDescription("turns per completed stage attempt"))
	errs = append(errs, err)

	t.stageAttempts = i64c("apothecary.stage.attempts", "{attempt}", "stage attempts started")
	t.stageDuration = f64h("apothecary.stage.duration", "s", "stage attempt duration, by outcome")
	t.stageFailures = i64c("apothecary.stage.failures", "{failure}", "failed stage attempts, by reason")
	t.edges = i64c("apothecary.edges", "{edge}", "failure/rejection edges taken, by edge")
	t.budgetStops = i64c("apothecary.budget.stops", "{run}", "runs abandoned for exceeding their budget")
	t.tokenUsage, err = m.Int64Histogram("gen_ai.client.token.usage", metric.WithUnit("{token}"),
		metric.WithDescription("tokens used per stage leg, by token type"))
	errs = append(errs, err)
	t.llmCost, err = m.Float64Counter("apothecary.llm.cost", metric.WithUnit("USD"),
		metric.WithDescription("model spend, by model and harness"))
	errs = append(errs, err)
	t.queueWait = f64h("apothecary.run.queue_wait", "s", "run_created to env_requested: time spent queued for a slot")
	t.provision = f64h("apothecary.workspace.provision", "s", "env_requested to env_ready/env_failed")
	t.answerWait = f64h("apothecary.ping.answer_wait", "s", "ping raised to answered, by type")
	t.holds = i64c("apothecary.holds", "{hold}", "take-overs started")
	for _, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("telemetry instruments: %w", err)
		}
	}
	return t, nil
}

// Shutdown ends the attempt spans still open so a stopping daemon loses
// none; the providers flush them on their own shutdown.
func (t *Telemetry) Shutdown(context.Context) {
	t.mu.Lock()
	for id, rt := range t.runs {
		if rt.stage != nil {
			rt.stage.End()
		}
		delete(t.runs, id)
	}
	t.mu.Unlock()
}

// run returns (lazily creating) the run's bookkeeping. A run first seen
// without its run_created (recovered at boot, or retried after it ended)
// needs no span of its own: its attempts link to the marker by derived ID.
func (t *Telemetry) run(e *event.Event) *runTrace {
	if rt, ok := t.runs[e.RunID]; ok {
		return rt
	}
	rt := &runTrace{}
	t.runs[e.RunID] = rt
	return rt
}

// OnEvent is the engine sink (§7): every freshly appended event flows
// through here exactly once.
func (t *Telemetry) OnEvent(e event.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ctx := context.Background()
	if _, open := t.runs[e.RunID]; !open && landsAfterTheEnd(e.Payload) {
		return // bookkeeping opened now would have nothing to end it
	}
	rt := t.run(&e)

	switch p := e.Payload.(type) {
	case *event.RunCreated:
		rt.createdAt = e.TS
		pins := make([]string, 0, len(p.Skillsets))
		for _, s := range p.Skillsets {
			pins = append(pins, fmt.Sprintf("%s@%d", s.Name, s.Seq))
		}
		// The marker: instant, under the run's derived IDs. Kept off any
		// context (the forcing ctx would force its children too).
		_, span := t.tracer.Start(ptel.WithIDs(context.Background(), ptel.RunTraceID(e.RunID), ptel.RunSpanID(e.RunID)), "run",
			trace.WithTimestamp(e.TS),
			trace.WithAttributes(
				attribute.String("apothecary.run_id", e.RunID),
				attribute.String("apothecary.project_id", p.ProjectID),
				attribute.String("apothecary.user_id", p.UserID),
				attribute.String("apothecary.ticket_id", p.TicketID),
				attribute.String("apothecary.flow", p.FlowName),
				attribute.String("apothecary.flow_hash", p.FlowHash),
				attribute.String("apothecary.ticket_change_type", p.TicketChangeType),
				attribute.String("apothecary.started_by", p.By),
				attribute.StringSlice("apothecary.skillsets", pins),
			))
		span.End(trace.WithTimestamp(e.TS))
	case *event.EnvRequested:
		rt.envRequestedAt = e.TS
		if !rt.createdAt.IsZero() {
			t.queueWait.Record(ctx, seconds(rt.createdAt, e.TS))
		}
	case *event.EnvReady:
		t.provisioned(rt, e.TS, "ready")
	case *event.EnvFailed:
		t.provisioned(rt, e.TS, "failed")
	case *event.StageStarted:
		if rt.stage != nil { // safety: an attempt left open
			rt.stage.End(trace.WithTimestamp(e.TS))
		}
		// The attempt roots its own trace under derived IDs, so the
		// agent.invoke the engine starts under StageSpanContext is its child;
		// the link leads back to the run.
		start := ptel.WithIDs(context.Background(), ptel.StageTraceID(e.RunID, p.StageID, p.Iteration), ptel.StageSpanID(e.RunID, p.StageID, p.Iteration))
		_, span := t.tracer.Start(start, p.StageID,
			trace.WithTimestamp(e.TS),
			trace.WithLinks(trace.Link{SpanContext: ptel.RunSpanContext(e.RunID)}),
			trace.WithAttributes(
				attribute.String("apothecary.run_id", e.RunID),
				attribute.String("apothecary.stage", p.StageID),
				attribute.Int("apothecary.iteration", p.Iteration),
				attribute.String("apothecary.harness", p.Harness),
				attribute.String("apothecary.model", p.Model),
				attribute.String("gen_ai.request.model", p.Model),
				attribute.String("gen_ai.agent.name", p.Agent),
				attribute.String("apothecary.agent_hash", p.AgentHash),
			))
		rt.stage, rt.stageID, rt.harness, rt.model = span, p.StageID, p.Harness, p.Model
		rt.stageStartedAt, rt.inputTokens, rt.outputTokens = e.TS, 0, 0
		t.stageAttempts.Add(ctx, 1, metric.WithAttributes(
			attribute.String("stage", p.StageID),
			attribute.String("harness", p.Harness),
			attribute.String("model", p.Model),
		))
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
		t.usage(rt, p.Usage)
		outcome := "completed"
		if p.Skipped {
			outcome = "skipped"
		}
		t.endStage(rt, e.TS, outcome, codes.Ok, "")
	case *event.StageFailed:
		t.stageFailures.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", p.Reason)))
		t.usage(rt, p.Usage)
		t.endStage(rt, e.TS, "failed", codes.Error, p.Reason)
	case *event.StagePaused:
		t.usage(rt, p.Usage)
	case *event.EdgeTaken:
		t.edges.Add(ctx, 1, metric.WithAttributes(attribute.String("edge", p.Edge)))
	case *event.PingRaised:
		t.pingsTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("type", p.Type)))
		if rt.pings == nil {
			rt.pings = map[string]pingClock{}
		}
		rt.pings[p.PingID] = pingClock{p.Type, e.TS}
	case *event.PingAnswered:
		if c, ok := rt.pings[p.PingID]; ok {
			t.answerWait.Record(ctx, seconds(c.at, e.TS), metric.WithAttributes(attribute.String("type", c.typ)))
			delete(rt.pings, p.PingID)
		}
	case *event.HoldTaken:
		t.holds.Add(ctx, 1)
	case *event.RunCompleted:
		t.endRun(e.RunID, rt, e.TS, "completed", codes.Ok, "")
	case *event.RunAbandoned:
		if strings.HasPrefix(p.Reason, "budget exceeded") {
			t.budgetStops.Add(ctx, 1)
		}
		t.endRun(e.RunID, rt, e.TS, "abandoned", codes.Error, "")
	case *event.RunFailed:
		t.endRun(e.RunID, rt, e.TS, "failed", codes.Error, "")
	}
}

// landsAfterTheEnd reports the events a finished run still gets: housekeeping
// on its workspace and its archiving. A retry is not one: it is new work.
func landsAfterTheEnd(p event.Payload) bool {
	switch p.(type) {
	case *event.EnvDestroyed, *event.RunArchived,
		*event.EnvRebuildStarted, *event.EnvRebuildCompleted, *event.EnvRebuildFailed:
		return true
	}
	return false
}

func seconds(from, to time.Time) float64 { return to.Sub(from).Seconds() }

// provisioned closes the open provisioning (one env_requested may meet
// several outcomes across re-provisionings: only the first counts).
func (t *Telemetry) provisioned(rt *runTrace, ts time.Time, outcome string) {
	if rt.envRequestedAt.IsZero() {
		return
	}
	t.provision.Record(context.Background(), seconds(rt.envRequestedAt, ts),
		metric.WithAttributes(attribute.String("outcome", outcome)))
	rt.envRequestedAt = time.Time{}
}

// usage records one leg's spend: the token and cost metrics, and the
// attempt's running totals on its span.
func (t *Telemetry) usage(rt *runTrace, u *event.Usage) {
	if u == nil {
		return
	}
	ctx := context.Background()
	model := attribute.String("gen_ai.request.model", rt.model)
	harness := attribute.String("apothecary.harness", rt.harness)
	t.tokenUsage.Record(ctx, int64(u.InputTokens), metric.WithAttributes(attribute.String("gen_ai.token.type", "input"), model, harness))
	t.tokenUsage.Record(ctx, int64(u.OutputTokens), metric.WithAttributes(attribute.String("gen_ai.token.type", "output"), model, harness))
	t.llmCost.Add(ctx, u.CostUSD, metric.WithAttributes(attribute.String("model", rt.model), attribute.String("harness", rt.harness)))
	if rt.stage == nil {
		return
	}
	rt.inputTokens += u.InputTokens
	rt.outputTokens += u.OutputTokens
	rt.stage.SetAttributes(
		attribute.Int("gen_ai.usage.input_tokens", rt.inputTokens),
		attribute.Int("gen_ai.usage.output_tokens", rt.outputTokens),
	)
}

func (t *Telemetry) endStage(rt *runTrace, ts time.Time, outcome string, code codes.Code, desc string) {
	if rt.stage == nil {
		return
	}
	if outcome != "" {
		t.stageDuration.Record(context.Background(), seconds(rt.stageStartedAt, ts), metric.WithAttributes(
			attribute.String("stage", rt.stageID),
			attribute.String("harness", rt.harness),
			attribute.String("model", rt.model),
			attribute.String("outcome", outcome),
		))
	}
	rt.stage.SetStatus(code, desc)
	rt.stage.End(trace.WithTimestamp(ts))
	rt.stage = nil
}

// endRun counts the run's end and closes the attempt it ended in, which
// carries the run's state: the marker span ended at creation.
func (t *Telemetry) endRun(runID string, rt *runTrace, ts time.Time, state string, code codes.Code, desc string) {
	t.runsTotal.Add(context.Background(), 1, metric.WithAttributes(attribute.String("state", state)))
	if rt.stage != nil {
		rt.stage.SetAttributes(attribute.String("apothecary.run_state", state))
		if desc == "" {
			desc = "run " + state
		}
	}
	t.endStage(rt, ts, "", code, desc)
	delete(t.runs, runID)
}
