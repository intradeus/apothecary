// Run analytics: the whitelisted query language over the user's own run facts,
// and the dashboards and scorecards built from it.
//
// A query names a grain (what one row of facts is: a run, a stage attempt…),
// the measures to aggregate, up to three dimensions to group by, filters and a
// time window. The engine compiles it against its catalog, so every field id
// here is one the catalog listed; nothing is free-form SQL.

import type { RunState } from './run';

export type Grain = 'runs' | 'attempts' | 'skillsets' | 'pings' | 'tickets';

/** How a field's values read: the formatter switches on it. `count` may be
 *  fractional (an average, a per-run rate). */
export type AnalyticsFieldType =
	'text' | 'bool' | 'time' | 'count' | 'usd' | 'ms' | 'tokens' | 'ratio';

export type Agg = 'sum' | 'avg' | 'min' | 'max' | 'median' | 'p25' | 'p75' | 'p90';

export interface CatalogField {
	id: string;
	label: string;
	type: AnalyticsFieldType;
	/** The aggregations a measure takes; empty on dimensions and derived
	 *  measures (which aggregate themselves). */
	aggs: Agg[];
}

export interface CatalogGrain {
	id: Grain;
	label: string;
	/** The field the time window and the bucket apply to. */
	timeField: string;
	dimensions: CatalogField[];
	measures: CatalogField[];
	/** Ratios and rates computed over the whole group: no agg. */
	derived: CatalogField[];
}

export interface Catalog {
	grains: CatalogGrain[];
}

/** A measure in a query: a catalog measure with its aggregation, or a derived
 *  measure with none. */
export interface MeasureRef {
	field: string;
	agg?: Agg;
}

export type FilterOp = 'eq' | 'neq' | 'in' | 'not_in';

export interface AnalyticsFilter {
	field: string;
	op: FilterOp;
	values: string[];
}

export type Bucket = 'day' | 'week' | 'month';

/** RFC 3339 bounds, `from` inclusive and `to` exclusive. A bucket groups the
 *  rows by time; the bucket is then the result's first dimension. */
export interface TimeRange {
	from?: string;
	to?: string;
	bucket?: Bucket;
}

/** `by`: 'n', 'score', 'bucket', 'm<i>' (0-based measure index) or a
 *  group-by dimension id. */
export interface OrderBy {
	by: string;
	desc: boolean;
}

/** Rank rows by a scorecard. With a baseline (the dimension values of one
 *  row) every row scores relative to it (baseline = 100); without, min-max
 *  across the rows. */
export interface ScoreSpec {
	scorecard: string;
	baseline?: string[];
}

export interface AnalyticsQuery {
	grain: Grain;
	/** May be empty: the row count `n` is then the value (a count panel). */
	measures: MeasureRef[];
	groupBy: string[];
	filters: AnalyticsFilter[];
	time: TimeRange;
	order?: OrderBy;
	limit?: number;
	score?: ScoreSpec;
}

export interface AnalyticsRow {
	/** Dimension values as text, in `result.dims` order. */
	dims: string[];
	/** One per `result.measures`; null where nothing aggregated. */
	values: (number | null)[];
	n: number;
	/** Too few rows behind it to trust (n < 5). */
	small: boolean;
	score?: number;
}

export interface AnalyticsResult {
	/** The bucket first when bucketed ({id: 'bucket', type: 'time'}), then the
	 *  group-by dimensions. */
	dims: CatalogField[];
	/** One per query measure (scorecard terms appended); labels carry the agg,
	 *  "Cost (p90)". */
	measures: CatalogField[];
	rows: AnalyticsRow[];
	/** dim id → raw value → display label (project names, "builder v3"). */
	labels: Record<string, Record<string, string>>;
	/** More than the row limit matched. */
	truncated: boolean;
}

export type Viz = 'number' | 'table' | 'bar' | 'line' | 'scatter' | 'heatmap' | 'box';
export const VIZES: readonly Viz[] = [
	'number',
	'table',
	'bar',
	'line',
	'scatter',
	'heatmap',
	'box'
];

/** Columns of the dashboard's 12-column grid a panel spans. */
export type PanelWidth = 3 | 4 | 6 | 8 | 12;
export const PANEL_WIDTHS: readonly PanelWidth[] = [3, 4, 6, 8, 12];

export const BUCKETS: readonly Bucket[] = ['day', 'week', 'month'];

export interface Panel {
	id: string;
	title: string;
	/** Saved without time bounds: the dashboard supplies the range. */
	query: AnalyticsQuery;
	viz: Viz;
	width: PanelWidth;
}

export interface Dashboard {
	id: string;
	/** Empty on built-ins. */
	userId?: string;
	name: string;
	/** Built-ins are read-only; duplicate one to change it. */
	builtin: boolean;
	/** The dimensions its filter bar offers. */
	filters: string[];
	panels: Panel[];
	createdAt: string;
	updatedAt: string;
}

export interface DashboardDraft {
	name: string;
	filters: string[];
	panels: Panel[];
}

export type Direction = 'lower' | 'higher';
export const DIRECTIONS: readonly Direction[] = ['lower', 'higher'];

/** One weighted criterion: which way of `measure` is better. */
export interface ScorecardTerm {
	measure: MeasureRef;
	direction: Direction;
	weight: number;
}

export interface Scorecard {
	id: string;
	userId?: string;
	name: string;
	builtin: boolean;
	terms: ScorecardTerm[];
	createdAt: string;
	updatedAt: string;
}

export interface ScorecardDraft {
	name: string;
	terms: ScorecardTerm[];
}

/** A run behind an aggregate row. */
export interface DrillRun {
	runId: string;
	title: string;
	flowName: string;
	flowHash: string;
	state: RunState;
	createdAt: string;
	wallMs: number;
	costUsd: number;
}

/** One distinct value of a dimension, for a filter picker. */
export interface AnalyticsValue {
	value: string;
	label: string;
}

/** "cost_usd/median", or the bare id of a derived measure. Stable: used as a
 *  key and to de-duplicate measures. */
export const measureKey = (m: MeasureRef): string => (m.agg ? `${m.field}/${m.agg}` : m.field);

/** Built-in dashboards and scorecards carry this id prefix. */
export const isBuiltinId = (id: string): boolean => id.startsWith('builtin-');
