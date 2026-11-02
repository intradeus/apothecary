import { describe, expect, it } from 'vitest';
import { aResult } from '$test/fixtures/analytics';
import { bucketDate, dimText, rowLabel, seriesColor, valueFields, valuesOf } from './shape';

describe('shape', () => {
	it('cycles the role-token hues for series', () => {
		expect(seriesColor(0)).toMatch(/^var\(--/);
		expect(seriesColor(6)).toBe(seriesColor(0));
		expect(seriesColor(1)).not.toBe(seriesColor(0));
	});

	it('reads the measures as the value columns', () => {
		const r = aResult();
		expect(valueFields(r).map((f) => f.id)).toEqual(['cost_usd']);
		expect(valuesOf(r, r.rows[1])).toEqual([1.5]);
	});

	it('makes the count the value of a query with no measures', () => {
		const r = aResult({ measures: [] });
		expect(valueFields(r)).toEqual([{ id: 'n', label: 'Runs', type: 'count', aggs: [] }]);
		expect(valuesOf(r, r.rows[0])).toEqual([12]);
	});

	it('labels dimensions through the result, and names a row by them all', () => {
		const r = aResult({
			dims: [...aResult().dims, { id: 'success', label: 'Succeeded', type: 'bool', aggs: [] }]
		});
		const row = { dims: ['abc', 'true'], values: [1], n: 1, small: true };
		expect(dimText(r, row, 0)).toBe('builder v3');
		expect(dimText(r, { ...row, dims: ['abc'] }, 1)).toBe('(none)');
		expect(dimText(r, row, 2)).toBe('(none)');
	});

	it('names a row without dimensions as the whole selection', () => {
		const r = aResult({ dims: [] });
		expect(rowLabel(r, { dims: [], values: [1], n: 1, small: false })).toBe('All');
	});

	it('reads a bucket by its date', () => {
		expect(bucketDate('2026-10-05 00:00:00+00').toISOString()).toBe('2026-10-05T00:00:00.000Z');
	});
});
