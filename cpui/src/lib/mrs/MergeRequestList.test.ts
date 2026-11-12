import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import type { MergeRequestRow } from '$lib/domain';
import { aWorkshop, bindable } from '$test/fixtures/app.svelte';
import { anOpenMr } from '$test/fixtures/run-components';
import MergeRequestList from './MergeRequestList.svelte';

vi.mock('$lib/api', () => ({ api: { projects: { mergeRequests: vi.fn() } } }));

const row = (over: Partial<MergeRequestRow['mr']> = {}, rest: Partial<MergeRequestRow> = {}) => ({
	mr: anOpenMr(over),
	runs: 0,
	...rest
});

beforeEach(() => {
	vi.resetAllMocks();
	fleet.projects = [aWorkshop({ id: 'p1', name: 'shop' }), aWorkshop({ id: 'p2', name: 'docs' })];
	fleet.updatedAt = 1;
	vi.mocked(api.projects.mergeRequests).mockImplementation((id) =>
		Promise.resolve(
			id === 'p1'
				? [row({ iid: 1, title: 'Add keys', author: 'ada' })]
				: [row({ iid: 2, title: 'Fix typo', author: 'bob', source: 'fix/typo' })]
		)
	);
});
afterEach(() => {
	fleet.projects = [];
	fleet.updatedAt = null;
	vi.useRealTimers();
});

describe('MergeRequestList', () => {
	it('waits for the workshops before reading', () => {
		fleet.updatedAt = null;
		render(MergeRequestList, {});
		expect(api.projects.mergeRequests).not.toHaveBeenCalled();
	});

	it('lists every workshop’s, each opening its own page', async () => {
		render(MergeRequestList, {});
		expect(await screen.findByRole('link', { name: '!1 Add keys' })).toHaveAttribute(
			'href',
			'/merge-requests/p1/1'
		);
		expect(screen.getByRole('link', { name: '!2 Fix typo' })).toHaveAttribute(
			'href',
			'/merge-requests/p2/2'
		);
		expect(screen.getByRole('link', { name: 'docs' })).toHaveAttribute('href', '/w/p2?tab=mrs');
		expect(screen.getAllByText('no run yet')).toHaveLength(2);
	});

	it('keeps to one workshop, without its column, and counts what it shows', async () => {
		const props = bindable({ projectId: 'p1', count: 0 });
		render(MergeRequestList, props);
		expect(await screen.findByText('Add keys')).toBeInTheDocument();
		expect(props.count).toBe(1);
		expect(screen.queryByText('Fix typo')).not.toBeInTheDocument();
		expect(screen.queryByText('Workshop')).not.toBeInTheDocument();
	});

	it('searches title, author and branches', async () => {
		render(MergeRequestList, { query: 'fix/' });
		expect(await screen.findByText('Fix typo')).toBeInTheDocument();
		expect(screen.queryByText('Add keys')).not.toBeInTheDocument();
	});

	it('says when nothing matches, and clears on request', async () => {
		const onclear = vi.fn();
		render(MergeRequestList, { query: 'nothing like it', onclear });
		await userEvent.click(await screen.findByRole('button', { name: 'Clear filters' }));
		expect(onclear).toHaveBeenCalled();
	});

	it('says when none are open', async () => {
		vi.mocked(api.projects.mergeRequests).mockResolvedValue([]);
		render(MergeRequestList, {});
		expect(await screen.findByText('No open merge requests')).toBeInTheDocument();
	});

	it('shows the last run and how often it was processed', async () => {
		vi.mocked(api.projects.mergeRequests).mockResolvedValue([
			row(
				{ iid: 3, title: 'Reviewed' },
				{
					runs: 2,
					run: { id: 'r1', state: 'completed', flow: 'reviewer', createdAt: '2026-11-01T00:00:00Z' }
				}
			)
		]);
		render(MergeRequestList, { projectId: 'p1' });
		expect(await screen.findByText('processed by apothecary 2 times')).toBeInTheDocument();
		expect(screen.getByText(/reviewer ·/)).toBeInTheDocument();
	});

	it('names a workshop whose repository will not answer, and still shows the rest', async () => {
		vi.mocked(api.projects.mergeRequests).mockImplementation((id) =>
			id === 'p2' ? Promise.reject(new Error('no token')) : Promise.resolve([row({ title: 'Ok' })])
		);
		render(MergeRequestList, {});
		expect(
			await screen.findByText('Could not list the merge requests of docs')
		).toBeInTheDocument();
		expect(screen.getByText('no token')).toBeInTheDocument();
		expect(screen.getByText('Ok')).toBeInTheDocument();

		vi.mocked(api.projects.mergeRequests).mockResolvedValue([]);
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		await waitFor(() =>
			expect(screen.queryByText('Could not list the merge requests of docs')).toBeNull()
		);
	});

	it('reads again every thirty seconds, unless the tab is hidden', async () => {
		vi.useFakeTimers();
		render(MergeRequestList, {});
		await vi.advanceTimersByTimeAsync(0);
		expect(api.projects.mergeRequests).toHaveBeenCalledTimes(2);
		await vi.advanceTimersByTimeAsync(30_000);
		expect(api.projects.mergeRequests).toHaveBeenCalledTimes(4);
		vi.spyOn(document, 'hidden', 'get').mockReturnValue(true);
		await vi.advanceTimersByTimeAsync(30_000);
		expect(api.projects.mergeRequests).toHaveBeenCalledTimes(4);
	});
});
