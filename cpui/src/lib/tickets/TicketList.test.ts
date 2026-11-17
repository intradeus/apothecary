import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { TicketPage } from '$lib/domain';
import { fleet } from '$lib/app/fleet.svelte';
import { aTicketRow } from '$test/fixtures';
import { choose } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import TicketList from './TicketList.svelte';

vi.mock('$lib/api', () => ({ api: { projects: { tickets: vi.fn() } } }));

const page = (keys: string[], next = ''): TicketPage => ({
	tickets: keys.map((k) => aTicketRow({ key: k, title: `Title ${k}` })),
	next
});
const list = () => vi.mocked(api.projects.tickets);
const setup = (over: Record<string, unknown> = {}) =>
	render(TicketList, { projectId: 'p1', ...over });

beforeEach(() => {
	vi.resetAllMocks();
	fleet.reset();
	list().mockResolvedValue(page(['A-1']));
});

describe('TicketList', () => {
	it("reads the workshop's open tickets", async () => {
		setup();
		expect(await screen.findByRole('link', { name: 'Title A-1' })).toHaveAttribute(
			'href',
			'/w/p1/tickets/A-1'
		);
		expect(list()).toHaveBeenCalledWith('p1', {
			q: undefined,
			done: 'open',
			cursor: undefined,
			refresh: undefined
		});
	});

	it('loads the next page with the cursor, and stops on the last', async () => {
		list()
			.mockResolvedValueOnce(page(['A-1'], 'c2'))
			.mockResolvedValueOnce(page(['A-2']));
		setup();
		await userEvent.click(await screen.findByRole('button', { name: 'Load more' }));
		expect(await screen.findByText('Title A-2')).toBeInTheDocument();
		expect(screen.getByText('Title A-1')).toBeInTheDocument();
		expect(list()).toHaveBeenLastCalledWith('p1', expect.objectContaining({ cursor: 'c2' }));
		expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
	});

	it('asks the tracker for done tickets', async () => {
		setup();
		await screen.findByText('Title A-1');
		await choose('Show', 'Done');
		await waitFor(() =>
			expect(list()).toHaveBeenLastCalledWith('p1', expect.objectContaining({ done: 'done' }))
		);
	});

	it('searches on Enter, and shows everything again once cleared', async () => {
		setup();
		const box = await screen.findByRole('searchbox', { name: 'Search the tracker' });
		await userEvent.type(box, ' keys {Enter}');
		await waitFor(() =>
			expect(list()).toHaveBeenLastCalledWith('p1', expect.objectContaining({ q: 'keys' }))
		);
		await userEvent.clear(box);
		await waitFor(() =>
			expect(list()).toHaveBeenLastCalledWith('p1', expect.objectContaining({ q: undefined }))
		);
	});

	it('reads the tracker now on Refresh', async () => {
		setup();
		await screen.findByText('Title A-1');
		await userEvent.click(screen.getByRole('button', { name: /Refresh/ }));
		expect(list()).toHaveBeenLastCalledWith('p1', expect.objectContaining({ refresh: true }));
	});

	it('shows a skeleton, then a failure with a retry', async () => {
		list().mockRejectedValueOnce(new Error('rate limited'));
		setup();
		expect(await screen.findByText('rate limited')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('Title A-1')).toBeInTheDocument();
	});

	it('keeps the rows when loading more fails', async () => {
		list()
			.mockResolvedValueOnce(page(['A-1'], 'c2'))
			.mockRejectedValueOnce(new Error('down'));
		setup();
		await userEvent.click(await screen.findByRole('button', { name: 'Load more' }));
		expect(await screen.findByText('down')).toBeInTheDocument();
		expect(screen.getByText('Title A-1')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
		expect(screen.queryByText('down')).not.toBeInTheDocument();
	});

	it('drops an answer a later load overtook', async () => {
		const slow = deferred<TicketPage>();
		list()
			.mockReturnValueOnce(slow.promise)
			.mockResolvedValueOnce(page(['B-1']));
		const { rerender } = setup();
		await rerender({ projectId: 'p2' });
		expect(await screen.findByText('Title B-1')).toBeInTheDocument();
		slow.resolve(page(['OLD']));
		await Promise.resolve();
		expect(screen.queryByText('Title OLD')).not.toBeInTheDocument();
	});

	it('says when the source has no tickets, with what the page offers', async () => {
		list().mockResolvedValue(page([]));
		const { container } = setup();
		expect(await screen.findByText('No open tickets')).toBeInTheDocument();
		expect(container).toBeTruthy();
	});

	it('says when nothing matches the filter', async () => {
		list().mockResolvedValue(page([]));
		setup();
		await screen.findByText('No open tickets');
		await choose('Show', 'All');
		expect(await screen.findByText('No tickets match')).toBeInTheDocument();
	});
});
