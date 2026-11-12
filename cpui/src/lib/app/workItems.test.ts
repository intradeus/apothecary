import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { OPERATES_ON } from '$lib/domain/flowdoc';
import { aProject, aTicket } from '$test/fixtures';
import { anOpenMr } from '$test/fixtures/run-components';
import { WORK_ITEMS, workItem } from './workItems';

vi.mock('$lib/api', () => ({
	api: { tickets: { list: vi.fn() }, projects: { mergeRequests: vi.fn() } }
}));

describe('WORK_ITEMS', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		fleet.projects = [aProject({ id: 'p1', name: 'ledger' })];
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

	it('offers tickets newest first, across workshops or within one', async () => {
		vi.mocked(api.tickets.list).mockResolvedValue([
			aTicket({ id: 'old', projectId: 'p1', title: 'Old', createdAt: '2026-10-01T00:00:00Z' }),
			aTicket({ id: 'new', projectId: 'p2', title: 'New', createdAt: '2026-10-20T00:00:00Z' })
		]);
		expect(await workItem('ticket').options('')).toEqual([
			{ value: 'new', label: 'New', detail: 'p2', href: '/tickets/new' },
			{ value: 'old', label: 'Old', detail: 'ledger', href: '/tickets/old' }
		]);
		expect(await workItem('ticket').options('p1')).toEqual([
			{ value: 'old', label: 'Old', detail: undefined, href: '/tickets/old' }
		]);
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

	it('says where to write a ticket, the workshop carried', () => {
		const create = workItem('ticket').create!;
		expect(create.label).toBe('New ticket');
		expect(create.href('p1')).toBe('/tickets/new?project=p1');
		expect(create.href('')).toBe('/tickets/new');
		expect(workItem('merge_request').create).toBeUndefined();
	});
});
