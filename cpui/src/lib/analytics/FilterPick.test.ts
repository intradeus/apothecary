import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { AnalyticsValue } from '$lib/domain';
import { deferred } from '$test/helpers';
import FilterPick from './FilterPick.svelte';

vi.mock('$lib/api', () => ({ api: { analytics: { values: vi.fn() } } }));

const VALUES: AnalyticsValue[] = [
	{ value: 'builder', label: 'builder' },
	{ value: 'p1', label: 'Bench' },
	{ value: '', label: '' }
];
const props = { label: 'Flow', grain: 'runs' as const, field: 'flow_name', value: [] };
const opener = () => screen.getByRole('button', { name: /^Flow:/ });

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.analytics.values).mockResolvedValue(VALUES);
});

describe('FilterPick', () => {
	it('reads any while nothing is picked, and asks nothing until opened', () => {
		render(FilterPick, props);
		expect(opener()).toHaveTextContent('Flow: any');
		expect(api.analytics.values).not.toHaveBeenCalled();
	});

	it('lists the values for the range on open and picks any number', async () => {
		const onchange = vi.fn();
		render(FilterPick, { ...props, from: '2026-08-01T00:00:00Z', onchange });
		await userEvent.click(opener());
		expect(api.analytics.values).toHaveBeenCalledWith('runs', 'flow_name', {
			from: '2026-08-01T00:00:00Z'
		});
		await userEvent.click(await screen.findByRole('checkbox', { name: 'Bench' }));
		expect(onchange).toHaveBeenLastCalledWith(['p1']);
		expect(opener()).toHaveTextContent('Flow: Bench');
		expect(opener()).toHaveClass('btn-sel');

		await userEvent.click(screen.getByRole('checkbox', { name: '(none)' }));
		expect(onchange).toHaveBeenLastCalledWith(['p1', '']);
		expect(opener()).toHaveTextContent('Flow: 2 picked');
	});

	it('clears the picks', async () => {
		const onchange = vi.fn();
		render(FilterPick, { ...props, value: ['builder'], onchange });
		expect(opener()).toHaveTextContent('Flow: builder');
		await userEvent.click(opener());
		await userEvent.click(screen.getByRole('button', { name: 'Clear' }));
		expect(onchange).toHaveBeenLastCalledWith([]);
		expect(opener()).toHaveTextContent('Flow: any');
	});

	it('asks for all time without a range', async () => {
		render(FilterPick, { label: 'Flow', grain: 'runs', field: 'flow_name' });
		await userEvent.click(opener());
		expect(api.analytics.values).toHaveBeenCalledWith('runs', 'flow_name', {});
	});

	it('shows it is loading, then that the range has no values', async () => {
		const d = deferred<AnalyticsValue[]>();
		vi.mocked(api.analytics.values).mockReturnValue(d.promise);
		render(FilterPick, props);
		await userEvent.click(opener());
		expect(screen.getByRole('status')).toBeInTheDocument();
		d.resolve([]);
		expect(await screen.findByText('No values in this range.')).toBeInTheDocument();
	});

	it('shows why the values could not be listed', async () => {
		vi.mocked(api.analytics.values).mockRejectedValue(new Error('down'));
		render(FilterPick, props);
		await userEvent.click(opener());
		expect(await screen.findByText('down')).toBeInTheDocument();
	});
});
