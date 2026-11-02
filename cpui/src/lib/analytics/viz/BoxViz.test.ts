import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import type { Agg, AnalyticsResult, MeasureRef } from '$lib/domain';
import { aResult } from '$test/fixtures/analytics';
import BoxViz from './BoxViz.svelte';

const AGGS: Agg[] = ['p25', 'median', 'p75', 'min', 'max'];
const measures: MeasureRef[] = AGGS.map((agg) => ({ field: 'wall_ms', agg }));
const spread = (over: Partial<AnalyticsResult> = {}) =>
	aResult({
		measures: AGGS.map((a) => ({ id: 'wall_ms', label: `Wall (${a})`, type: 'ms', aggs: [] })),
		rows: [
			// p25, median, p75, min, max
			{ dims: ['abc'], values: [60_000, 120_000, 180_000, 30_000, 300_000], n: 20, small: false },
			{ dims: ['def'], values: [90_000, 100_000, 200_000, 10_000, 400_000], n: 20, small: false },
			{ dims: ['ghi'], values: [null, null, null, null, null], n: 1, small: true }
		],
		...over
	});

describe('BoxViz', () => {
	it('draws a box per row with a value, labelled by its median', () => {
		const { container } = render(BoxViz, {
			result: spread(),
			measures,
			width: 400,
			ondrill: vi.fn()
		});
		expect(container.querySelectorAll('.lc-boxplot-box')).toHaveLength(2);
		expect(screen.getByRole('button', { name: 'builder v3: median 2m 00s' })).toBeInTheDocument();
	});

	it('says what it needs when the query lacks one of the five', () => {
		render(BoxViz, {
			result: spread(),
			measures: measures.slice(0, 4),
			width: 400,
			ondrill: vi.fn()
		});
		expect(
			screen.getByText(/A box plot needs the min, p25, median, p75 and max/)
		).toBeInTheDocument();
		render(BoxViz, {
			result: aResult({ measures: [] }),
			measures: [],
			width: 400,
			ondrill: vi.fn()
		});
		expect(screen.getAllByText(/A box plot needs/)).toHaveLength(2);
	});

	it('opens the runs behind a clicked box', async () => {
		const ondrill = vi.fn();
		const r = spread();
		render(BoxViz, { result: r, measures, width: 400, ondrill });
		const box = screen.getByRole('button', { name: 'builder v4: median 1m 40s' });
		await fireEvent.click(box);
		expect(ondrill).toHaveBeenCalledWith(r.rows[1]);
		await fireEvent.keyDown(box, { key: 'Tab' });
		expect(ondrill).toHaveBeenCalledTimes(1);
		await fireEvent.keyDown(box, { key: 'Enter' });
		expect(ondrill).toHaveBeenCalledTimes(2);
	});
});
