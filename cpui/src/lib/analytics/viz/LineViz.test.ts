import { render, screen } from '@testing-library/svelte';
import { tick } from 'svelte';
import { describe, expect, it } from 'vitest';
import type { AnalyticsResult, CatalogField } from '$lib/domain';
import { aResult } from '$test/fixtures/analytics';
import LineViz from './LineViz.svelte';

const bucket: CatalogField = { id: 'bucket', label: 'Week', type: 'time', aggs: [] };
const flow: CatalogField = { id: 'flow_name', label: 'Flow', type: 'text', aggs: [] };
const W1 = '2026-10-05 00:00:00+00';
const W2 = '2026-10-12 00:00:00+00';

const lines = (c: HTMLElement) => [...c.querySelectorAll('path.lc-path')];

const weekly = (over: Partial<AnalyticsResult> = {}) =>
	aResult({
		dims: [bucket],
		measures: [
			{ id: 'cost_usd', label: 'Cost (sum)', type: 'usd', aggs: [] },
			{ id: 'success_rate', label: 'Success rate', type: 'ratio', aggs: [] }
		],
		// Out of order on purpose: the line runs by date.
		rows: [
			{ dims: [W2], values: [2, 0.5], n: 4, small: true },
			{ dims: [W1], values: [1, 1], n: 6, small: false }
		],
		labels: {},
		...over
	});

describe('LineViz', () => {
	it('draws a line per measure over the buckets, with a legend', async () => {
		const { container } = render(LineViz, { result: weekly(), width: 400 });
		expect(lines(container)).toHaveLength(2);
		expect(await screen.findByText('Cost (sum)')).toBeInTheDocument();
		expect(screen.getByText('Success rate')).toBeInTheDocument();
	});

	it('draws one line of the count when there are no measures, without a legend', async () => {
		const { container } = render(LineViz, {
			result: weekly({
				measures: [],
				rows: [
					{ dims: [W1], values: [], n: 6, small: false },
					{ dims: [W2], values: [], n: 4, small: true }
				]
			}),
			width: 400
		});
		expect(lines(container)).toHaveLength(1);
		await tick();
		expect(screen.queryByText('Runs')).not.toBeInTheDocument();
	});

	it('draws a line per value of a second dimension', async () => {
		const { container } = render(LineViz, {
			result: weekly({
				dims: [bucket, flow],
				measures: [{ id: 'cost_usd', label: 'Cost (sum)', type: 'usd', aggs: [] }],
				rows: [
					{ dims: [W1, 'builder'], values: [1], n: 6, small: false },
					{ dims: [W1, 'fixer'], values: [2], n: 6, small: false },
					{ dims: [W2, 'builder'], values: [3], n: 6, small: false }
				]
			}),
			width: 400
		});
		expect(lines(container)).toHaveLength(2);
		expect(await screen.findByText('builder')).toBeInTheDocument();
		expect(screen.getByText('fixer')).toBeInTheDocument();
	});

	it('reads a row without a bucket as empty', () => {
		const { container } = render(LineViz, {
			result: weekly({ rows: [{ dims: [], values: [1, 1], n: 1, small: true }] }),
			width: 400
		});
		expect(container.querySelector('svg')).toBeInTheDocument();
	});
});
