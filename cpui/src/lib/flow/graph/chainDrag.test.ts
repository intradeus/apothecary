import type { Node } from '@xyflow/svelte';
import { describe, expect, it } from 'vitest';
import { dragChain, startChainDrag } from './chainDrag';

const node = (id: string, x: number, y: number, data: object = {}): Node => ({
	id,
	position: { x, y },
	data: data as Record<string, unknown>
});

const nodes = () => [
	node('__trigger:w', 0, 0, { band: 'w' }),
	node('a', 100, 10, { band: 'w' }),
	node('b', 200, 20, { band: 'w' }),
	node('ghost', 300, 30, { band: 'w', leaving: true }),
	node('other', 400, 40, { band: 'main' })
];

describe('startChainDrag', () => {
	it('takes the marker’s chain along, not what is leaving or another chain', () => {
		const list = nodes();
		const g = startChainDrag(list[0], list);
		expect(g?.anchor).toBe('__trigger:w');
		expect(g?.from).toEqual({ x: 0, y: 0 });
		expect([...g!.members.keys()]).toEqual(['a', 'b']);
	});

	it('starts from __start too', () => {
		const start = node('__start', 0, 0, { band: 'main' });
		expect(startChainDrag(start, [start, nodes()[4]])?.members.has('other')).toBe(true);
	});

	it.each<[string, Node | null]>([
		['nothing', null],
		['a stage', node('a', 0, 0, { band: 'w' })],
		['a marker with no chain', node('__trigger:w', 0, 0)]
	])('starts no drag from %s', (_, n) => {
		expect(startChainDrag(n, nodes())).toBeNull();
	});

	it('copies the positions, so a later move cannot shift the start', () => {
		const list = nodes();
		const g = startChainDrag(list[0], list)!;
		list[1].position.x = 999;
		expect(g.members.get('a')).toEqual({ x: 100, y: 10 });
	});
});

describe('dragChain', () => {
	it('moves the chain by what the marker moved, and nothing else', () => {
		const list = nodes();
		const g = startChainDrag(list[0], list)!;
		const moved = node('__trigger:w', 15, -5, { band: 'w' });
		const out = dragChain(g, moved, list);
		expect(out.find((n) => n.id === 'a')?.position).toEqual({ x: 115, y: 5 });
		expect(out.find((n) => n.id === 'b')?.position).toEqual({ x: 215, y: 15 });
		expect(out.find((n) => n.id === 'other')).toBe(list[4]);
		expect(out.find((n) => n.id === 'ghost')).toBe(list[3]);
	});

	it('moves from where the drag began, not from the last step', () => {
		const list = nodes();
		const g = startChainDrag(list[0], list)!;
		const once = dragChain(g, node('__trigger:w', 10, 0), list);
		const twice = dragChain(g, node('__trigger:w', 20, 0), once);
		expect(twice.find((n) => n.id === 'a')?.position.x).toBe(120);
	});
});
