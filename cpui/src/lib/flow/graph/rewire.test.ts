import { describe, expect, it } from 'vitest';
import { ABORT, DEBUG, DONE, END, START, type FlowDoc } from '$lib/domain/flowdoc';
import { readDoc } from '$test/fixtures/logic';
import { rewire } from './rewire';

const flow = (): FlowDoc =>
	readDoc(`
name: f
version: 5
parameters: {}
main:
  - { id: a, type: command, run: a, on_fail: { to: b, max: 2, then: { to: c, max: 1 } } }
  - { id: b, type: command, run: b }
  - { id: c, type: command, run: c }
watchers:
  - { id: w, on: manual, stages: [{ id: x, type: command, run: x }] }
`);
const stage = (doc: FlowDoc, id: string) => doc.stages.find((s) => s.id === id)!;

describe('the entry marker', () => {
	it('makes the stage it is dropped on the entry', () => {
		const doc = flow();
		expect(rewire(doc, START, 'entry', 'b')).toBe('changed');
		expect(doc.stages[0].id).toBe('b');
	});

	it.each([DONE, ABORT, DEBUG, 'a', 'nowhere'])('changes nothing dropped on %s', (target) => {
		const doc = flow();
		expect(rewire(doc, START, 'entry', target)).toBe('same');
		expect(doc.stages.map((s) => s.id)).toEqual(['a', 'b', 'c', 'x']);
	});
});

describe('a success route', () => {
	it.each(['c', END, DONE])('goes where it is dropped: %s', (target) => {
		const doc = flow();
		expect(rewire(doc, 'a', 'success', target)).toBe('changed');
		expect(stage(doc, 'a').on_success).toBe(target);
	});

	it.each(['a', ABORT, DEBUG])('never goes to %s', (target) => {
		const doc = flow();
		rewire(doc, 'a', 'success', target);
		expect(stage(doc, 'a').on_success).toBe('b');
	});
});

describe('a failure route', () => {
	it('keeps its chain when it is pointed elsewhere', () => {
		const doc = flow();
		expect(rewire(doc, 'a', 'on_fail', 'c')).toBe('changed');
		expect(stage(doc, 'a').on_fail).toEqual({ to: 'c', max: 2, then: { to: 'c', max: 1 } });
	});

	it('starts with three tries when there was no chain', () => {
		const doc = flow();
		rewire(doc, 'b', 'on_timeout', 'a');
		expect(stage(doc, 'b').on_timeout).toEqual({ to: 'a', max: 3 });
	});

	it('reaches a stage’s end, but not done', () => {
		const doc = flow();
		expect(rewire(doc, 'b', 'on_fail', DONE)).toBe('same');
		expect(stage(doc, 'b').on_fail).toBeUndefined();
	});

	it('replaces the whole chain with the stop', () => {
		const doc = flow();
		expect(rewire(doc, 'a', 'on_fail', ABORT)).toBe('changed');
		expect(stage(doc, 'a').on_fail).toEqual({ to: ABORT });
	});

	it('appends the debug leaf after a finite chain', () => {
		const doc = flow();
		rewire(doc, 'a', 'on_fail', DEBUG);
		expect(stage(doc, 'a').on_fail).toEqual({
			to: 'b',
			max: 2,
			then: { to: 'c', max: 1, then: { to: DEBUG } }
		});
	});

	it.each([
		['no chain', undefined],
		['an unlimited chain', { to: 'b', max: -1 }],
		['a chain ending in abort', { to: ABORT }],
		['a chain already parked', { to: DEBUG }]
	])('makes %s the bare leaf', (_, existing) => {
		const doc = flow();
		stage(doc, 'b').on_fail = existing;
		rewire(doc, 'b', 'on_fail', DEBUG);
		expect(stage(doc, 'b').on_fail).toEqual({ to: DEBUG });
	});
});

describe('what a drop cannot do', () => {
	it('changes nothing from an unknown stage', () => {
		expect(rewire(flow(), 'nope', 'success', 'a')).toBe('same');
	});

	it.each(['success', 'on_fail'])('explains a drop across chains: %s', (handle) => {
		const doc = flow();
		expect(rewire(doc, 'a', handle, 'x')).toEqual({ from: 'a', to: 'x' });
		expect(stage(doc, 'a').on_success).toBe('b');
	});
});
