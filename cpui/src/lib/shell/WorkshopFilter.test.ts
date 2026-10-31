import { render, screen } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fleet } from '$lib/app/fleet.svelte';
import { aProject } from '$test/fixtures';
import { choose } from '$test/fixtures/app.svelte';
import WorkshopFilter from './WorkshopFilter.svelte';

vi.mock('$lib/api', () => ({ api: {} }));

beforeEach(() => {
	fleet.projects = [aProject({ id: 'p1', name: 'billing' }), aProject({ id: 'p2', name: 'web' })];
});

describe('WorkshopFilter', () => {
	it('reads All workshops for no filter', () => {
		render(WorkshopFilter, { value: '', onchange: vi.fn() });
		expect(screen.getByRole('combobox', { name: 'Filter by workshop' })).toHaveTextContent(
			'All workshops'
		);
	});

	it('shows the workshop the page filters on', () => {
		render(WorkshopFilter, { value: 'p2', onchange: vi.fn() });
		expect(screen.getByRole('combobox', { name: 'Filter by workshop' })).toHaveTextContent('web');
	});

	it('reports a workshop by id, and all of them as empty', async () => {
		const onchange = vi.fn();
		render(WorkshopFilter, { value: 'p1', onchange });

		await choose('Filter by workshop', 'web');
		expect(onchange).toHaveBeenLastCalledWith('p2');

		await choose('Filter by workshop', 'All workshops');
		expect(onchange).toHaveBeenLastCalledWith('');
	});
});
