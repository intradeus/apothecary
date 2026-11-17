import { describe, expect, it } from 'vitest';
import type { RunState } from '$lib/domain';
import {
	ANY_FINISHED,
	finishedParams,
	matchesFinished,
	readFinishedFilter,
	type ArchivedFilter,
	type OutcomeFilter
} from './filter';

const AT = '2026-10-31T12:00:00Z';
const run = (state: RunState, archived = false) => ({
	state,
	archivedAt: archived ? AT : undefined
});

describe('matchesFinished', () => {
	it('lets every finished run through by default, archived ones too', () => {
		for (const r of [run('completed'), run('failed', true), run('abandoned', true)])
			expect(matchesFinished(r, ANY_FINISHED)).toBe(true);
	});

	it.each<[OutcomeFilter, ArchivedFilter, RunState, boolean, boolean]>([
		['failed', 'yes', 'failed', true, true],
		['failed', 'no', 'failed', true, false],
		['failed', 'any', 'failed', true, true],
		['failed', 'no', 'failed', false, true],
		['failed', 'yes', 'failed', false, false],
		['completed', 'any', 'failed', true, false],
		['completed', 'yes', 'failed', true, false],
		['abandoned', 'no', 'abandoned', false, true],
		['any', 'yes', 'completed', true, true],
		['any', 'yes', 'abandoned', false, false],
		['any', 'no', 'completed', true, false]
	])('outcome %s, archived %s: a %s run (archived %s) passes: %s', (o, a, state, arch, want) => {
		expect(matchesFinished(run(state, arch), { outcome: o, archived: a })).toBe(want);
	});
});

describe('the filter in the URL', () => {
	it('reads the params, anything unknown or absent as any', () => {
		expect(readFinishedFilter(new URLSearchParams('outcome=failed&archived=yes'))).toEqual({
			outcome: 'failed',
			archived: 'yes'
		});
		expect(readFinishedFilter(new URLSearchParams('outcome=archived&archived=maybe'))).toEqual(
			ANY_FINISHED
		);
		expect(readFinishedFilter(new URLSearchParams())).toEqual(ANY_FINISHED);
	});

	it('writes any as no param', () => {
		expect(finishedParams(ANY_FINISHED)).toEqual({ outcome: undefined, archived: undefined });
		expect(finishedParams({ outcome: 'abandoned', archived: 'no' })).toEqual({
			outcome: 'abandoned',
			archived: 'no'
		});
	});
});
