import { render, screen, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import StepList from './StepList.svelte';

const steps = [
	{ tone: 'ok' as const, label: 'image', detail: 'pulled' },
	{ tone: 'bad' as const, label: 'firewall', detail: 'blocked', bad: true },
	{ tone: 'pending' as const, label: 'tools' }
];

describe('StepList', () => {
	it('lists each step with its mark, name and detail, in order', () => {
		render(StepList, { steps });
		const rows = screen.getAllByRole('listitem');
		expect(rows).toHaveLength(3);
		expect(within(rows[0]).getByRole('img', { name: 'ok' })).toBeInTheDocument();
		expect(rows[0]).toHaveTextContent('image');
		expect(rows[0]).toHaveTextContent('pulled');
		expect(within(rows[1]).getByRole('img', { name: 'bad' })).toBeInTheDocument();
		expect(rows[1]).toHaveTextContent('firewall');
	});

	it('shows a bad detail as an error', () => {
		render(StepList, { steps });
		expect(screen.getByText('blocked')).toHaveClass('bad');
		expect(screen.getByText('pulled')).not.toHaveClass('bad');
	});

	it('fades the name of a step that has not run', () => {
		render(StepList, { steps });
		expect(screen.getByText('tools')).toHaveClass('faint');
		expect(screen.getByText('image')).not.toHaveClass('faint');
	});

	it('adds a trailing pulsing row for what is running', () => {
		render(StepList, { steps, live: { label: 'bootstrap', detail: 'cloning' } });
		const rows = screen.getAllByRole('listitem');
		expect(rows).toHaveLength(4);
		const last = rows[3];
		expect(within(last).getByRole('img', { name: 'running' })).toBeInTheDocument();
		expect(last).toHaveTextContent('bootstrap');
		expect(last).toHaveTextContent('cloning');
	});

	it('runs on an empty list with only the live row', () => {
		render(StepList, { steps: [], live: { label: 'bootstrap' }, empty: 'No checks yet' });
		expect(screen.getAllByRole('listitem')).toHaveLength(1);
		expect(screen.queryByText('No checks yet')).toBeNull();
	});

	it('says the empty line when nothing has run and nothing is running', () => {
		render(StepList, { steps: [], empty: 'No checks yet' });
		const row = screen.getByRole('listitem');
		expect(row).toHaveTextContent('No checks yet');
		expect(within(row).getByRole('img', { name: 'none' })).toBeInTheDocument();
	});

	it('is an empty list without an empty line', () => {
		render(StepList, { steps: [] });
		expect(screen.queryByRole('listitem')).toBeNull();
	});

	it('keeps a row by its key as the label changes', async () => {
		const { rerender } = render(StepList, {
			steps: [{ key: 'a', tone: 'live' as const, label: 'image' }]
		});
		const row = screen.getByRole('listitem');

		await rerender({ steps: [{ key: 'a', tone: 'ok' as const, label: 'image pulled' }] });

		expect(screen.getByRole('listitem')).toBe(row);
		expect(row).toHaveTextContent('image pulled');
	});

	it('lets steps arrive below the ones already there', async () => {
		const { rerender } = render(StepList, { steps: steps.slice(0, 1) });
		await rerender({ steps: steps.slice(0, 2) });
		expect(screen.getAllByRole('listitem').map((r) => r.textContent)).toEqual([
			expect.stringContaining('image'),
			expect.stringContaining('firewall')
		]);
	});
});
