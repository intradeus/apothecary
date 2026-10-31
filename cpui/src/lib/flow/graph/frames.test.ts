import type { Node } from '@xyflow/svelte';
import { describe, expect, it } from 'vitest';
import { readDoc } from '$test/fixtures/logic';
import { chainFrames } from './frames';

const doc = readDoc(`
name: f
version: 5
parameters: {}
main: [{ id: a, type: command, run: a }]
watchers:
  - { id: w, on: manual, stages: [{ id: x, type: command, run: x }] }
  - { id: v, on: manual, stages: [{ id: y, type: command, run: y }] }
outcomes:
  - { id: o, on: mr_merged, then: abort }
`);

const node = (x: number, y: number, band: string, over: Partial<Node> = {}): Node => ({
	id: `${band}-${x}`,
	type: 'stage',
	position: { x, y },
	measured: { width: 100, height: 50 },
	data: { band },
	...over
});

describe('chainFrames', () => {
	it('wraps a chain’s nodes, padded', () => {
		const [frame] = chainFrames(doc, [node(0, 0, 'w'), node(200, 30, 'w')], new Set());
		expect(frame).toEqual({
			id: 'w',
			hue: 'accent',
			x: -16,
			y: -16,
			w: 332,
			h: 112,
			folded: false
		});
	});

	it('draws none for the main chain', () => {
		expect(chainFrames(doc, [node(0, 0, 'main')], new Set())).toEqual([]);
	});

	it.each<[string, Partial<Node>]>([
		['a folded node', { hidden: true }],
		['a node leaving', { data: { band: 'w', leaving: true } }],
		['a node of no chain', { data: {} }],
		['a node of an unknown chain', { data: { band: 'gone' } }]
	])('leaves out %s', (_, over) => {
		expect(chainFrames(doc, [node(0, 0, 'w', over)], new Set())).toEqual([]);
	});

	it.each<[Partial<Node>, number, number]>([
		[{ type: 'stage', measured: undefined }, 220, 120],
		[{ type: 'end', measured: undefined }, 120, 44]
	])('guesses the size of a node nobody measured: %j', (over, w, h) => {
		const [frame] = chainFrames(doc, [node(0, 0, 'w', over)], new Set());
		expect(frame.w).toBe(w + 32);
		expect(frame.h).toBe(h + 32);
	});

	it('leaves room for a stage’s edge labels', () => {
		const [frame] = chainFrames(
			doc,
			[node(0, 0, 'w', { data: { band: 'w', labels: 2 } })],
			new Set()
		);
		expect(frame.h).toBe(50 + 6 + 2 * 20 + 32);
	});

	it('keeps the document’s order, whatever the nodes’', () => {
		const frames = chainFrames(doc, [node(0, 0, 'o'), node(0, 0, 'v'), node(0, 0, 'w')], new Set());
		expect(frames.map((f) => f.id)).toEqual(['w', 'v', 'o']);
	});

	it('colours an outcome the end it says', () => {
		expect(chainFrames(doc, [node(0, 0, 'o')], new Set())[0].hue).toBe('danger');
	});

	it('marks a folded chain', () => {
		const frames = chainFrames(doc, [node(0, 0, 'w'), node(0, 0, 'v')], new Set(['v']));
		expect(frames.map((f) => f.folded)).toEqual([false, true]);
	});
});
