import { describe, expect, it } from 'vitest';
import { readDoc } from '$test/fixtures/logic';
import { levelsOf, sequenceOf } from './sequence';

const doc = readDoc(`
name: f
version: 5
parameters: {}
sequences:
  - id: inner
    description: the inner one
    stages:
      - { id: leaf, type: command, run: r }
  - id: outer
    stages:
      - { id: first, sequence: inner, on_fail: { to: tail, max: 1 } }
      - { id: second, sequence: inner, on_fail: fail }
      - { id: tail, type: command, run: t }
      - { id: last, sequence: inner }
main:
  - { id: run_it, sequence: outer, on_success: after, on_fail: { to: after, max: 2 } }
  - { id: after, type: command, run: a, on_success: done }
  - { id: bare, sequence: outer }
`);

describe('sequenceOf', () => {
	it('finds what a sequence stage runs', () => {
		expect(sequenceOf(doc, doc.stages[0])?.id).toBe('outer');
	});

	it('finds nothing for another stage, nothing, or an unknown sequence', () => {
		expect(sequenceOf(doc, doc.stages[1])).toBeUndefined();
		expect(sequenceOf(doc, undefined)).toBeUndefined();
		expect(sequenceOf(null, doc.stages[0])).toBeUndefined();
		expect(sequenceOf(doc, { id: 'x', type: 'sequence', sequence: 'gone' })).toBeUndefined();
	});
});

describe('levelsOf', () => {
	it('opens a sequence on its own, its ways out in general terms', () => {
		const [level, ...rest] = levelsOf(doc, { root: '', sequence: 'inner', path: [] });
		expect(rest).toEqual([]);
		expect(level.stage).toBeUndefined();
		expect(level.sequence.id).toBe('inner');
		expect(level.view).toEqual({
			id: 'inner',
			description: 'the inner one',
			exit: expect.any(String),
			fail: expect.any(String)
		});
	});

	it('opens nothing for a sequence that is gone', () => {
		expect(levelsOf(doc, { root: '', sequence: 'gone', path: [] })).toEqual([]);
	});

	it('opens what a stage of the chains runs, leading where that stage leads', () => {
		const [level] = levelsOf(doc, { root: 'run_it', path: [] });
		expect(level.stage?.id).toBe('run_it');
		expect(level.view).toEqual({
			id: 'outer',
			description: undefined,
			exit: 'after',
			fail: 'after'
		});
	});

	it.each(['nowhere', 'after'])('opens nothing for %s', (root) => {
		expect(levelsOf(doc, { root, path: [] })).toEqual([]);
	});

	it('leaves `fail` unsaid when the stage has no on_fail', () => {
		expect(levelsOf(doc, { root: 'bare', path: [] })[0].view.fail).toBeUndefined();
	});

	it.each<[string, string | undefined, string | undefined]>([
		// Not the last: it goes on to the next stage. Its on_fail is its own.
		['first', 'second', 'tail'],
		// `fail` is the level above's fail.
		['second', 'tail', 'after'],
		// Last: it leaves by the level above's way out.
		['last', 'after', undefined]
	])('opens %s nested, with where it leads', (stage, exit, fail) => {
		const levels = levelsOf(doc, { root: 'run_it', path: [stage] });
		expect(levels.map((l) => l.sequence.id)).toEqual(['outer', 'inner']);
		expect(levels[1].stage?.id).toBe(stage);
		expect(levels[1].view.exit).toBe(exit);
		expect(levels[1].view.fail).toBe(fail);
	});

	it('stops at a path that no longer leads anywhere', () => {
		const levels = levelsOf(doc, { root: 'run_it', path: ['tail', 'first'] });
		expect(levels).toHaveLength(1);
	});
});
