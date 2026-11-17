import { render, screen } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fleet } from '$lib/app/fleet.svelte';
import { aRun, aTicketRow } from '$test/fixtures';
import TicketTable from './TicketTable.svelte';

vi.mock('$lib/api', () => ({ api: {} }));

const setup = (props: Partial<Parameters<typeof TicketTable>[1]> = {}) =>
	render(TicketTable, { projectId: 'p1', tickets: [], ...props });

beforeEach(() => {
	vi.useFakeTimers({ now: new Date('2026-10-31T12:00:00Z') });
	fleet.reset();
});
afterEach(() => {
	vi.useRealTimers();
});

describe('TicketTable', () => {
	it('has a head for each column, in the order given', () => {
		const { container } = setup({ cols: ['type', 'title'] });
		expect(container).toHaveTextContent(/^TypeTitle$/);
	});

	it('shows every column by default', () => {
		const { container } = setup();
		expect(container).toHaveTextContent(/^KeyTitleStatusTypeBase branchActive runUpdated$/);
	});

	it('links a ticket to its page in the workshop, and shows its facts', () => {
		setup({
			tickets: [
				aTicketRow({
					key: 'group/app#12',
					title: 'Add keys',
					changeType: 'fix',
					baseBranch: 'fix/keys'
				})
			]
		});
		expect(screen.getByRole('link', { name: 'Add keys' })).toHaveAttribute(
			'href',
			'/w/p1/tickets/group%2Fapp%2312'
		);
		expect(screen.getByText('group/app#12')).toBeInTheDocument();
		expect(screen.getByText('fix')).toBeInTheDocument();
		expect(screen.getByText('fix/keys')).toBeInTheDocument();
	});

	it("tints the tracker status by whether it's done", () => {
		setup({
			tickets: [
				aTicketRow({ key: 'a', status: 'In Review' }),
				aTicketRow({ key: 'b', status: 'Closed', done: true })
			],
			cols: ['status']
		});
		expect(screen.getByText('In Review')).toHaveClass('tint-muted');
		expect(screen.getByText('Closed')).toHaveClass('tint-success');
	});

	it('reads no active run when there is none', () => {
		setup({ tickets: [aTicketRow()], cols: ['run'] });
		expect(screen.getByText('none running')).toBeInTheDocument();
	});

	it('links the active run, with its state once the fleet has it', () => {
		fleet.runs = [aRun({ id: 'r1', state: 'paused' })];
		setup({
			tickets: [
				aTicketRow({ key: 'a', activeRunId: 'r1' }),
				aTicketRow({ key: 'b', activeRunId: '01TESTRUN00000000000000XYZ' })
			],
			cols: ['run']
		});
		const links = screen.getAllByRole('link');
		expect(links[0]).toHaveAttribute('href', '/runs/r1');
		expect(links[0]).toHaveTextContent(/paused/i);
		expect(links[1]).toHaveAttribute('href', '/runs/01TESTRUN00000000000000XYZ');
	});

	it('says how long ago the tracker last changed it, a dash when it does not say', () => {
		setup({
			tickets: [
				aTicketRow({ key: 'a', updatedAt: '2026-10-29T12:00:00Z' }),
				aTicketRow({ key: 'b', updatedAt: undefined })
			],
			cols: ['updated']
		});
		expect(screen.getByText('2d ago')).toBeInTheDocument();
		expect(screen.getByText('—')).toBeInTheDocument();
	});
});
