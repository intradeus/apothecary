import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import { aResult } from '$test/fixtures/analytics';
import BarViz from './BarViz.svelte';

const bars = (c: HTMLElement) => [...c.querySelectorAll<SVGElement>('.lc-bar')];

describe('BarViz', () => {
	it('draws a bar per row, labelled by its dimensions, valued on the axis', () => {
		const { container } = render(BarViz, { result: aResult(), width: 400, ondrill: vi.fn() });
		expect(bars(container)).toHaveLength(2);
		expect(screen.getByText('builder v3')).toBeInTheDocument();
		expect(screen.getAllByText(/^\$\d/).length).toBeGreaterThan(0);
	});

	it('shortens a long row label on the axis', () => {
		const long = 'a-very-long-flow-version-name';
		render(BarViz, {
			result: aResult({ labels: { flow_hash: { abc: long } } }),
			width: 400,
			ondrill: vi.fn()
		});
		expect(screen.getByText('a-very-long-flow-…')).toBeInTheDocument();
	});

	it('draws a series per measure, a missing value as zero', () => {
		const r = aResult({
			measures: [...aResult().measures, { id: 'wall_ms', label: 'Wall', type: 'ms', aggs: [] }],
			rows: [{ dims: ['abc'], values: [0.25, null], n: 2, small: true }]
		});
		const { container } = render(BarViz, { result: r, width: 400, ondrill: vi.fn() });
		expect(bars(container)).toHaveLength(2);
	});

	it('opens the runs behind a clicked bar', async () => {
		const ondrill = vi.fn();
		const r = aResult();
		const { container } = render(BarViz, { result: r, width: 400, ondrill });
		await fireEvent.click(bars(container)[1]);
		expect(ondrill).toHaveBeenCalledWith(r.rows[1]);
	});

	it('opens them from the tooltip band over a bar, which takes a pointer’s click', async () => {
		const ondrill = vi.fn();
		const r = aResult();
		const { container } = render(BarViz, { result: r, width: 400, ondrill });
		const bands = [...container.querySelectorAll<SVGElement>('.lc-tooltip-rect')];
		expect(bands).toHaveLength(2);
		await fireEvent.click(bands[1]);
		expect(ondrill).toHaveBeenCalledWith(r.rows[1]);
	});
});
