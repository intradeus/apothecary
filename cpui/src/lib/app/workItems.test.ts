import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { OPERATES_ON } from '$lib/domain/flowdoc';
import { aTicketRow } from '$test/fixtures';
import { anOpenMr } from '$test/fixtures/run-components';
import { WORK_ITEMS, workItem } from './workItems';

vi.mock('$lib/api', () => ({
	api: { projects: { tickets: vi.fn(), mergeRequests: vi.fn() } }
}));

describe('WORK_ITEMS', () => {
	beforeEach(() => {
		vi.resetAllMocks();
	});

	// The kinds are the language's operates_on values, in its order: a kind
	// the language cannot name could never start a run.
	it('has one kind per operates_on value', () => {
		expect(WORK_ITEMS.map((w) => w.kind)).toEqual([...OPERATES_ON]);
		for (const k of OPERATES_ON) expect(workItem(k).kind).toBe(k);
	});

	it('names each for the sidebar and the dialog', () => {
		expect(WORK_ITEMS.map((w) => [w.label, w.one, w.list()])).toEqual([
			['Tickets', 'Ticket', '/tickets'],
			['Merge requests', 'Merge request', '/merge-requests']
		]);
		expect(workItem('merge_request').match('/merge-requests/p1/2')).toBe(true);
		expect(workItem('ticket').match('/merge-requests')).toBe(false);
	});

	it('offers a workshop’s live tickets, each to its page there', async () => {
		vi.mocked(api.projects.tickets).mockResolvedValue({
			tickets: [aTicketRow({ key: 'PROJ-1', title: 'Fix login' })],
			next: ''
		});
		expect(await workItem('ticket').options('p1')).toEqual([
			{ value: 'PROJ-1', label: 'Fix login', detail: 'PROJ-1', href: '/w/p1/tickets/PROJ-1' }
		]);
		expect(api.projects.tickets).toHaveBeenCalledWith('p1', { limit: 50 });
		expect(workItem('ticket').perWorkshop).toBe(true);
	});

	it('offers a workshop’s merge requests, each to its page', async () => {
		vi.mocked(api.projects.mergeRequests).mockResolvedValue([
			{ mr: anOpenMr({ iid: 7, title: 'Add keys', author: 'ada' }), runs: 0 }
		]);
		expect(await workItem('merge_request').options('p1')).toEqual([
			{ value: '7', label: '!7 Add keys', detail: 'ada', href: '/merge-requests/p1/7' }
		]);
		expect(api.projects.mergeRequests).toHaveBeenCalledWith('p1');
		expect(workItem('merge_request').perWorkshop).toBe(true);
	});

	it('names how to write a ticket', () => {
		expect(workItem('ticket').create!.label).toBe('New ticket');
		expect(workItem('merge_request').create).toBeUndefined();
	});
});
