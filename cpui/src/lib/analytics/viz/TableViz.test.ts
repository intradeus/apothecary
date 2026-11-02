import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { AnalyticsResult } from '$lib/domain';
import { aResult } from '$test/fixtures/analytics';
import TableViz from './TableViz.svelte';

const firstCells = () =>
	screen
		.getAllByRole('row')
		.slice(1)
		.map((r) => within(r).getAllByRole('cell')[0].textContent?.trim());

const ranked = (): AnalyticsResult =>
	aResult({
		rows: [
			{ dims: ['abc'], values: [0.25], n: 12, small: false, score: 40.4 },
			{ dims: ['def'], values: [null], n: 3, small: true, score: 100 },
			{ dims: ['ghi'], values: [2], n: 7, small: false }
		],
		labels: { flow_hash: { abc: 'builder v3', def: 'builder v4' } }
	});

describe('TableViz', () => {
	it('shows labelled dimensions, formatted measures and n', () => {
		render(TableViz, { result: aResult(), ondrill: vi.fn() });
		const heads = screen.getAllByRole('columnheader').map((h) => h.textContent?.trim());
		expect(heads).toEqual(['Flow version', 'Cost (median)', 'n']);
		expect(firstCells()).toEqual(['builder v3', 'builder v4']);
		expect(screen.getByText('$1.500')).toBeInTheDocument();
	});

	it('adds the score column when ranked, rounded, a dash where unscored', () => {
		render(TableViz, { result: ranked(), ondrill: vi.fn() });
		expect(screen.getByRole('columnheader', { name: /Score/ })).toBeInTheDocument();
		expect(screen.getByText('40')).toBeInTheDocument();
		expect(screen.getAllByText('—')).toHaveLength(2);
	});

	it('shows only n for a count', () => {
		render(TableViz, { result: aResult({ measures: [] }), ondrill: vi.fn() });
		const heads = screen.getAllByRole('columnheader').map((h) => h.textContent?.trim());
		expect(heads).toEqual(['Flow version', 'n']);
	});

	it('sorts by a header, the other way on a second click, missing values last', async () => {
		render(TableViz, { result: ranked(), ondrill: vi.fn() });
		expect(firstCells()).toEqual(['builder v3', 'builder v4', 'ghi']);

		const cost = screen.getByRole('button', { name: 'Cost (median)' });
		await userEvent.click(cost);
		expect(firstCells()).toEqual(['builder v3', 'ghi', 'builder v4']);
		expect(screen.getByRole('columnheader', { name: /Cost/ })).toHaveAttribute(
			'aria-sort',
			'ascending'
		);
		await userEvent.click(cost);
		expect(firstCells()).toEqual(['ghi', 'builder v3', 'builder v4']);
		expect(screen.getByRole('columnheader', { name: /Cost/ })).toHaveAttribute(
			'aria-sort',
			'descending'
		);

		await userEvent.click(screen.getByRole('button', { name: 'Flow version' }));
		expect(firstCells()).toEqual(['builder v3', 'builder v4', 'ghi']);
		await userEvent.click(screen.getByRole('button', { name: /Score/ }));
		expect(firstCells()).toEqual(['builder v3', 'builder v4', 'ghi']);
		await userEvent.click(screen.getByRole('button', { name: /Score/ }));
		expect(firstCells()).toEqual(['builder v4', 'builder v3', 'ghi']);
	});

	it('keeps rows with both values missing in place', async () => {
		render(TableViz, {
			result: aResult({
				rows: [
					{ dims: ['abc'], values: [null], n: 1, small: true },
					{ dims: ['def'], values: [null], n: 1, small: true }
				]
			}),
			ondrill: vi.fn()
		});
		await userEvent.click(screen.getByRole('button', { name: 'Cost (median)' }));
		expect(firstCells()).toEqual(['builder v3', 'builder v4']);
	});

	it('opens the runs behind a row', async () => {
		const ondrill = vi.fn();
		const r = aResult();
		render(TableViz, { result: r, ondrill });
		await userEvent.click(screen.getByRole('button', { name: 'builder v4' }));
		expect(ondrill).toHaveBeenCalledWith(r.rows[1]);
	});

	it('says when the rows were cut off', () => {
		render(TableViz, { result: aResult({ truncated: true }), ondrill: vi.fn() });
		expect(screen.getByText('Only the first 500 rows are shown.')).toBeInTheDocument();
	});
});
