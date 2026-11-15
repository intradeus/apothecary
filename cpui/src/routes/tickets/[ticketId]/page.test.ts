import { render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { page } from '$app/state';
import { api } from '$lib/api';
import type { TicketRecord } from '$lib/domain';
import { aTicket } from '$test/fixtures';
import Page from './+page.svelte';

// The route's params and URL as SvelteKit's page state holds them. vi.mock
// is hoisted above the imports, so its factory loads the rune fixture itself.
vi.mock('$app/state', async () => {
	const { bindable } = await import('$test/fixtures/app.svelte');
	return {
		page: bindable({
			params: { ticketId: '01TESTTICK000000000000ABCD' },
			url: new URL('http://localhost/tickets/01TESTTICK000000000000ABCD')
		})
	};
});
vi.mock('$lib/api', () => ({ api: { tickets: { get: vi.fn() } } }));

const record = (over: Partial<TicketRecord> = {}): TicketRecord => ({
	ticket: aTicket({ sourceUrl: 'https://tracker.test/browse/PROJ-7' }),
	key: 'PROJ-7',
	source: { id: '01TESTSRC0000000000000ABCD', kind: 'jira', name: 'Jira' },
	workshops: [
		{ id: 'p1', name: 'shop' },
		{ id: 'p2', name: 'docs' }
	],
	...over
});

function visit(r: TicketRecord | Error, query = '') {
	page.url.search = query;
	if (r instanceof Error) vi.mocked(api.tickets.get).mockRejectedValue(r);
	else vi.mocked(api.tickets.get).mockResolvedValue(r);
	render(Page);
}

beforeEach(() => {
	vi.resetAllMocks();
});

describe('a ticket by its record id', () => {
	it('opens it through the workshop the link names, when that one reads its source', async () => {
		visit(record(), '?workshop=p2');
		await waitFor(() =>
			expect(goto).toHaveBeenCalledWith('/w/p2/tickets/PROJ-7', { replaceState: true })
		);
		expect(api.tickets.get).toHaveBeenCalledWith('01TESTTICK000000000000ABCD');
	});

	it('opens it through the first workshop on its source otherwise, its key escaped', async () => {
		visit(record({ key: 'group/project#12' }), '?workshop=elsewhere');
		await waitFor(() =>
			expect(goto).toHaveBeenCalledWith('/w/p1/tickets/group%2Fproject%2312', {
				replaceState: true
			})
		);
	});

	it('shows the ticket as last seen when no workshop reads its source any more', async () => {
		visit(record({ workshops: [] }));
		expect(await screen.findByText('No workshop reads this ticket')).toBeInTheDocument();
		expect(screen.getByRole('heading', { name: 'Add idempotency keys' })).toBeInTheDocument();
		expect(screen.getByRole('link', { name: /Jira/ })).toHaveAttribute(
			'href',
			'https://tracker.test/browse/PROJ-7'
		);
		expect(screen.getByText(/last seen/)).toBeInTheDocument();
		expect(goto).not.toHaveBeenCalled();
	});

	it('says when there is no such ticket', async () => {
		visit(new Error('ticket not found'));
		expect(await screen.findByText('No such ticket')).toBeInTheDocument();
		expect(screen.getByText('ticket not found')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'All tickets' })).toHaveAttribute('href', '/tickets');
		expect(goto).not.toHaveBeenCalled();
	});
});
