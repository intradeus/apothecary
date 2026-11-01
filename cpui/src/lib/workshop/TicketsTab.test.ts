import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fleet } from '$lib/app/fleet.svelte';
import type { Ticket } from '$lib/domain';
import { aRun, aTicket } from '$test/fixtures';
import { aWorkshop, choose } from '$test/fixtures/app.svelte';
import WithHeader from '$test/fixtures/WithHeader.svelte';
import TicketsTab from './TicketsTab.svelte';

vi.mock('$lib/api', () => ({ api: {} }));

const project = aWorkshop({ id: 'p1' });
const setup = (tickets: Ticket[] | null, over: Record<string, unknown> = {}) =>
	render(WithHeader, {
		props: {
			page: TicketsTab,
			props: { project, tickets, error: '', onretry: vi.fn(), ...over }
		}
	});
const search = () => screen.getByPlaceholderText('Search title, key, goal');

beforeEach(() => {
	fleet.reset();
});

describe('TicketsTab', () => {
	it('puts the new-ticket action in the page header', () => {
		const { container } = setup([]);
		const link = container.querySelector('header a');
		expect(link).toHaveTextContent('New ticket');
		expect(link).toHaveAttribute('href', '/tickets/new?project=p1');
	});

	it('shows a skeleton while loading', () => {
		setup(null);
		expect(screen.queryByText('No tickets yet')).not.toBeInTheDocument();
		expect(screen.queryByRole('link', { name: 'Add keys' })).not.toBeInTheDocument();
	});

	it('offers a retry when loading failed', async () => {
		const onretry = vi.fn();
		setup(null, { error: 'down', onretry });
		expect(screen.getByText('Couldn’t load tickets')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(onretry).toHaveBeenCalledOnce();
	});

	it('keeps the list when a reload fails', () => {
		setup([aTicket({ title: 'Add keys' })], { error: 'down' });
		expect(screen.getByRole('link', { name: 'Add keys' })).toBeInTheDocument();
		expect(screen.queryByText('Couldn’t load tickets')).not.toBeInTheDocument();
	});

	it('invites the first ticket', () => {
		setup([]);
		expect(screen.getByText('No tickets yet')).toBeInTheDocument();
	});

	it('lists the newest first', () => {
		const { container } = setup([
			aTicket({ id: 'a', title: 'Older', createdAt: '2026-10-01T00:00:00Z' }),
			aTicket({ id: 'b', title: 'Newer', createdAt: '2026-10-02T00:00:00Z' })
		]);
		const text = container.textContent ?? '';
		expect(text.indexOf('Newer')).toBeLessThan(text.indexOf('Older'));
	});

	it.each([
		['title', 'KEYS'],
		['tracker key', 'proj-7'],
		['goal', 'retry safely']
	])('finds a ticket by %s', async (_, q) => {
		setup([
			aTicket({
				id: 'a',
				title: 'Add keys',
				goal: 'Retry safely',
				source: { key: 'PROJ-7' } as never
			}),
			aTicket({ id: 'b', title: 'Other', goal: 'Nothing' })
		]);
		await userEvent.type(search(), q);
		expect(screen.getByRole('link', { name: 'Add keys' })).toBeInTheDocument();
		expect(screen.queryByRole('link', { name: 'Other' })).not.toBeInTheDocument();
	});

	it('narrows to tickets with an active run', async () => {
		fleet.runs = [
			aRun({ id: 'r', ticketId: 'a' }),
			aRun({ id: 's', ticketId: 'b', state: 'failed' })
		];
		setup([aTicket({ id: 'a', title: 'Live' }), aTicket({ id: 'b', title: 'Stopped' })]);

		await userEvent.click(screen.getByRole('switch', { name: 'Has an active run' }));
		expect(screen.getByRole('link', { name: 'Live' })).toBeInTheDocument();
		expect(screen.queryByRole('link', { name: 'Stopped' })).not.toBeInTheDocument();
	});

	it('hides done tickets until asked', async () => {
		const done = { id: 's', name: 'Closed', isDone: true };
		setup([
			aTicket({ id: 'a', title: 'Open one' }),
			aTicket({ id: 'b', title: 'Shipped', status: done })
		]);
		expect(screen.getByRole('link', { name: 'Open one' })).toBeInTheDocument();
		expect(screen.queryByRole('link', { name: 'Shipped' })).not.toBeInTheDocument();

		await userEvent.click(screen.getByRole('switch', { name: 'Show done' }));
		expect(screen.getByRole('link', { name: 'Shipped' })).toBeInTheDocument();
	});

	it('narrows to one status', async () => {
		setup([
			aTicket({ id: 'a', title: 'Waiting', status: { id: '1', name: 'To Do', isDone: false } }),
			aTicket({ id: 'b', title: 'Going', status: { id: '2', name: 'Doing', isDone: false } })
		]);
		await choose('Filter by status', 'Doing');
		expect(screen.getByRole('link', { name: 'Going' })).toBeInTheDocument();
		expect(screen.queryByRole('link', { name: 'Waiting' })).not.toBeInTheDocument();
	});

	it('says nothing matched rather than inviting a first ticket', async () => {
		setup([aTicket()]);
		await userEvent.type(search(), 'zzz');
		expect(screen.getByText('No ticket matches this filter.')).toBeInTheDocument();
		expect(screen.queryByText('No tickets yet')).not.toBeInTheDocument();
	});

	it('links to the desk-wide list, filtered', () => {
		setup([]);
		expect(screen.getByRole('link', { name: /Open in Tickets/ })).toHaveAttribute(
			'href',
			'/tickets?project=p1'
		);
	});
});
