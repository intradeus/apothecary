import { describe, expect, it } from 'vitest';
import {
	asChangeType,
	asFlowStep,
	CHANGE_TYPES,
	duplicateSteps,
	isDone,
	statusNames,
	statusOptions,
	trackerIdOf,
	withStatus,
	type TicketStatusRef,
	type TrackerStatus
} from './ticket';

const row = (over: Partial<TrackerStatus>): TrackerStatus => ({
	id: 's1',
	integrationId: 'i1',
	remoteId: '',
	name: 'To Do',
	remoteCategory: '',
	position: 0,
	isDone: false,
	...over
});
const ref = (name: string, isDone = false): { status: TicketStatusRef } => ({
	status: { id: name, name, isDone }
});

describe('asFlowStep', () => {
	it('keeps a known step and drops anything else', () => {
		expect(asFlowStep('in_review')).toBe('in_review');
		expect(asFlowStep('none')).toBeUndefined();
		expect(asFlowStep(undefined)).toBeUndefined();
	});
});

describe('isDone', () => {
	it('reads the status, and counts a ticket with none as not done', () => {
		expect(isDone(ref('Closed', true))).toBe(true);
		expect(isDone(ref('Open'))).toBe(false);
		expect(isDone({})).toBe(false);
	});
});

describe('duplicateSteps', () => {
	it('names each step more than one status holds, in flow order', () => {
		expect(
			duplicateSteps([
				{ flowStep: 'done' },
				{ flowStep: 'todo' },
				{ flowStep: 'done' },
				{ flowStep: 'todo' },
				{ flowStep: 'blocked' },
				{}
			])
		).toEqual(['todo', 'done']);
	});

	it('is empty when each step is held once and the rest have none', () => {
		expect(duplicateSteps([{ flowStep: 'todo' }, {}, {}, { flowStep: 'done' }])).toEqual([]);
	});
});

describe('trackerIdOf', () => {
	const trackers = [
		{ id: 'jira', kind: 'jira' },
		{ id: 'own', kind: 'internal' }
	];

	it("is a sourced ticket's own tracker", () => {
		expect(trackerIdOf({ source: { integrationId: 'jira', kind: 'jira', key: 'P-1' } }, [])).toBe(
			'jira'
		);
	});

	it("is the user's internal tracker for a ticket with no source", () => {
		expect(trackerIdOf({}, trackers)).toBe('own');
		expect(trackerIdOf({}, [trackers[0]])).toBeUndefined();
	});
});

describe('statusOptions', () => {
	it('lists the tracker statuses in tracker order', () => {
		const opts = statusOptions([
			row({ id: 'b', name: 'Done', position: 2, isDone: true, flowStep: 'done' }),
			row({ id: 'a', name: 'To Do', position: 1, flowStep: 'todo' })
		]);
		expect(opts).toEqual([
			{ id: 'a', name: 'To Do', isDone: false, flowStep: 'todo' },
			{ id: 'b', name: 'Done', isDone: true, flowStep: 'done' }
		]);
	});

	it('keeps the current status first when the list no longer has it', () => {
		const opts = statusOptions([row({ id: 'a' })], { id: 'old', name: 'Legacy', isDone: false });
		expect(opts.map((o) => o.id)).toEqual(['old', 'a']);
	});

	it('does not repeat a current status the list has', () => {
		const opts = statusOptions([row({ id: 'a' })], { id: 'a', name: 'To Do', isDone: false });
		expect(opts).toHaveLength(1);
	});
});

describe('status filter', () => {
	const tickets = [ref('Open'), {}, ref('Review'), ref('Open')];

	it('offers each status once, in first-seen order, skipping unsynced tickets', () => {
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
