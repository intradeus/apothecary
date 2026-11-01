import { describe, expect, it } from 'vitest';
import type { TrackerStatus } from '$lib/domain';
import {
	moveRow,
	namesProblem,
	NO_STEP,
	removedRows,
	statusListProblem,
	stepOptions
} from './trackerRows';

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

describe('stepOptions', () => {
	const rows = [
		row({ id: 'a', name: 'To Do', flowStep: 'todo' }),
		row({ id: 'b', name: 'Doing', flowStep: 'in_progress' }),
		row({ id: 'c', name: 'Parked' })
	];

	it('offers every step, then none', () => {
		expect(stepOptions(rows, 2).map((o) => o.value)).toEqual([
			'todo',
			'in_progress',
			'in_review',
			'done',
			'blocked',
			NO_STEP
		]);
	});

	it('disables the steps other rows hold, but not its own', () => {
		const opts = stepOptions(rows, 0);
		const disabled = opts.filter((o) => o.disabled).map((o) => o.value);
		expect(disabled).toEqual(['in_progress']);
	});
});

describe('statusListProblem', () => {
	it('refuses a step held twice', () => {
		expect(
			statusListProblem([
				row({ id: 'a', name: 'A', flowStep: 'done' }),
				row({ id: 'b', name: 'B', flowStep: 'done' })
			])
		).toBe('Only one status can be “done”.');
	});

	it('accepts a list with each step at most once', () => {
		expect(
			statusListProblem([
				row({ id: 'a', name: 'A', flowStep: 'todo' }),
				row({ id: 'b', name: 'B' }),
				row({ id: 'c', name: 'C' })
			])
		).toBe('');
	});
});

describe('namesProblem', () => {
	it('refuses an empty name', () => {
		expect(namesProblem([{ name: 'A' }, { name: '  ' }])).toBe('Every row needs a name.');
	});

	it('refuses the same name twice, case aside', () => {
		expect(namesProblem([{ name: 'Done' }, { name: ' done ' }])).toBe('“done” is listed twice.');
	});
});

describe('moveRow', () => {
	const rows = ['a', 'b', 'c'].map((id, position) => row({ id, position }));

	it('moves a row and renumbers the positions', () => {
		const moved = moveRow(rows, 2, -1);
		expect(moved.map((r) => [r.id, r.position])).toEqual([
			['a', 0],
			['c', 1],
			['b', 2]
		]);
	});

	it('stops at the ends', () => {
		expect(moveRow(rows, 0, -1).map((r) => r.id)).toEqual(['a', 'b', 'c']);
		expect(moveRow(rows, 2, 1).map((r) => r.id)).toEqual(['a', 'b', 'c']);
	});
});

describe('removedRows', () => {
	it('lists the saved rows the list dropped, ignoring new ones', () => {
		const saved = [row({ id: 'a' }), row({ id: 'b' })];
		expect(removedRows(saved, [row({ id: 'b' }), row({ id: '' })]).map((r) => r.id)).toEqual(['a']);
	});
});
