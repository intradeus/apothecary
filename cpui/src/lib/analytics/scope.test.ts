import { describe, expect, it } from 'vitest';
import type { AnalyticsQuery } from '$lib/domain';
import { aResult } from '$test/fixtures/analytics';
import { drillScope, rangeFrom, scoped } from './scope';

const q = (over: Partial<AnalyticsQuery> = {}): AnalyticsQuery => ({
	grain: 'runs',
	measures: [],
	groupBy: [],
	filters: [{ field: 'state', op: 'eq', values: ['completed'] }],
	time: {},
	...over
});
const NOW = new Date('2026-11-01T00:00:00Z');

describe('rangeFrom', () => {
	it('counts back from now, and is open for all time', () => {
		expect(rangeFrom('30d', NOW)).toBe('2026-10-02T00:00:00.000Z');
		expect(rangeFrom('90d', NOW)).toBe('2026-08-03T00:00:00.000Z');
		expect(rangeFrom('1y', NOW)).toBe('2025-11-01T00:00:00.000Z');
		expect(rangeFrom('all', NOW)).toBeUndefined();
	});

	it('defaults to the present', () => {
		expect(Date.parse(rangeFrom('30d')!)).toBeLessThan(Date.now());
	});
});

describe('scoped', () => {
	it('sets the range start and keeps the bucket, dropping saved bounds', () => {
		const out = scoped(q({ time: { bucket: 'week', to: 'x' } }), 'F', {}, () => true);
		expect(out.time).toEqual({ bucket: 'week', from: 'F' });
		expect(scoped(q({ time: { from: 'old' } }), undefined, {}, () => true).time).toEqual({});
	});

	it('adds an in filter for each pick the grain has', () => {
		const out = scoped(
			q(),
			undefined,
			{ flow_name: ['a', 'b'], stage_id: ['x'], project_id: [] },
			(d) => d !== 'stage_id'
		);
		expect(out.filters).toEqual([
			{ field: 'state', op: 'eq', values: ['completed'] },
			{ field: 'flow_name', op: 'in', values: ['a', 'b'] }
		]);
	});
});

describe('drillScope', () => {
	it('is the query’s filters and range for the whole panel', () => {
		expect(drillScope(q({ time: { from: 'F', to: 'T', bucket: 'day' } }))).toEqual({
			filters: q().filters,
			time: { from: 'F', to: 'T' }
		});
	});

	it('adds an eq filter per dimension of a clicked row', () => {
		const r = aResult();
		expect(drillScope(q(), r, r.rows[0]).filters).toEqual([
			...q().filters,
			{ field: 'flow_hash', op: 'eq', values: ['abc'] }
		]);
	});

	it('turns a bucket into its own range, inside the query’s', () => {
		const r = aResult({
			dims: [{ id: 'bucket', label: 'Week', type: 'time', aggs: [] }, ...aResult().dims]
		});
		const row = { dims: ['2026-10-05 00:00:00+00', 'abc'], values: [1], n: 1, small: true };
		const week = drillScope(
			q({ time: { bucket: 'week', from: '2026-10-07T00:00:00.000Z' } }),
			r,
			row
		);
		expect(week.time).toEqual({ from: '2026-10-07T00:00:00.000Z', to: '2026-10-12T00:00:00.000Z' });
		expect(week.filters.at(-1)).toEqual({ field: 'flow_hash', op: 'eq', values: ['abc'] });

		const month = drillScope(
			q({ time: { bucket: 'month', to: '2026-10-20T00:00:00.000Z' } }),
			r,
			row
		);
		expect(month.time).toEqual({
			from: '2026-10-05T00:00:00.000Z',
			to: '2026-10-20T00:00:00.000Z'
		});

		const open = drillScope(q({ time: { bucket: 'month' } }), r, row);
		expect(open.time).toEqual({ from: '2026-10-05T00:00:00.000Z', to: '2026-11-05T00:00:00.000Z' });

		const day = drillScope(q({ time: {} }), r, row);
		expect(day.time).toEqual({ from: '2026-10-05T00:00:00.000Z', to: '2026-10-06T00:00:00.000Z' });
	});

	it('reads a missing dimension value as empty', () => {
		const r = aResult();
		expect(drillScope(q(), r, { dims: [], values: [], n: 0, small: true }).filters.at(-1)).toEqual({
			field: 'flow_hash',
			op: 'eq',
			values: ['']
		});
	});
});
