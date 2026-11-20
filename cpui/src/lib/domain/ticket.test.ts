import { describe, expect, it } from 'vitest';
import {
	asChangeType,
	CHANGE_TYPES,
	statusNames,
	statusOptions,
	ticketChanges,
	withStatus,
	type TicketVersion,
	type TrackerStatus
} from './ticket';

const row = (over: Partial<TrackerStatus>): TrackerStatus => ({
	id: 's1',
	integrationId: 'i1',
	remoteId: '',
	name: 'To Do',
	position: 0,
	isDone: false,
	...over
});

describe('statusOptions', () => {
	it('lists the tracker statuses in tracker order, by name', () => {
		const opts = statusOptions([
			row({ id: 'b', name: 'Done', position: 2, isDone: true }),
			row({ id: 'a', name: 'To Do', position: 1 })
		]);
		expect(opts).toEqual([
			{ name: 'To Do', isDone: false },
			{ name: 'Done', isDone: true }
		]);
	});

	it('keeps the current status first when the list no longer has it', () => {
		const opts = statusOptions([row({})], { status: 'Legacy', done: true });
		expect(opts).toEqual([
			{ name: 'Legacy', isDone: true },
			{ name: 'To Do', isDone: false }
		]);
	});

	it('does not repeat a current status the list has, nor add a missing one', () => {
		expect(statusOptions([row({})], { status: 'To Do', done: false })).toHaveLength(1);
		expect(statusOptions([row({})], { status: '', done: false })).toHaveLength(1);
	});
});

describe('status filter', () => {
	const tickets = [{ status: 'Open' }, { status: '' }, { status: 'Review' }, { status: 'Open' }];

	it('offers each status once, in first-seen order, skipping tickets with none', () => {
		expect(statusNames(tickets)).toEqual(['Open', 'Review']);
	});

	it('narrows to one status name, or keeps all for none', () => {
		expect(withStatus(tickets, 'Open')).toHaveLength(2);
		expect(withStatus(tickets, '')).toBe(tickets);
	});
});

describe('asChangeType', () => {
	it.each(CHANGE_TYPES)('keeps %s', (type) => {
		expect(asChangeType(type)).toBe(type);
	});

	it.each([[undefined], [''], ['bug'], ['FEAT']])('reads %j as a feature', (value) => {
		expect(asChangeType(value)).toBe('feat');
	});

	it('knows the types a branch may be named after', () => {
		expect(CHANGE_TYPES).toEqual(['feat', 'fix', 'hotfix', 'perf', 'docs', 'chore']);
	});
});

describe('ticketChanges', () => {
	const v: TicketVersion = {
		seq: 2,
		title: 'Old',
		goal: 'a\nb',
		baseBranch: 'feat/x',
		changeType: 'fix',
		typeId: 'y1',
		labels: ['b', 'a'],
		author: { kind: 'user', id: 'u' },
		createdAt: 't',
		changed: [],
		oldest: false
	};
	const now = {
		title: 'Old',
		body: 'a\nb',
		baseBranch: 'feat/x',
		changeType: 'fix' as const,
		typeId: 'y1',
		type: 'Bug',
		labels: ['a', 'b']
	};

	it('finds nothing when the version is the ticket now (labels as a set)', () => {
		expect(ticketChanges(v, now)).toEqual([]);
	});

	it('names each differing field, current value before, version after', () => {
		const ch = ticketChanges(
			v,
			{
				title: 'New',
				body: 'a',
				baseBranch: 'main',
				changeType: 'feat',
				typeId: 'y2',
				type: 'Story',
				labels: []
			},
			(id) => (id === 'y1' ? 'Bug' : id)
		);
		expect(ch).toEqual([
			{ field: 'title', before: 'New', after: 'Old' },
			{ field: 'goal', before: 'a', after: 'a\nb' },
			{ field: 'change_type', before: 'feat', after: 'fix' },
			{ field: 'type', before: 'Story', after: 'Bug' },
			{ field: 'labels', before: '', after: 'a, b' },
			{ field: 'base_branch', before: 'main', after: 'feat/x' }
		]);
	});

	it('reads a type by its id without a name, and an untyped version as empty', () => {
		expect(ticketChanges({ ...v, typeId: '' }, { ...now, type: '' })).toEqual([
			{ field: 'type', before: 'y1', after: '' }
		]);
		expect(ticketChanges({ ...v, typeId: 'y9' }, now)).toEqual([
			{ field: 'type', before: 'Bug', after: 'y9' }
		]);
	});
});
