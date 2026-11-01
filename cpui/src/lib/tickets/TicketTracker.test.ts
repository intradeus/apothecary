import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Ticket, TrackerStatus } from '$lib/domain';
import { aTicket } from '$test/fixtures';
import { anIntegration, choose } from '$test/fixtures/app.svelte';
import TicketTracker from './TicketTracker.svelte';

vi.mock('$lib/api', () => ({
	api: {
		integrations: { list: vi.fn(), statuses: vi.fn() },
		tickets: { setStatus: vi.fn(), setLabels: vi.fn(), sync: vi.fn() }
	}
}));

const status = (id: string, name: string, position: number, isDone = false): TrackerStatus => ({
	id,
	integrationId: 'own',
	remoteId: '',
	name,
	remoteCategory: '',
	position,
	isDone
});
const todo = { id: 's1', name: 'To Do', isDone: false };

const setup = (over: Partial<Ticket> = {}) => {
	const onchange = vi.fn();
	render(TicketTracker, { ticket: aTicket({ id: 't1', status: todo, ...over }), onchange });
	return { onchange };
};

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.integrations.list).mockResolvedValue([
		anIntegration({ id: 'jira', port: 'ticketsource', kind: 'jira' }),
		anIntegration({ id: 'own', port: 'ticketsource', kind: 'internal' })
	]);
	vi.mocked(api.integrations.statuses).mockResolvedValue({
		statuses: [status('s1', 'To Do', 0), status('s2', 'Done', 1, true)],
		warnings: []
	});
});

describe('TicketTracker, the status', () => {
	it("moves a ticket with no source through the user's internal tracker", async () => {
		const moved = aTicket({ id: 't1', status: { id: 's2', name: 'Done', isDone: true } });
		vi.mocked(api.tickets.setStatus).mockResolvedValue(moved);
		const { onchange } = setup();
		await waitFor(() => expect(api.integrations.statuses).toHaveBeenCalledWith('own'));
		await choose('Change status', /^Done/);

		expect(api.tickets.setStatus).toHaveBeenCalledWith('t1', 's2');
		await waitFor(() => expect(onchange).toHaveBeenCalledWith(moved));
	});

	it("reads a sourced ticket's statuses from its own tracker", async () => {
		setup({ source: { integrationId: 'jira', kind: 'jira', key: 'P-1' } });
		await waitFor(() => expect(api.integrations.statuses).toHaveBeenCalledWith('jira'));
		expect(api.integrations.list).not.toHaveBeenCalled();
	});

	it('shows a refused move next to it', async () => {
		vi.mocked(api.tickets.setStatus).mockRejectedValue(new Error('not reachable from To Do'));
		setup();
		await waitFor(() => expect(api.integrations.statuses).toHaveBeenCalled());
		await choose('Change status', /^Done/);
		expect(await screen.findByText('not reachable from To Do')).toBeInTheDocument();
	});
});

describe('TicketTracker, the labels', () => {
	it('adds a label', async () => {
		vi.mocked(api.tickets.setLabels).mockResolvedValue(aTicket());
		setup({ labels: ['ui'] });
		await userEvent.type(screen.getByRole('textbox', { name: 'Add a label…' }), 'api{Enter}');
		expect(api.tickets.setLabels).toHaveBeenCalledWith('t1', ['ui', 'api']);
	});

	it('removes a label', async () => {
		vi.mocked(api.tickets.setLabels).mockResolvedValue(aTicket());
		setup({ labels: ['ui', 'api'] });
		await userEvent.click(screen.getByRole('button', { name: 'Remove label ui' }));
		expect(api.tickets.setLabels).toHaveBeenCalledWith('t1', ['api']);
	});

	it('refuses a label with a comma before sending it', async () => {
		setup();
		await userEvent.type(screen.getByRole('textbox', { name: 'Add a label…' }), 'a,b{Enter}');
		expect(screen.getByText('A label can’t contain a comma.')).toBeInTheDocument();
		expect(api.tickets.setLabels).not.toHaveBeenCalled();
	});
});

describe('TicketTracker, freshness', () => {
	beforeEach(() => {
		vi.useFakeTimers({ now: new Date('2026-10-31T12:00:00Z'), shouldAdvanceTime: true });
		return () => vi.useRealTimers();
	});

	it('says when a sourced ticket last synced, and refreshes it', async () => {
		const fresh = aTicket({ id: 't1' });
		vi.mocked(api.tickets.sync).mockResolvedValue(fresh);
		const { onchange } = setup({
			source: { integrationId: 'jira', kind: 'jira', key: 'P-1' },
			syncedAt: '2026-10-31T11:55:00Z'
		});
		expect(screen.getByText('synced 5m ago')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Refresh/ }));
		expect(api.tickets.sync).toHaveBeenCalledWith('t1');
		await waitFor(() => expect(onchange).toHaveBeenCalledWith(fresh));
	});

	it('warns when the last sync failed', () => {
		setup({ syncState: 'error', syncError: 'timeout' });
		expect(screen.getByRole('alert')).toHaveTextContent('The last sync failed: timeout');
	});

	it('says when the issue is gone', () => {
		setup({
			source: { integrationId: 'jira', kind: 'jira', key: 'P-1' },
			syncState: 'gone'
		});
		expect(screen.getByRole('alert')).toHaveTextContent('The issue is gone from jira');
	});

	it('lists the links', () => {
		setup({ links: [{ url: 'https://x.test/spec', title: 'Spec' }] });
		expect(screen.getByRole('link', { name: /Spec/ })).toHaveAttribute(
			'href',
			'https://x.test/spec'
		);
	});
});
