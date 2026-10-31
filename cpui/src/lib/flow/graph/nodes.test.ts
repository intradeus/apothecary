import type { Node } from '@xyflow/svelte';
import { describe, expect, it } from 'vitest';
import { ABORT, DONE, END, type FlowDoc } from '$lib/domain/flowdoc';
import { aFlow, aStage, readDoc } from '$test/fixtures/logic';
import { arrivalsOf, chromeNodes, ghostOf, stageNodes, widthIn, type Arrivals } from './nodes';
import { sceneOf, type Layout, type SceneInput } from './scene';

const NONE: Arrivals = { added: [], removed: [], renamed: null };
const noWidth = widthIn([]);

const allChains = (): FlowDoc =>
	readDoc(`
name: f
version: 5
parameters: {}
main:
  - { id: a, type: command, run: a, on_fail: { to: a, max: 1, then: debug } }
  - { id: b, type: command, run: b, on_success: done }
watchers:
  - id: w
    on: { signal: mr_conflicts, start: confirm }
    stages:
      - { id: x, type: command, run: x, on_fail: abort }
  - { id: hand, on: manual, stages: [{ id: y, type: command, run: y }] }
outcomes:
  - { id: merged, on: mr_merged, stages: [{ id: z, type: command, run: z }] }
  - { id: closed, on: mr_closed, then: abort }
`);

const scene = (doc: FlowDoc, over: Partial<SceneInput> = {}) =>
	sceneOf({ doc, layout: {}, agents: [], actions: [], collapsed: new Set(), ...over });

function chrome(doc: FlowDoc, over: Partial<SceneInput> = {}) {
	const sc = scene(doc, over);
	const { nodes, placed } = stageNodes(sc, NONE, noWidth);
	return { stages: nodes, chrome: chromeNodes(sc, placed, noWidth) };
}
const byId = (nodes: Node[], id: string) => nodes.find((n) => n.id === id)!;
const ids = (nodes: Node[]) => nodes.map((n) => n.id);

const stageNode = (id: string, over: Partial<Node> = {}): Node => ({
	id,
	type: 'stage',
	position: { x: 1, y: 2 },
	data: {},
	...over
});

describe('arrivalsOf', () => {
	const doc = aFlow([aStage({ id: 'a' }), aStage({ id: 'b' })]);
	const ids = new Set(['a', 'b']);

	it('sees nothing on the first build', () => {
		expect(arrivalsOf(doc, ids, [stageNode('old')], null)).toEqual(NONE);
	});

	it('tells what arrived from what left', () => {
		const gone = stageNode('old');
		const two = stageNode('older');
		const out = arrivalsOf(doc, ids, [stageNode('a'), gone, two], new Set(['a', 'old', 'older']));
		expect(out).toEqual({ added: ['b'], removed: [gone, two], renamed: null });
	});

	it('reads one out and one in as a rename', () => {
		const gone = stageNode('old');
		const out = arrivalsOf(doc, ids, [stageNode('a'), gone], new Set(['a', 'old']));
		expect(out.renamed).toEqual({ from: gone, to: 'b' });
	});

	it('ignores what is not a stage, and ghosts already leaving', () => {
		const prev = [
			stageNode('__end', { type: 'end' }),
			stageNode('ghost', { data: { leaving: true } })
		];
		expect(arrivalsOf(doc, ids, prev, new Set(['a', 'b'])).removed).toEqual([]);
	});
});

describe('ghostOf', () => {
	it('turns a stage into what cannot be touched, leaving where it stood', () => {
		const ghost = ghostOf(stageNode('a', { selected: true, data: { fresh: true, band: 'w' } }));
		expect(ghost).toMatchObject({
			id: 'a',
			position: { x: 1, y: 2 },
			selected: false,
			selectable: false,
			draggable: false,
			connectable: false,
			data: { band: 'w', leaving: true, fresh: false }
		});
	});
});

describe('widthIn', () => {
	it('reads what was measured, else the guess', () => {
		const width = widthIn([stageNode('a', { measured: { width: 300, height: 10 } })]);
		expect(width('a')).toBe(300);
		expect(width('b')).toBe(220);
		expect(width('b', 50)).toBe(50);
	});
});

describe('stageNodes', () => {
	const placedAt = (doc: FlowDoc, layout: Layout, arrivals = NONE, width = noWidth) =>
		stageNodes(scene(doc, { layout }), arrivals, width).nodes;

	it('draws each stage with what its card needs', () => {
		const doc = allChains();
		const agents = [{ name: 'a' }];
		const { nodes } = stageNodes(scene(doc, { agents }), NONE, noWidth);
		expect(ids(nodes)).toEqual(['a', 'b', 'x', 'y', 'z']);
		expect(nodes[0]).toMatchObject({
			type: 'stage',
			hidden: false,
			data: { entry: true, agents, band: 'main', labels: 2, fresh: false }
		});
		expect(nodes[1].data.entry).toBe(false);
		expect(nodes[2].data).toMatchObject({ entry: true, band: 'w', labels: 1 });
	});

	it('keeps a place the layout has', () => {
		const nodes = placedAt(aFlow([aStage({ id: 'a' })]), { a: { x: 7, y: 8 } });
		expect(nodes[0].position).toEqual({ x: 7, y: 8 });
	});

	it('puts a stage nobody placed after the one before it', () => {
		const doc = aFlow([aStage({ id: 'a' }), aStage({ id: 'b' })]);
		const nodes = placedAt(
			doc,
			{ a: { x: 100, y: 50 } },
			NONE,
			widthIn([stageNode('a', { measured: { width: 300, height: 1 } })])
		);
		expect(nodes[1].position).toEqual({ x: 440, y: 50 });
	});

	it('puts a new chain’s first stage beside its marker', () => {
		const doc = allChains();
		const nodes = placedAt(doc, { 'trigger:w': { x: -300, y: 400 } });
		expect(byId(nodes, 'x').position).toEqual({ x: -80, y: 400 });
	});

	it('falls back on a row with no marker to go by', () => {
		const doc = aFlow([aStage({ id: 'a' })]);
		expect(placedAt(doc, {})[0].position).toEqual({ x: 0, y: 0 });
	});

	it('keeps a renamed stage’s place', () => {
		const doc = aFlow([aStage({ id: 'b' })]);
		const from = stageNode('a', { position: { x: 40, y: 50 } });
		const nodes = placedAt(doc, {}, { added: ['b'], removed: [from], renamed: { from, to: 'b' } });
		expect(nodes[0].position).toEqual({ x: 40, y: 50 });
		expect(nodes[0].data.fresh).toBe(false);
	});

	it('marks a stage that just arrived', () => {
		const doc = aFlow([aStage({ id: 'b' })]);
		const nodes = placedAt(doc, {}, { added: ['b'], removed: [], renamed: null });
		expect(nodes[0].data.fresh).toBe(true);
	});

	it('hides the stages of a folded chain', () => {
		const { nodes } = stageNodes(scene(allChains(), { collapsed: new Set(['w']) }), NONE, noWidth);
		expect(byId(nodes, 'x').hidden).toBe(true);
		expect(byId(nodes, 'a').hidden).toBe(false);
	});

	it('reports where each one ended up', () => {
		const doc = aFlow([aStage({ id: 'a' })]);
		const { placed } = stageNodes(scene(doc, { layout: { a: { x: 3, y: 4 } } }), NONE, noWidth);
		expect(placed.a).toEqual({ x: 3, y: 4 });
	});
});

describe('the markers', () => {
	it('names what opens a watcher', () => {
		const n = byId(chrome(allChains()).chrome, '__trigger:w');
		expect(n.type).toBe('start');
		expect(n.data).toMatchObject({
			entry: false,
			outcome: false,
			hub: true,
			band: 'w',
			label: 'w: on mr_conflicts (confirm)'
		});
	});

	it('names how an outcome ends the run', () => {
		const n = byId(chrome(allChains()).chrome, '__trigger:closed');
		expect(n.data).toMatchObject({
			outcome: true,
			then: ABORT,
			label: 'closed: on mr_closed → abort'
		});
	});

	it('titles a manual watcher, an outcome and an aborting one differently', () => {
		const nodes = chrome(allChains()).chrome;
		const titles = ['hand', 'merged', 'closed', 'w'].map(
			(id) => byId(nodes, `__trigger:${id}`).data.title
		);
		expect(new Set(titles).size).toBe(4);
		for (const title of titles) expect(title).not.toMatch(/^flow\.graph/);
	});

	it('says how much a folded chain holds', () => {
		const n = byId(chrome(allChains(), { collapsed: new Set(['w']) }).chrome, '__trigger:w');
		expect(n.data.label).toBe('w: on mr_conflicts (confirm) · 1 stage');
	});

	it('stands a marker where the layout says, else left of its first stage', () => {
		const placed = chrome(allChains(), { layout: { 'trigger:w': { x: 1, y: 2 } } }).chrome;
		expect(byId(placed, '__trigger:w').position).toEqual({ x: 1, y: 2 });
		const guessed = chrome(allChains(), { layout: { x: { x: 500, y: 300 } } }).chrome;
		expect(byId(guessed, '__trigger:w').position).toEqual({ x: 280, y: 300 });
	});

	it('draws the main chain’s start, its label saying what runs operate on', () => {
		const n = byId(chrome(allChains()).chrome, '__start');
		expect(n.connectable).toBe(true);
		expect(n.data).toMatchObject({
			entry: true,
			band: 'main',
			label: 'main · operates on a ticket'
		});
	});

	it('says a merge request when that is what a flow operates on', () => {
		const doc = allChains();
		doc.operates_on = 'merge_request';
		expect(byId(chrome(doc).chrome, '__start').data.label).toBe(
			'main · operates on a merge request'
		);
	});

	it('starts a flow that only watches at the hub, not connectable', () => {
		const doc = readDoc(`
name: f
version: 5
parameters: {}
operates_on: merge_request
watchers: [{ id: w, on: manual, stages: [{ id: x, type: command, run: x }] }]
`);
		const n = byId(chrome(doc).chrome, '__start');
		expect(n.connectable).toBe(false);
		expect(n.data.label).toBe('run · operates on a merge request');
	});

	it('marks a sequence drawn on its own', () => {
		const doc: FlowDoc = { name: 'f', parameters: {}, stages: [aStage({ id: 'a' })] };
		const n = byId(
			chrome(doc, { sequence: { id: 'ship', description: 'ships it', exit: 'x' } }).chrome,
			'__start'
		);
		expect(n.data).toMatchObject({ label: 'sequence ship', title: 'ships it' });
	});

	it('starts a plain flat flow plainly', () => {
		const doc: FlowDoc = { name: 'f', parameters: {}, stages: [aStage({ id: 'a' })] };
		expect(byId(chrome(doc).chrome, '__start').data.label).toBe('start');
	});
});

describe('sections and the hub', () => {
	it('labels each section there is, none draggable', () => {
		const nodes = chrome(allChains()).chrome.filter((n) => n.type === 'section');
		expect(ids(nodes)).toEqual(['__section:main', '__section:watcher', '__section:outcome']);
		expect(nodes[1].data.label).toBe('watchers');
		expect(nodes[1].draggable).toBe(false);
	});

	it('labels nothing with one chain', () => {
		expect(chrome(aFlow([aStage({ id: 'a' })])).chrome.some((n) => n.type === 'section')).toBe(
			false
		);
	});

	it('draws the hub only when something waits', () => {
		const hub = byId(chrome(allChains()).chrome, '__hub');
		expect(hub.data.label).toBe('waiting');
		expect(ids(chrome(aFlow([aStage({ id: 'a' })])).chrome)).not.toContain('__hub');
	});

	it('stands the hub where the layout says, else left of the first branch', () => {
		const placed = chrome(allChains(), { layout: { hub: { x: 5, y: 6 } } }).chrome;
		expect(byId(placed, '__hub').position).toEqual({ x: 5, y: 6 });
		const near = chrome(allChains(), { layout: { 'trigger:w': { x: 0, y: 100 } } }).chrome;
		expect(byId(near, '__hub').position).toEqual({ x: -200, y: 4 });
	});
});

describe('the ends', () => {
	it('draws end for a flow without a hub, which stands for it otherwise', () => {
		expect(ids(chrome(aFlow([aStage({ id: 'a' })])).chrome)).toContain('__end');
		expect(ids(chrome(allChains()).chrome)).not.toContain('__end');
	});

	it.each<[string, Parameters<typeof aStage>[0], string, boolean]>([
		['a success', { id: 'a', on_success: DONE }, '__done', true],
		['nothing', { id: 'a', on_success: END }, '__done', false],
		['a failure', { id: 'a', on_fail: { to: ABORT } }, '__abort', true],
		['a fallback', { id: 'a', on_fail: { to: 'a', max: 1, then: { to: ABORT } } }, '__abort', true],
		['nothing', { id: 'a', on_success: END }, '__abort', false]
	])('draws a shared end when %s reaches it', (_, stage, id, drawn) => {
		expect(ids(chrome(aFlow([aStage(stage)])).chrome).includes(id)).toBe(drawn);
	});

	it('takes no account of what branches: their ends are their own', () => {
		const doc = readDoc(`
name: f
version: 5
parameters: {}
main: [{ id: a, type: command, run: a, on_success: end }]
watchers: [{ id: w, on: manual, stages: [{ id: x, type: command, run: x, on_success: done }] }]
`);
		const nodes = ids(chrome(doc).chrome);
		expect(nodes).not.toContain('__done');
		expect(nodes).toContain('__done:w');
	});

	it('closes a branch’s row right of its last stage', () => {
		const doc = allChains();
		const nodes = chrome(doc, { layout: { y: { x: 100, y: 40 } } }).chrome;
		expect(byId(nodes, '__end:hand').position).toEqual({ x: 368, y: 40 });
		expect(byId(nodes, '__end:hand').data.band).toBe('hand');
	});

	it('stacks a branch’s ends', () => {
		const nodes = chrome(allChains(), { layout: { x: { x: 0, y: 0 } } }).chrome;
		expect(byId(nodes, '__end:w').position.y).toBe(0);
		expect(byId(nodes, '__abort:w').position.y).toBe(58);
	});

	it('closes a chainless outcome’s row right of its marker', () => {
		const nodes = chrome(allChains(), { layout: { 'trigger:closed': { x: 10, y: 20 } } }).chrome;
		expect(byId(nodes, '__abort:closed').position).toEqual({ x: 258, y: 20 });
	});

	it('hides a folded branch’s ends', () => {
		const nodes = chrome(allChains(), { collapsed: new Set(['w']) }).chrome;
		expect(byId(nodes, '__end:w').hidden).toBe(true);
		expect(byId(nodes, '__end:hand').hidden).toBe(false);
	});

	it('titles a branch’s ends from the catalogue', () => {
		const nodes = chrome(allChains()).chrome;
		expect(byId(nodes, '__abort:w').data.title).toBe('The run stops deliberately');
		expect(byId(nodes, '__done:merged').data.title).toBe('The run is done');
	});
});

describe('debug leaves', () => {
	it('draws one for each stage that escalates, under it', () => {
		const nodes = chrome(allChains(), { layout: { a: { x: 10, y: 20 } } }).chrome.filter(
			(n) => n.type === 'debug'
		);
		expect(ids(nodes)).toEqual(['__debug:a']);
		expect(nodes[0].position).toEqual({ x: 34, y: 190 });
		expect(nodes[0].data).toMatchObject({ stage: 'a', band: 'main' });
	});

	it('keeps its own place when it has one', () => {
		const nodes = chrome(allChains(), { layout: { 'debug:a': { x: 1, y: 2 } } }).chrome;
		expect(byId(nodes, '__debug:a').position).toEqual({ x: 1, y: 2 });
	});
});

describe('a sequence’s ways out', () => {
	const seqDoc = (over: object = {}): FlowDoc => ({
		name: 'f',
		parameters: {},
		stages: [aStage({ id: 'a', ...over }), aStage({ id: 'b' })]
	});
	const view = { sequence: { id: 'q', exit: 'after', fail: 'repair' } };

	it('always has its success', () => {
		const n = byId(chrome(seqDoc(), view).chrome, '__exit');
		expect(n.type).toBe('exit');
		expect(n.data.label).toBe('back · after');
	});

	it('has `fail` only when a stage routes there', () => {
		expect(ids(chrome(seqDoc(), view).chrome)).not.toContain('__fail');
		const nodes = chrome(seqDoc({ on_fail: { to: 'fail' } }), view).chrome;
		expect(byId(nodes, '__fail').data.label).toBe('fail · repair');
	});

	it('words `fail` plainly when the sequence has no stage to name', () => {
		const nodes = chrome(seqDoc({ on_fail: { to: 'fail' } }), {
			sequence: { id: 'q', exit: 'after' }
		}).chrome;
		expect(byId(nodes, '__fail').data.label).toBe('fail · fail');
	});

	it('uses the column’s spots, its own ends under them', () => {
		const layout = { end: { x: 0, y: 500 }, done: { x: 100, y: 500 } };
		const doc = seqDoc({ on_success: END, on_fail: { to: 'a', max: 1, then: { to: ABORT } } });
		doc.stages[1].on_success = DONE;
		const nodes = chrome(doc, { ...view, layout }).chrome;
		expect(byId(nodes, '__exit').position).toEqual({ x: 0, y: 500 });
		expect(byId(nodes, '__end').position).toEqual({ x: 0, y: 570 });
		expect(byId(nodes, '__done').position).toEqual({ x: 100, y: 570 });
		expect(byId(nodes, '__abort').position).toEqual({ x: 0, y: 640 });
	});

	it('draws end only when a stage reaches it', () => {
		expect(ids(chrome(seqDoc(), view).chrome)).not.toContain('__end');
	});

	it('draws no sections or hub', () => {
		const nodes = chrome(allChains(), view).chrome;
		expect(nodes.some((n) => n.type === 'section' || n.type === 'hub')).toBe(false);
	});
});
