import { describe, expect, it } from 'vitest';
import { attemptRef, parseRef, type RunRef } from './ref';

// Every form of the grammar. The engine's test holds the same list
// (internal/run/ref/ref_test.go): a change to one is a change to both.
const examples: [string, RunRef][] = [
	['review#5', { stage: 'review', attempt: 5 }],
	['stage-2#3', { stage: 'stage-2', attempt: 3 }],
	['fix_ci#12', { stage: 'fix_ci', attempt: 12 }],
	['settle.resolve#2', { stage: 'settle.resolve', attempt: 2 }],
	['decision#2', { stage: 'decision', attempt: 2 }],
	['run#1', { stage: 'run', attempt: 1 }],
	['build#3/decision#1', { stage: 'build', attempt: 3, kind: 'decision', n: 1 }],
	['build#3/note#2', { stage: 'build', attempt: 3, kind: 'note', n: 2 }],
	['run#1/decision#1', { stage: 'run', attempt: 1, kind: 'decision', n: 1 }],
	['run', {}],
	['run/decision#4', { kind: 'decision', n: 4 }]
];

// What the grammar does not produce: the same list as TestParseRefuses.
const refused = [
	'',
	'review',
	'review#',
	'review#0',
	'review#05',
	'review#-1',
	'review#1#2',
	'review-5',
	'#5',
	'Review#5',
	'2fix#1',
	'review#5/',
	'review#5/note',
	'review#5/note#0',
	'review#5/verdict#1',
	'review#5/note#1/x',
	'run/note#1',
	'run/decision',
	'run#',
	'decision#1/decision#1/x',
	'review#5 ',
	' review#5',
	'a..b#1',
	'.a#1'
];

describe('references', () => {
	it.each(examples)('reads %s', (s, want) => {
		expect(parseRef(s)).toEqual(want);
	});

	it.each(refused)('refuses %j', (s) => {
		expect(parseRef(s)).toBeNull();
	});

	it('formats an attempt the way it is read back', () => {
		expect(attemptRef('stage-2', 3)).toBe('stage-2#3');
		expect(attemptRef('settle.resolve', 2)).toBe('settle.resolve#2');
		expect(parseRef(attemptRef('review', 5))).toEqual({ stage: 'review', attempt: 5 });
	});
});
