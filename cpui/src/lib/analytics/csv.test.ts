import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AnalyticsResult } from '$lib/domain';
import { csvName, downloadCsv, toCsv } from './csv';

const result = (over: Partial<AnalyticsResult> = {}): AnalyticsResult => ({
	dims: [{ id: 'flow_hash', label: 'Flow version', type: 'text', aggs: [] }],
	measures: [{ id: 'cost_usd', label: 'Cost (median)', type: 'usd', aggs: [] }],
	rows: [
		{ dims: ['abc'], values: [0.25], n: 12, small: false },
		{ dims: ['def'], values: [null], n: 3, small: true }
	],
	labels: { flow_hash: { abc: 'builder v3', def: 'say "hi", ok' } },
	truncated: false,
	...over
});

describe('toCsv', () => {
	it('writes labelled dimensions, raw measures and n, quoting what needs it', () => {
		expect(toCsv(result())).toBe(
			'Flow version,Cost (median),n\r\n' + 'builder v3,0.25,12\r\n' + '"say ""hi"", ok",,3\r\n'
		);
	});

	it('adds the score column when the rows are ranked', () => {
		const ranked = result({
			rows: [
				{ dims: ['abc'], values: [1], n: 5, small: false, score: 100 },
				{ dims: ['def'], values: [2], n: 5, small: false }
			]
		});
		expect(toCsv(ranked).split('\r\n')).toEqual([
			'Flow version,Cost (median),n,Score',
			'builder v3,1,5,100',
			'"say ""hi"", ok",2,5,',
			''
		]);
	});

	it('quotes a line break inside a value', () => {
		const r = result({ labels: { flow_hash: { abc: 'two\nlines' } }, rows: [result().rows[0]] });
		expect(toCsv(r)).toContain('"two\nlines",0.25,12');
	});
});

describe('csvName', () => {
	it('slugs the panel title, with a fallback for one that has no letters', () => {
		expect(csvName('Compare flows — p90!')).toBe('compare-flows-p90.csv');
		expect(csvName('——')).toBe('panel.csv');
	});
});

describe('downloadCsv', () => {
	afterEach(() => vi.restoreAllMocks());

	it('clicks a throwaway link to the text and lets the URL go', () => {
		const create = vi.fn(() => 'blob:x');
		const revoke = vi.fn();
		Object.assign(URL, { createObjectURL: create, revokeObjectURL: revoke });
		const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});

		downloadCsv('Weekly cost', 'a,b\r\n');

		const clicked = click.mock.contexts[0] as HTMLAnchorElement;
		expect(create).toHaveBeenCalledWith(expect.any(Blob));
		expect(clicked.download).toBe('weekly-cost.csv');
		expect(clicked.href).toBe('blob:x');
		expect(clicked.isConnected).toBe(false);
		expect(revoke).toHaveBeenCalledWith('blob:x');
	});
});
