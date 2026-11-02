import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import type { AnalyticsResult } from '$lib/domain';
import { aResult } from '$test/fixtures/analytics';
import HeatmapViz from './HeatmapViz.svelte';

const grid = (over: Partial<AnalyticsResult> = {}) =>
	aResult({
		dims: [
			{ id: 'stage_id', label: 'Stage', type: 'text', aggs: [] },
			{ id: 'iteration', label: 'Iteration', type: 'text', aggs: [] }
		],
		measures: [],
		rows: [
			{ dims: ['plan', '1'], values: [], n: 10, small: false },
			{ dims: ['build', '1'], values: [], n: 5, small: false },
			{ dims: ['build', '2'], values: [], n: 0, small: true }
		],
		labels: {},
		...over
	});

const cells = (c: HTMLElement) => [...c.querySelectorAll<SVGElement>('.cell')];

describe('HeatmapViz', () => {
	it('draws a cell per row, shaded by its value, with the value on it', () => {
		const { container } = render(HeatmapViz, { result: grid(), width: 400, ondrill: vi.fn() });
		const all = cells(container);
		expect(all).toHaveLength(3);
		const alpha = all.map((c) => Number(c.getAttribute('fill-opacity')));
		expect(alpha[0]).toBe(1);
		expect(alpha[1]).toBeCloseTo(0.56);
		expect(alpha[2]).toBeCloseTo(0.12);
		expect(screen.getByRole('button', { name: 'plan, 1: 10' })).toBeInTheDocument();
		expect(screen.getByText('5')).toBeInTheDocument();
	});

	it('keeps an empty grid faint', () => {
		const { container } = render(HeatmapViz, {
			result: grid({
				measures: [{ id: 'cost_usd', label: 'Cost', type: 'usd', aggs: [] }],
				rows: [{ dims: ['plan'], values: [null], n: 1, small: true }]
			}),
			width: 400,
			ondrill: vi.fn()
		});
		expect(cells(container)[0].getAttribute('fill-opacity')).toBe('0.08');
		expect(screen.getByRole('button', { name: 'plan, (none): —' })).toBeInTheDocument();
	});

	it('opens the runs behind a clicked cell', async () => {
		const ondrill = vi.fn();
		const r = grid();
		render(HeatmapViz, { result: r, width: 400, ondrill });
		const cell = screen.getByRole('button', { name: 'build, 2: 0' });
		await fireEvent.click(cell);
		expect(ondrill).toHaveBeenCalledWith(r.rows[2]);
		await fireEvent.keyDown(cell, { key: 'Tab' });
		expect(ondrill).toHaveBeenCalledTimes(1);
		await fireEvent.keyDown(cell, { key: 'Enter' });
		expect(ondrill).toHaveBeenCalledTimes(2);
	});
	it('keeps an all-zero grid faint', () => {
		const { container } = render(HeatmapViz, {
			result: grid({ rows: [{ dims: ['plan', '1'], values: [], n: 0, small: true }] }),
			width: 400,
			ondrill: vi.fn()
		});
		expect(cells(container)[0].getAttribute('fill-opacity')).toBe('0.08');
	});
});
