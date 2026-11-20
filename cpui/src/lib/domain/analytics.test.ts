import { describe, expect, it } from 'vitest';
import { compareDashboard, isBuiltinId, measureKey, sameDashboard, type Panel } from './analytics';

describe('measureKey', () => {
	it('names an aggregated measure by field and agg, a derived one by its id', () => {
		expect(measureKey({ field: 'cost_usd', agg: 'median' })).toBe('cost_usd/median');
		expect(measureKey({ field: 'success_rate' })).toBe('success_rate');
	});
});

describe('isBuiltinId', () => {
	it('knows the engine’s built-ins by their prefix', () => {
		expect(isBuiltinId('builtin-overview')).toBe(true);
		expect(isBuiltinId('01JBUILTIN')).toBe(false);
	});
});

describe('compareDashboard', () => {
	const panel = (over: Partial<Panel> = {}): Panel => ({
		id: 'p1',
		title: 'Cost',
		query: { grain: 'runs', measures: [], groupBy: [], filters: [], time: {} },
		viz: 'table',
		width: 6,
		...over
	});

	it('finds nothing between equal content', () => {
		const d = { name: 'Mine', filters: ['flow_name'], panels: [panel()] };
		const c = compareDashboard(d, structuredClone(d));
		expect(c).toEqual({
			filtersOnlyInVersion: [],
			filtersOnlyInCurrent: [],
			panelsOnlyInVersion: [],
			panelsOnlyInCurrent: [],
			panelsChanged: []
		});
		expect(sameDashboard(c)).toBe(true);
	});

	it('names the name, filters and panels by id that differ, and what changed in a panel', () => {
		const kept = panel({ id: 'k', title: 'Kept' });
		const c = compareDashboard(
			{
				name: 'Old',
				filters: ['flow_name', 'stage_id'],
				panels: [
					panel({ id: 'gone', title: 'Gone' }),
					kept,
					panel({ id: 'ch', title: 'Then', viz: 'bar', width: 12 }),
					panel({ id: 'q', query: { ...panel().query, groupBy: ['flow_name'] } })
				]
			},
			{
				name: 'New',
				filters: ['flow_name', 'model'],
				panels: [
					kept,
					panel({ id: 'ch', title: 'Now' }),
					panel({ id: 'q' }),
					panel({ id: 'new', title: 'New' })
				]
			}
		);
		expect(c.name).toEqual({ version: 'Old', current: 'New' });
		expect(c.filtersOnlyInVersion).toEqual(['stage_id']);
		expect(c.filtersOnlyInCurrent).toEqual(['model']);
		expect(c.panelsOnlyInVersion.map((p) => p.id)).toEqual(['gone']);
		expect(c.panelsOnlyInCurrent.map((p) => p.id)).toEqual(['new']);
		expect(c.panelsChanged).toEqual([
			{ id: 'ch', title: 'Then', fields: ['title', 'viz', 'width'] },
			{ id: 'q', title: 'Cost', fields: ['query'] }
		]);
		expect(sameDashboard(c)).toBe(false);
	});

	it.each([
		['the name', { name: 'Other' }],
		['a filter in the version', { filters: ['a', 'b'] }],
		['a filter in the current one', { filters: [] }],
		['a panel in the version', { panels: [panel(), panel({ id: 'x' })] }],
		['a panel in the current one', { panels: [] }],
		['a changed panel', { panels: [panel({ width: 3 })] }]
	])('is different on %s alone', (_, over) => {
		const cur = { name: 'Mine', filters: ['a'], panels: [panel()] };
		expect(sameDashboard(compareDashboard({ ...cur, ...over }, cur))).toBe(false);
	});
});
