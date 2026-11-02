import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { aResult } from '$test/fixtures/analytics';
import ScatterViz from './ScatterViz.svelte';

const points = (c: HTMLElement) => [...c.querySelectorAll('circle')];

describe('ScatterViz', () => {
	it('plots a point per row, first measure across and second up', () => {
		const { container } = render(ScatterViz, {
			result: aResult({
				measures: [
					{ id: 'cost_usd', label: 'Cost', type: 'usd', aggs: [] },
					{ id: 'wall_ms', label: 'Wall', type: 'ms', aggs: [] }
				],
				rows: [
					{ dims: ['abc'], values: [0.5, 60_000], n: 9, small: false },
					{ dims: ['def'], values: [1, 120_000], n: 9, small: false },
					{ dims: ['ghi'], values: [1, null], n: 9, small: false }
				]
			}),
			width: 400
		});
		// The row missing a value has no place on the plane.
		expect(points(container)).toHaveLength(2);
		expect(container.textContent).toMatch(/\$/);
		expect(container.textContent).toMatch(/\dm/);
	});

	it('puts the count up when there is one measure', () => {
		const { container } = render(ScatterViz, { result: aResult(), width: 400 });
		expect(points(container)).toHaveLength(2);
	});
});
