import { describe, expect, it } from 'vitest';
import type { DocPhase, FlowDoc } from '$lib/domain/flowdoc';
import { aFlow, aStage, readDoc } from '$test/fixtures/logic';
import { chainHue, sceneOf, type SceneInput } from './scene';

const all = readDoc(`
name: f
version: 5
parameters: {}
main: [{ id: a, type: command, run: a }]
watchers: [{ id: w, on: manual, stages: [{ id: x, type: command, run: x }] }]
outcomes: [{ id: o, on: mr_merged }]
`);
const watchOnly = readDoc(`
name: f
version: 5
parameters: {}
watchers: [{ id: w, on: manual, stages: [{ id: x, type: command, run: x }] }]
`);

const scene = (doc: FlowDoc, over: Partial<SceneInput> = {}) =>
	sceneOf({ doc, layout: {}, agents: [], actions: [], collapsed: new Set(), ...over });

describe('chainHue', () => {
	it.each<[Partial<DocPhase>, string]>([
		[{ kind: 'watcher' }, 'accent'],
		[{ kind: 'main' }, 'accent'],
		[{ kind: 'outcome' }, 'success'],
		[{ kind: 'outcome', then: 'done' }, 'success'],
		[{ kind: 'outcome', then: 'abort' }, 'danger']
	])('colours %j %s', (ph, hue) => {
		expect(chainHue(ph as DocPhase)).toBe(hue);
	});
});

describe('sceneOf', () => {
	it('sees a hub once something waits', () => {
		expect(scene(all).hub).toBe(true);
		expect(scene(aFlow([aStage({ id: 'a' })])).hub).toBe(false);
	});

	it('draws no hub for a sequence on its own', () => {
		expect(scene(all, { sequence: { id: 'q', exit: 'x' } }).hub).toBe(false);
	});

	it('separates the main chain from what branches', () => {
		const sc = scene(all);
		expect(sc.mainPhase?.id).toBe('main');
		expect(sc.triggers.map((p) => p.id)).toEqual(['w', 'o']);
		expect(sc.watchesOnly).toBe(false);
	});

	it('knows a flow that only watches', () => {
		const sc = scene(watchOnly);
		expect(sc.watchesOnly).toBe(true);
		expect(sc.mainPhase).toBeUndefined();
	});

	it.each<[string | undefined, string | undefined]>([
		['w', 'w'],
		['o', 'o'],
		['main', undefined],
		['nope', undefined],
		[undefined, undefined]
	])('finds the branch of phase %s', (phase, id) => {
		expect(scene(all).branch(phase)?.id).toBe(id);
	});

	it('finds no branch inside a sequence', () => {
		expect(scene(all, { sequence: { id: 'q', exit: 'x' } }).branch('w')).toBeUndefined();
	});

	it.each<[string | undefined, string, string]>([
		['w', 'end', '__end:w'],
		['o', 'abort', '__abort:o'],
		['main', 'end', '__hub'],
		['main', 'done', '__done'],
		[undefined, 'abort', '__abort']
	])('lands %s’s route to %s on %s', (phase, to, node) => {
		expect(scene(all).termNode(phase, to)).toBe(node);
	});

	it('lands the end of a flow without a hub on its own terminal', () => {
		expect(scene(aFlow([aStage({ id: 'a' })])).termNode('main', 'end')).toBe('__end');
	});

	it('bands a node with its chain, main by default', () => {
		const sc = scene(all);
		expect(sc.bandOf('w')).toBe('w');
		expect(sc.bandOf('')).toBe('main');
		expect(sc.bandOf(undefined)).toBe('main');
	});

	it('hides only a folded branch', () => {
		const sc = scene(all, { collapsed: new Set(['w', 'main']) });
		expect(sc.hidden('w')).toBe(true);
		expect(sc.hidden('o')).toBe(false);
		expect(sc.hidden('main')).toBe(false);
		expect(sc.hidden(undefined)).toBe(false);
	});
});
