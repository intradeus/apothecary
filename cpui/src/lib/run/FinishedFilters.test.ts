import { render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import { choose } from '$test/fixtures/app.svelte';
import { ANY_FINISHED } from './filter';
import FinishedFilters from './FinishedFilters.svelte';

describe('FinishedFilters', () => {
	it('shows any outcome, archived or not, by default', () => {
		render(FinishedFilters, { value: ANY_FINISHED, onchange: vi.fn() });
		expect(screen.getByRole('combobox', { name: 'Outcome' })).toHaveTextContent('Any outcome');
		expect(screen.getByRole('combobox', { name: 'Archived' })).toHaveTextContent('Archived or not');
	});

	it('changes one filter and keeps the other', async () => {
		const onchange = vi.fn();
		render(FinishedFilters, { value: { outcome: 'failed', archived: 'any' }, onchange });

		await choose('Archived', 'Not archived');
		expect(onchange).toHaveBeenLastCalledWith({ outcome: 'failed', archived: 'no' });

		await choose('Outcome', 'Abandoned');
		expect(onchange).toHaveBeenLastCalledWith({ outcome: 'abandoned', archived: 'any' });
	});
});
