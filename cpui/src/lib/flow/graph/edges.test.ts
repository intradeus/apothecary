import { describe, expect, it, vi } from 'vitest';
import type { Edge } from '@xyflow/svelte';
import { ABORT, DEBUG, DONE, END, FAIL, type FlowDoc } from '$lib/domain/flowdoc';
import { aFlow, aStage, readDoc } from '$test/fixtures/logic';
import { buildEdges } from './edges';
import { sceneOf, type SceneInput } from './scene';

const pickNothing = () => {};

const edges = (
	doc: FlowDoc,
	over: Partial<SceneInput> = {},
	pick: (s: unknown) => void = pickNothing
) =>
	buildEdges(
		sceneOf({ doc, layout: {}, agents: [], actions: [], collapsed: new Set(), ...over }),
		pick
	);
const byId = (list: Edge[], id: string) => list.find((e) => e.id === id)!;
const has = (list: Edge[], id: string) => list.some((e) => e.id === id);

const chains = (): FlowDoc =>
	readDoc(`
name: f
version: 5
parameters: {}
main:
  - { id: a, type: command, run: a }
  - { id: b, type: command, run: b, on_success: done, on_fail: abort }
watchers:
  - id: w
    on: manual
    stages:
      - { id: x, type: command, run: x, on_fail: { to: x, max: 2 } }
outcomes:
  - { id: bye, on: mr_closed, then: abort }
`);

describe('success edges', () => {
	it('runs one from each stage to where it goes', () => {
		const e = byId(edges(aFlow([aStage({ id: 'a' }), aStage({ id: 'b' })])), 'success|a');
		expect(e).toMatchObject({
			source: 'a',
			sourceHandle: 'success',
			target: 'b',
			class: 'e-success'
		});
		expect(e.style).toContain('--eh: var(--success)');
	});

	it.each<[string, string, string]>([
		['a', 'b', 'the next stage'],
		['b', '__done', 'the shared done']
	])('sends %s to %s, %s', (id, target) => {
		expect(byId(edges(chains()), `success|${id}`).target).toBe(target);
	});

	it('sends the end of a branch to its own terminal', () => {
		expect(byId(edges(chains()), 'success|x').target).toBe('__end:w');
	});

	it('sends the main chain’s end to the hub, once there is one', () => {
		const doc = chains();
		doc.stages[1].on_success = END;
		expect(byId(edges(doc), 'success|b').target).toBe('__hub');
	});

	it('lands a verdict’s success on the dot that reads it', () => {
		const doc = aFlow([
			aStage({ id: 'gate', type: 'human_gate' }),
			aStage({ id: 'next', input: { said: { from: 'edge.verdict' } } })
		]);
		expect(byId(edges(doc), 'success|gate').targetHandle).toBe('in:said');
	});

	it('lands a plain success on no dot, even when the target reads a verdict', () => {
		const doc = aFlow([
			aStage({ id: 'cmd' }),
			aStage({ id: 'next', input: { said: { from: 'edge.verdict' } } })
		]);
		expect(byId(edges(doc), 'success|cmd').targetHandle).toBeUndefined();
	});

	it('reads an agent’s verdict from its contract', () => {
		const doc = aFlow([
			aStage({ id: 'rev', type: 'agent', agent: 'r' }),
			aStage({ id: 'next', input: { said: { from: 'edge.verdict' } } })
		]);
		const agents = [{ name: 'r', outputs: [{ name: 'v', type: 'verdict' }] }];
		expect(byId(edges(doc, { agents }), 'success|rev').targetHandle).toBe('in:said');
	});
});

describe('failure edges', () => {
	const failing = (on_fail: FlowDoc['stages'][number]['on_fail']) =>
		edges(aFlow([aStage({ id: 'a', on_fail }), aStage({ id: 'b' })]));

	it.each<[string, Parameters<typeof failing>[0], string, string]>([
		['a counted retry', { to: 'b', max: 3 }, 'on_fail|a|0', 'fail ×3'],
		['an unlimited one', { to: 'b', max: -1 }, 'on_fail|a|0', 'fail ×∞'],
		['one with no count', { to: 'b' }, 'on_fail|a|0', 'fail ×?'],
		['abort', { to: ABORT }, 'on_fail|a|0', 'fail → abort'],
		['end', { to: END }, 'on_fail|a|0', 'fail → end'],
		['done', { to: DONE }, 'on_fail|a|0', 'fail → done'],
		['fail', { to: FAIL }, 'on_fail|a|0', 'fail → fail'],
		['debug', { to: DEBUG }, 'on_fail|a|0', 'fail'],
		[
			'a fallback',
			{ to: 'b', max: 1, then: { to: 'a', max: 2 } },
			'on_fail|a|1',
			'fail · then#1 ×2'
		],
		[
			'a fallback to abort',
			{ to: 'b', max: 1, then: { to: ABORT } },
			'on_fail|a|1',
			'fail · then#1 → abort'
		],
		[
			'a fallback to debug',
			{ to: 'b', max: 1, then: { to: DEBUG } },
			'on_fail|a|1',
			'fail · then#1'
		]
	])('labels %s in words', (_, edge, id, label) => {
		expect(byId(failing(edge), id).label).toBe(label);
	});

	it('names each kind in its hue, its word without the on_', () => {
		const list = edges(
			aFlow([
				aStage({
					id: 'a',
					on_reject: { to: 'a', max: 1 },
					on_timeout: { to: 'a', max: 1 }
				})
			])
		);
		expect(byId(list, 'on_reject|a|0').label).toBe('reject ×1');
		expect(byId(list, 'on_reject|a|0').style).toContain('var(--warning)');
		expect(byId(list, 'on_timeout|a|0').label).toBe('timeout ×1');
		expect(byId(list, 'on_timeout|a|0').style).toContain('var(--accent-2)');
	});

	it('dashes a first link and fades a fallback', () => {
		const list = failing({ to: 'b', max: 1, then: { to: 'a', max: 2 } });
		expect(byId(list, 'on_fail|a|0').class).toBe('e-dash');
		expect(byId(list, 'on_fail|a|1').class).toBe('e-dash e-then');
		expect(byId(list, 'on_fail|a|0').animated).toBe(true);
	});

	it.each<[string, string]>([
		[FAIL, '__fail'],
		[DEBUG, '__debug:a'],
		[ABORT, '__abort'],
		[END, '__end'],
		[DONE, '__done'],
		['b', 'b']
	])('sends a link to %s to the node %s', (to, target) => {
		expect(byId(failing({ to, max: 1 }), 'on_fail|a|0').target).toBe(target);
	});

	it('lands an edge on the dot that receives what it carries', () => {
		const doc = aFlow([
			aStage({ id: 'a', on_fail: { to: 'fix', max: 1 } }),
			aStage({ id: 'gate', type: 'agent', on_reject: { to: 'fix', max: 1 } }),
			aStage({
				id: 'fix',
				input: { why: { from: 'edge.failure' }, said: { from: 'edge.verdict' } }
			})
		]);
		const list = edges(doc);
		expect(byId(list, 'on_fail|a|0').targetHandle).toBe('in:why');
		expect(byId(list, 'on_reject|gate|0').targetHandle).toBe('in:said');
	});

	it('numbers each label’s row under its stage', () => {
		const list = edges(
			aFlow([
				aStage({
					id: 'a',
					on_fail: { to: 'a', max: 1, then: { to: DEBUG } },
					on_timeout: { to: ABORT }
				})
			])
		);
		expect(byId(list, 'on_fail|a|0').data?.row).toBe(0);
		expect(byId(list, 'on_fail|a|1').data?.row).toBe(1);
		expect(byId(list, 'on_timeout|a|0').data?.row).toBe(2);
	});

	it('selects the edge when its label is clicked', () => {
		const pick = vi.fn();
		const doc = aFlow([aStage({ id: 'a', on_fail: { to: 'a', max: 1 } })]);
		(byId(edges(doc, {}, pick), 'on_fail|a|0').data?.pick as () => void)();
		expect(pick).toHaveBeenCalledWith({ kind: 'edge', stage: 'a', edge: 'on_fail' });
	});
});

describe('the debug leaf’s return', () => {
	it('comes back to the stage that parked, labelled', () => {
		const pick = vi.fn();
		const list = edges(aFlow([aStage({ id: 'a', on_fail: { to: DEBUG } })]), {}, pick);
		const back = byId(list, 'debugreturn|a');
		expect(back).toMatchObject({ source: '__debug:a', target: 'a' });
		expect(back.label).toContain('again');
		(back.data?.pick as () => void)();
		expect(pick).toHaveBeenCalledWith({ kind: 'stage', id: 'a' });
	});

	it('is drawn only for a stage that parks', () => {
		expect(has(edges(aFlow([aStage({ id: 'a' })])), 'debugreturn|a')).toBe(false);
	});
});

describe('entries and branches', () => {
	it('points the start at the first stage', () => {
		expect(byId(edges(aFlow([aStage({ id: 'a' })])), 'entry|__start')).toMatchObject({
			source: '__start',
			target: 'a'
		});
	});

	it('has no entry in an empty flow', () => {
		expect(has(edges(aFlow([])), 'entry|__start')).toBe(false);
	});

	it('has the start of a flow that only watches go to the hub, unselectable', () => {
		const doc = readDoc(`
name: f
version: 5
parameters: {}
watchers: [{ id: w, on: manual, stages: [{ id: x, type: command, run: x }] }]
`);
		expect(byId(edges(doc), 'entry|__start')).toMatchObject({
			target: '__hub',
			selectable: false
		});
	});

	it('branches each watcher and outcome from the hub, then on to its first stage', () => {
		const list = edges(chains());
		expect(byId(list, 'hub|w')).toMatchObject({
			source: '__hub',
			target: '__trigger:w',
			targetHandle: 'hub'
		});
		expect(byId(list, 'entry|__trigger:w')).toMatchObject({ source: '__trigger:w', target: 'x' });
	});

	it('points a chainless outcome’s marker straight at its end', () => {
		expect(byId(edges(chains()), 'entry|__trigger:bye').target).toBe('__abort:bye');
	});

	it('colours a chain by what it is', () => {
		const list = edges(chains());
		expect(byId(list, 'entry|__trigger:w').style).toContain('var(--accent)');
		expect(byId(list, 'entry|__trigger:bye').style).toContain('var(--danger)');
	});
});

describe('dataflow', () => {
	const flowing = readDoc(`
name: f
version: 5
parameters: {}
main:
  - { id: w, type: command, run: w, output: { o: { to: p.md } } }
  - { id: r, type: command, run: r, input: { i: { from: p.md } } }
watchers:
  - id: later
    on: manual
    stages:
      - { id: x, type: command, run: x, input: { i: { from: p.md } } }
`);

	it('draws a link behind the control edges, untouchable', () => {
		const list = edges(flowing);
		expect(list[0]).toMatchObject({
			id: 'data|w|r|p.md',
			source: 'w',
			sourceHandle: 'out:o',
			target: 'r',
			targetHandle: 'in:i',
			selectable: false,
			focusable: false,
			deletable: false,
			zIndex: -1,
			class: 'e-data'
		});
	});

	it('draws none across chains: the port says it', () => {
		expect(has(edges(flowing), 'data|w|x|p.md')).toBe(false);
	});
});

describe('a sequence on its own', () => {
	const view = { sequence: { id: 'q', exit: 'after' } };
	const seq = (stages: FlowDoc['stages']): FlowDoc => ({ name: 'f', parameters: {}, stages });

	it('goes on to the next stage, the last leaving the sequence', () => {
		const list = edges(seq([aStage({ id: 'a' }), aStage({ id: 'b' })]), view);
		expect(byId(list, 'success|a').target).toBe('b');
		expect(byId(list, 'success|b').target).toBe('__exit');
	});

	it('keeps a stage’s own success', () => {
		const list = edges(
			seq([aStage({ id: 'a', on_success: 'b' }), aStage({ id: 'b', on_success: 'a' })]),
			view
		);
		expect(byId(list, 'success|b').target).toBe('a');
	});

	it('enters on its first stage', () => {
		expect(byId(edges(seq([aStage({ id: 'a' })]), view), 'entry|__start').target).toBe('a');
	});
});
