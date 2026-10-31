import { render, screen } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fleet } from '$lib/app/fleet.svelte';
import { aProject, aRun, aTicket } from '$test/fixtures';
import TicketTable from './TicketTable.svelte';

vi.mock('$lib/api', () => ({ api: {} }));

beforeEach(() => {
	vi.useFakeTimers({ now: new Date('2026-10-31T12:00:00Z') });
	fleet.reset();
	fleet.projects = [aProject({ id: 'p1', name: 'billing' })];
});
afterEach(() => {
	vi.useRealTimers();
});

describe('TicketTable', () => {
	it('has a head for each column, in the order given', () => {
		const { container } = render(TicketTable, { tickets: [], cols: ['type', 'title'] });
		expect(container).toHaveTextContent(/^TypeTitle$/);
	});

	it('shows every column by default', () => {
		const { container } = render(TicketTable, { tickets: [] });
		expect(container).toHaveTextContent(/^TitleTypeSourceWorkshopBase branchRunsCreated$/);
	});

	it('links a ticket and shows its facts', () => {
		render(TicketTable, {
			tickets: [
				aTicket({
					id: 't1',
					title: 'Add keys',
					projectId: 'p1',
					changeType: 'fix',
					baseBranch: 'fix/keys'
				})
			]
		});
		expect(screen.getByRole('link', { name: 'Add keys' })).toHaveAttribute('href', '/tickets/t1');
		expect(screen.getByText('fix')).toBeInTheDocument();
		expect(screen.getByText('billing')).toBeInTheDocument();
		expect(screen.getByText('fix/keys')).toBeInTheDocument();
	});

	it('shows a tracker key with where it came from, a dash for one written here', () => {
		render(TicketTable, {
			tickets: [
				aTicket({ id: 'a', source: { key: 'PROJ-7', kind: 'jira' } as never }),
				aTicket({ id: 'b' })
			],
			cols: ['source']
		});
		expect(screen.getByText('PROJ-7')).toHaveAttribute('title', 'Imported from jira');
		expect(screen.getByText('—')).toBeInTheDocument();
	});

	it('reads no runs before there are any', () => {
		render(TicketTable, { tickets: [aTicket()], cols: ['runs'] });
		expect(screen.getByText('no runs')).toBeInTheDocument();
	});

	it("shows the latest run's state, and how many there were", () => {
		fleet.runs = [
			aRun({ id: 'old', ticketId: 't1', state: 'failed', createdAt: '2026-10-01T00:00:00Z' }),
			aRun({ id: 'new', ticketId: 't1', state: 'completed', createdAt: '2026-10-02T00:00:00Z' })
		];
		render(TicketTable, { tickets: [aTicket({ id: 't1' })], cols: ['runs'] });
		expect(screen.getByText(/completed/i)).toBeInTheDocument();
		expect(screen.queryByText(/failed/i)).not.toBeInTheDocument();
		expect(screen.getByText('×2')).toBeInTheDocument();
	});

	it('says how long ago it was created', () => {
		render(TicketTable, {
			tickets: [aTicket({ createdAt: '2026-10-29T12:00:00Z' })],
			cols: ['created']
		});
		expect(screen.getByText('2d ago')).toBeInTheDocument();
	});
});
