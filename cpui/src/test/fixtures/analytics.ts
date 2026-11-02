// Analytics objects for tests: a small catalog, results, panels, dashboards.
import type {
	AnalyticsResult,
	Catalog,
	CatalogField,
	Dashboard,
	Panel,
	Scorecard
} from '$lib/domain';

const f = (
	id: string,
	label: string,
	type: CatalogField['type'],
	aggs: CatalogField['aggs'] = []
) => ({ id, label, type, aggs }) as CatalogField;

export const aCatalog = (): Catalog => ({
	grains: [
		{
			id: 'runs',
			label: 'Runs',
			timeField: 'created_at',
			dimensions: [
				f('flow_name', 'Flow', 'text'),
				f('flow_hash', 'Flow version', 'text'),
				f('project_id', 'Workshop', 'text'),
				f('success', 'Succeeded', 'bool')
			],
			measures: [
				f('wall_ms', 'Wall time', 'ms', [
					'sum',
					'avg',
					'min',
					'max',
					'median',
					'p25',
					'p75',
					'p90'
				]),
				f('cost_usd', 'Cost', 'usd', ['sum', 'avg', 'median'])
			],
			derived: [f('success_rate', 'Success rate', 'ratio')]
		},
		{
			id: 'attempts',
			label: 'Stage attempts',
			timeField: 'started_at',
			dimensions: [f('stage_id', 'Stage', 'text'), f('iteration', 'Iteration', 'text')],
			measures: [f('cost_usd', 'Cost', 'usd', ['sum'])],
			derived: []
		}
	]
});

/** Cost by flow version: two rows, the second on few runs. */
export const aResult = (over: Partial<AnalyticsResult> = {}): AnalyticsResult => ({
	dims: [f('flow_hash', 'Flow version', 'text')],
	measures: [f('cost_usd', 'Cost (median)', 'usd')],
	rows: [
		{ dims: ['abc'], values: [0.25], n: 12, small: false },
		{ dims: ['def'], values: [1.5], n: 3, small: true }
	],
	labels: { flow_hash: { abc: 'builder v3', def: 'builder v4' } },
	truncated: false,
	...over
});

export const aPanel = (over: Partial<Panel> = {}): Panel => ({
	id: 'p1',
	title: 'Cost by version',
	query: {
		grain: 'runs',
		measures: [{ field: 'cost_usd', agg: 'median' }],
		groupBy: ['flow_hash'],
		filters: [],
		time: {}
	},
	viz: 'table',
	width: 6,
	...over
});

export const aDashboard = (over: Partial<Dashboard> = {}): Dashboard => ({
	id: 'd1',
	userId: 'u1',
	name: 'Mine',
	builtin: false,
	filters: ['flow_name'],
	panels: [aPanel()],
	createdAt: '2026-10-01T00:00:00Z',
	updatedAt: '2026-10-01T00:00:00Z',
	...over
});

export const aScorecard = (over: Partial<Scorecard> = {}): Scorecard => ({
	id: 'builtin-cheapest',
	name: 'Cheapest',
	builtin: true,
	terms: [{ measure: { field: 'cost_usd', agg: 'median' }, direction: 'lower', weight: 1 }],
	createdAt: '2026-10-01T00:00:00Z',
	updatedAt: '2026-10-01T00:00:00Z',
	...over
});
