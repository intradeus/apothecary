import { describe, expect, it } from 'vitest';
import type { AnalyticsResult, CatalogField } from '$lib/domain';
import { fmtDim, fmtDims, fmtValue } from './format';

const field = (id: string, type: CatalogField['type'] = 'text'): CatalogField => ({
	id,
	label: id,
	type,
	aggs: []
});

describe('fmtValue', () => {
	it.each<[CatalogField['type'], number, string]>([
		['usd', 1.5, '$1.500'],
		['usd', 0, '$0.000'],
		['ms', 125_000, '2m 05s'],
		['ms', 230.4, '230ms'],
		['ms', 0, '0s'],
		['ratio', 0.873, '87%'],
		['ratio', 0.05, '5.0%'],
		['tokens', 950, '950'],
		['tokens', 12_345, '12.3k'],
		['tokens', 4_100_000, '4.1M'],
		['count', 3, '3'],
		['count', 2.456, '2.46'],
		['count', 1.5, '1.5']
	])('reads a %s of %d as %s', (type, v, want) => {
		expect(fmtValue(type, v)).toBe(want);
	});

	it('reads nothing aggregated as a dash', () => {
		expect(fmtValue('usd', null)).toBe('—');
		expect(fmtValue('count', undefined)).toBe('—');
		expect(fmtValue('count', NaN)).toBe('—');
	});
});

describe('fmtDim', () => {
	it('prefers the result’s label for a value', () => {
		expect(fmtDim(field('project_id'), 'p1', { project_id: { p1: 'Shop' } })).toBe('Shop');
		expect(fmtDim(field('project_id'), 'p2', { project_id: { p1: 'Shop' } })).toBe('p2');
	});

	it('names the empty value, booleans and time buckets for people', () => {
		expect(fmtDim(field('end_reason'), '')).toBe('(none)');
		expect(fmtDim(field('success', 'bool'), 'true')).toBe('yes');
		expect(fmtDim(field('success', 'bool'), 'false')).toBe('no');
		expect(fmtDim(field('success', 'bool'), 'maybe')).toBe('maybe');
		expect(fmtDim(field('bucket', 'time'), '2026-10-26 00:00:00+00')).toBe('2026-10-26');
		expect(fmtDim(field('bucket', 'time'), 'soon')).toBe('soon');
	});
});

describe('fmtDims', () => {
	it('joins a row’s dimensions as the panel shows them', () => {
		const result: AnalyticsResult = {
			dims: [field('flow_hash'), field('success', 'bool')],
			measures: [],
			rows: [],
			labels: { flow_hash: { abc: 'builder v3' } },
			truncated: false
		};
		expect(fmtDims(result, ['abc', 'true'])).toBe('builder v3 · yes');
	});
});
