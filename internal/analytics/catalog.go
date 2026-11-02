// Package analytics is the run analytics a person reads about their own runs
// (TODO 23, docs/observability-analytics-plan.md): where a run's time went,
// what it spent, how often it retried, asked and was held — compared across
// flows, flow versions, skillsets, stages and models.
//
// The data is the fact tables the event store projects (run_facts,
// stage_attempts, run_skillsets, pings). A query is never SQL from the
// client: it is a whitelisted shape (a grain, measures, group-by dimensions,
// filters, a time range) checked against the catalog below and compiled here,
// with the caller's user id bound as $1 in every source. That is the whole
// tenancy story, so it lives in one place and nothing else builds analytics
// SQL. The package imports nothing from the module: it knows the tables, not
// the domain that writes them.
package analytics

import "slices"

// Grain is what one row of a query's source is: a run, a stage attempt, a
// run's pinned skillset, a ping, or a ticket (all its runs).
type Grain string

const (
	GrainRuns      Grain = "runs"
	GrainAttempts  Grain = "attempts"
	GrainSkillsets Grain = "skillsets"
	GrainPings     Grain = "pings"
	GrainTickets   Grain = "tickets"
)

// FieldType says how a value reads: the UI formats by it.
type FieldType string

const (
	TypeText   FieldType = "text"
	TypeBool   FieldType = "bool"
	TypeTime   FieldType = "time"
	TypeCount  FieldType = "count"
	TypeUSD    FieldType = "usd"
	TypeMS     FieldType = "ms"
	TypeTokens FieldType = "tokens"
	TypeRatio  FieldType = "ratio"
)

// Agg is how a measure folds over a group's rows.
type Agg string

const (
	AggSum    Agg = "sum"
	AggAvg    Agg = "avg"
	AggMin    Agg = "min"
	AggMax    Agg = "max"
	AggMedian Agg = "median"
	AggP25    Agg = "p25"
	AggP75    Agg = "p75"
	AggP90    Agg = "p90"
)

// numericAggs are every agg; each plain measure takes all of them.
var numericAggs = []Agg{AggSum, AggAvg, AggMin, AggMax, AggMedian, AggP25, AggP75, AggP90}

// aggSQL is an agg's SQL around an expression.
func aggSQL(a Agg, expr string) string {
	switch a {
	case AggMedian:
		return "percentile_cont(0.5) WITHIN GROUP (ORDER BY " + expr + ")"
	case AggP25:
		return "percentile_cont(0.25) WITHIN GROUP (ORDER BY " + expr + ")"
	case AggP75:
		return "percentile_cont(0.75) WITHIN GROUP (ORDER BY " + expr + ")"
	case AggP90:
		return "percentile_cont(0.9) WITHIN GROUP (ORDER BY " + expr + ")"
	}
	return string(a) + "(" + expr + ")"
}

// Field is one dimension or measure as the catalog offers it. Aggs lists
// the aggs a plain measure takes; a dimension and a derived measure have
// none (a result's measure carries the one it was asked with).
type Field struct {
	ID    string    `json:"id"`
	Label string    `json:"label"`
	Type  FieldType `json:"type"`
	Aggs  []Agg     `json:"aggs,omitempty"`
}

// GrainInfo is one grain's fields. TimeField names the column the time
// range and the bucket apply to.
type GrainInfo struct {
	ID         Grain   `json:"id"`
	Label      string  `json:"label"`
	TimeField  string  `json:"time_field"`
	Dimensions []Field `json:"dimensions"`
	Measures   []Field `json:"measures"`
	Derived    []Field `json:"derived"`
}

// Catalog is everything a query may name.
type Catalog struct {
	Grains []GrainInfo `json:"grains"`
}

// field is a catalog field with its SQL: a column expression for a
// dimension or plain measure, a whole aggregate for a derived one.
type field struct {
	Field
	expr string
}

// grain is a grain's source. from is the FROM clause; where its own
// predicate (it binds the user as $1 unless from already does). runID is
// the run a row belongs to (drill), "" when a row is not one run's.
type grain struct {
	id        Grain
	label     string
	from      string
	where     string
	timeField string
	timeExpr  string
	runID     string
	dims      []field
	measures  []field
	derived   []field
}

func (g *grain) dim(id string) (field, bool) { return find(g.dims, id) }

func find(fs []field, id string) (field, bool) {
	i := slices.IndexFunc(fs, func(f field) bool { return f.ID == id })
	if i < 0 {
		return field{}, false
	}
	return fs[i], true
}

func dim(id, label string, t FieldType, expr string) field {
	return field{Field{ID: id, Label: label, Type: t}, expr}
}

func measure(id, label string, t FieldType, expr string) field {
	return field{Field{ID: id, Label: label, Type: t, Aggs: numericAggs}, expr}
}

func derived(id, label string, t FieldType, expr string) field {
	return field{Field{ID: id, Label: label, Type: t}, expr}
}

// runMeasures are a run's facts, on the run_facts alias f.
func runMeasures() []field {
	return []field{
		measure("wall_ms", "Wall time", TypeMS, "f.wall_ms"),
		measure("queued_ms", "Queued", TypeMS, "f.queued_ms"),
		measure("provisioning_ms", "Provisioning", TypeMS, "f.provisioning_ms"),
		measure("active_ms", "Active", TypeMS, "f.active_ms"),
		measure("waiting_human_ms", "Waiting on a human", TypeMS, "f.waiting_human_ms"),
		measure("held_ms", "Held", TypeMS, "f.held_ms"),
		measure("paused_ms", "Paused", TypeMS, "f.paused_ms"),
		measure("cost_usd", "Cost", TypeUSD, "f.cost_usd"),
		measure("rework_cost_usd", "Rework cost", TypeUSD, "f.rework_cost_usd"),
		measure("input_tokens", "Input tokens", TypeTokens, "f.input_tokens"),
		measure("output_tokens", "Output tokens", TypeTokens, "f.output_tokens"),
		measure("turns", "Turns", TypeCount, "f.turns"),
		measure("stages", "Stages", TypeCount, "f.stages"),
		measure("attempts", "Attempts", TypeCount, "f.attempts"),
		measure("failed_attempts", "Failed attempts", TypeCount, "f.failed_attempts"),
		measure("retries", "Retries", TypeCount, "f.retries"),
		measure("try_agains", "Try-agains", TypeCount, "f.try_agains"),
		measure("pings", "Pings", TypeCount, "f.pings"),
		measure("pings_unwanted", "Unwanted pings", TypeCount, "f.pings_unwanted"),
		measure("gates_rejected", "Gates rejected", TypeCount, "f.gates_rejected"),
		measure("holds", "Holds", TypeCount, "f.holds"),
		measure("mrs_created", "Merge requests", TypeCount, "f.mrs_created"),
	}
}

// runDerived are the ratios over a group of runs, on the alias f.
func runDerived() []field {
	return []field{
		derived("success_rate", "Success rate", TypeRatio, "avg(f.success::int)"),
		derived("first_pass_rate", "First-pass rate", TypeRatio, "avg(f.first_pass::int)"),
		derived("cost_per_success", "Cost per success", TypeUSD, "sum(f.cost_usd)/nullif(sum(f.success::int),0)"),
		derived("unwanted_pings_per_run", "Unwanted pings per run", TypeCount, "avg(f.pings_unwanted)"),
		derived("attempts_per_stage", "Attempts per stage", TypeCount, "sum(f.attempts)::float8/nullif(sum(f.stages),0)"),
		derived("runs_per_ticket", "Runs per ticket", TypeCount, "count(*)::float8/nullif(count(DISTINCT nullif(f.ticket_id,'')),0)"),
		derived("human_minutes_per_run", "Human minutes per run", TypeCount, "avg(f.waiting_human_ms+f.held_ms)/60000.0"),
		derived("avg_response_ms", "Average response time", TypeMS, "sum(f.human_response_ms)::float8/nullif(sum(f.pings_answered),0)"),
	}
}

// ticketsSource is one row per ticket: its runs, whether any shipped
// (completed), and what it took to ship — the runs up to and including the
// first success (after marks a run that came after one).
const ticketsSource = `(SELECT user_id, ticket_id, min(project_id) project_id,
  min(ticket_change_type) ticket_change_type, min(created_at) created_at,
  count(*) runs, bool_or(success) shipped,
  count(*) FILTER (WHERE NOT after) runs_to_ship,
  coalesce(sum(cost_usd) FILTER (WHERE NOT after),0) cost_to_ship,
  sum(cost_usd) total_cost_usd
 FROM (SELECT *, coalesce(bool_or(success) OVER (PARTITION BY ticket_id ORDER BY created_at
         ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING), false) after
       FROM run_facts WHERE user_id = $1 AND ticket_id <> '') r
 GROUP BY user_id, ticket_id) t
LEFT JOIN tickets tk ON tk.id = t.ticket_id`

var grains = []*grain{
	{
		id: GrainRuns, label: "Runs",
		from:      "run_facts f LEFT JOIN tickets tk ON tk.id = f.ticket_id",
		where:     "f.user_id = $1",
		timeField: "created_at", timeExpr: "f.created_at",
		runID: "f.run_id",
		dims: []field{
			dim("project_id", "Project", TypeText, "f.project_id"),
			dim("flow_name", "Flow", TypeText, "f.flow_name"),
			dim("flow_hash", "Flow version", TypeText, "f.flow_hash"),
			dim("ticket_id", "Ticket", TypeText, "f.ticket_id"),
			dim("ticket_change_type", "Change type", TypeText, "f.ticket_change_type"),
			dim("ticket_source_type", "Tracker issue type", TypeText, "tk.source_type"),
			dim("tracker_kind", "Tracker", TypeText, "f.tracker_kind"),
			dim("automation_id", "Automation", TypeText, "tk.automation_id"),
			dim("started_by", "Started by", TypeText, "f.started_by"),
			dim("state", "State", TypeText, "f.state"),
			dim("end_reason", "End reason", TypeText, "f.end_reason"),
			dim("stalled_at", "Stalled at", TypeText, "f.stalled_at"),
			dim("success", "Succeeded", TypeBool, "f.success"),
			dim("first_pass", "First pass", TypeBool, "f.first_pass"),
		},
		measures: append(runMeasures(),
			measure("ask_cost_usd", "Ask cost", TypeUSD, "(SELECT coalesce(sum(x.cost_usd),0) FROM ask_messages x WHERE x.run_id=f.run_id)")),
		derived: runDerived(),
	},
	{
		id: GrainAttempts, label: "Stage attempts",
		from:      "stage_attempts a",
		where:     "a.user_id = $1",
		timeField: "started_at", timeExpr: "a.started_at",
		runID: "a.run_id",
		dims: []field{
			dim("stage_id", "Stage", TypeText, "a.stage_id"),
			dim("iteration", "Iteration", TypeCount, "a.iteration"),
			dim("harness", "Harness", TypeText, "a.harness"),
			dim("model", "Model", TypeText, "a.model"),
			dim("agent", "Agent", TypeText, "a.agent"),
			dim("agent_hash", "Agent version", TypeText, "a.agent_hash"),
			dim("outcome", "Outcome", TypeText, "a.outcome"),
			dim("failure_reason", "Failure reason", TypeText, "a.failure_reason"),
			dim("project_id", "Project", TypeText, "a.project_id"),
			dim("flow_name", "Flow", TypeText, "a.flow_name"),
			dim("flow_hash", "Flow version", TypeText, "a.flow_hash"),
			dim("ticket_change_type", "Change type", TypeText, "a.ticket_change_type"),
		},
		measures: []field{
			measure("active_ms", "Active", TypeMS, "a.active_ms"),
			measure("waiting_ms", "Waiting", TypeMS, "a.waiting_ms"),
			measure("cost_usd", "Cost", TypeUSD, "a.cost_usd"),
			measure("input_tokens", "Input tokens", TypeTokens, "a.input_tokens"),
			measure("output_tokens", "Output tokens", TypeTokens, "a.output_tokens"),
			measure("turns", "Turns", TypeCount, "a.turns"),
			measure("pings", "Pings", TypeCount, "a.pings"),
		},
		derived: []field{
			derived("failure_rate", "Failure rate", TypeRatio, "avg((a.outcome='failed')::int)"),
		},
	},
	{
		id: GrainSkillsets, label: "Skillsets",
		from:      "run_skillsets s JOIN run_facts f ON f.run_id = s.run_id",
		where:     "s.user_id = $1",
		timeField: "created_at", timeExpr: "f.created_at",
		runID: "s.run_id",
		dims: []field{
			dim("skillset", "Skillset", TypeText, "s.name"),
			dim("skillset_version", "Skillset version", TypeText, "s.name || ' v' || s.seq"),
			dim("project_id", "Project", TypeText, "f.project_id"),
			dim("flow_name", "Flow", TypeText, "f.flow_name"),
			dim("flow_hash", "Flow version", TypeText, "f.flow_hash"),
			dim("ticket_change_type", "Change type", TypeText, "f.ticket_change_type"),
			dim("state", "State", TypeText, "f.state"),
			dim("success", "Succeeded", TypeBool, "f.success"),
		},
		measures: runMeasures(),
		derived:  runDerived(),
	},
	{
		id: GrainPings, label: "Pings",
		from:      "pings p JOIN run_facts f ON f.run_id = p.run_id",
		where:     "p.user_id = $1",
		timeField: "raised_at", timeExpr: "p.raised_at",
		runID: "p.run_id",
		dims: []field{
			dim("type", "Type", TypeText, "p.type"),
			dim("stage_id", "Stage", TypeText, "p.stage_id"),
			dim("answer", "Answer", TypeText, "p.answer"),
			dim("status", "Status", TypeText, "p.status"),
			dim("project_id", "Project", TypeText, "f.project_id"),
			dim("flow_name", "Flow", TypeText, "f.flow_name"),
			dim("flow_hash", "Flow version", TypeText, "f.flow_hash"),
			dim("ticket_change_type", "Change type", TypeText, "f.ticket_change_type"),
		},
		measures: []field{
			measure("response_ms", "Response time", TypeMS, "extract(epoch FROM p.answered_at - p.raised_at)*1000"),
		},
		derived: []field{
			derived("reject_rate", "Reject rate", TypeRatio, "avg(CASE WHEN p.answered_at IS NULL THEN NULL ELSE (p.answer='reject')::int END)"),
		},
	},
	{
		id: GrainTickets, label: "Tickets",
		from:      ticketsSource,
		timeField: "created_at", timeExpr: "t.created_at",
		dims: []field{
			dim("project_id", "Project", TypeText, "t.project_id"),
			dim("ticket_change_type", "Change type", TypeText, "t.ticket_change_type"),
			dim("ticket_source_type", "Tracker issue type", TypeText, "tk.source_type"),
			dim("shipped", "Shipped", TypeBool, "t.shipped"),
		},
		measures: []field{
			measure("runs", "Runs", TypeCount, "t.runs"),
			measure("runs_to_ship", "Runs to ship", TypeCount, "t.runs_to_ship"),
			measure("cost_to_ship", "Cost to ship", TypeUSD, "t.cost_to_ship"),
			measure("total_cost_usd", "Total cost", TypeUSD, "t.total_cost_usd"),
		},
		derived: []field{
			derived("ship_rate", "Ship rate", TypeRatio, "avg(t.shipped::int)"),
		},
	},
}

func grainOf(id Grain) (*grain, bool) {
	i := slices.IndexFunc(grains, func(g *grain) bool { return g.id == id })
	if i < 0 {
		return nil, false
	}
	return grains[i], true
}

func fields(fs []field) []Field {
	out := make([]Field, len(fs))
	for i, f := range fs {
		out[i] = f.Field
	}
	return out
}

// catalog is the public view of grains.
func catalog() Catalog {
	c := Catalog{Grains: make([]GrainInfo, len(grains))}
	for i, g := range grains {
		c.Grains[i] = GrainInfo{
			ID: g.id, Label: g.label, TimeField: g.timeField,
			Dimensions: fields(g.dims), Measures: fields(g.measures), Derived: fields(g.derived),
		}
	}
	return c
}
