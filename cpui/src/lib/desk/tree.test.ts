import { describe, expect, it } from 'vitest';
import {
	bumpIds,
	getAt,
	hit,
	isPreset,
	lay,
	leaf,
	leavesOf,
	mapLeaf,
	norm,
	PRESET_IDS,
	PRESETS,
	setAt,
	split,
	validTree,
	VIEW_LABEL,
	VIEWS,
	type Divider,
	type DropZone,
	type Leaf,
	type LeafRect,
	type Node,
	type Split,
	type ViewId
} from './tree';

const asLeaf = (n: unknown) => n as Leaf;
const asSplit = (n: unknown) => n as Split;

describe('the views', () => {
	it('labels every view', () => {
		for (const v of VIEWS) expect(VIEW_LABEL[v]).not.toContain('desk.view');
	});

	it('names a view by its word', () => {
		expect(VIEW_LABEL.session.length).toBeGreaterThan(0);
		expect(new Set(VIEWS.map((v) => VIEW_LABEL[v])).size).toBe(VIEWS.length);
	});
});

describe('the presets', () => {
	it('knows its presets', () => {
		expect(PRESET_IDS).toEqual(['split', 'focus', 'dock', 'recipe', 'bench']);
		expect(isPreset('dock')).toBe(true);
		expect(isPreset('custom')).toBe(false);
	});

	it.each([
		['split', 'Split'],
		['focus', 'Focus'],
		['dock', 'Dock'],
		['recipe', 'Recipe'],
		['bench', 'Bench']
	] as const)('labels %s', (id, label) => {
		expect(PRESETS[id].label).toBe(label);
	});

	it.each(PRESET_IDS)('holds every view exactly once in %s', (id) => {
		const views = leavesOf(PRESETS[id].make()).flatMap((l) => l.tabs);
		expect([...views].sort()).toEqual([...VIEWS].sort());
	});

	it.each(PRESET_IDS)('makes a tree that survives its own checks: %s', (id) => {
		const tree = PRESETS[id].make();
		expect(validTree(tree)).toBe(true);
		const walk = (n: Node) => {
			if (n.k === 'leaf') return;
			expect(n.s.reduce((a, b) => a + b, 0)).toBeCloseTo(1);
			expect(n.s).toHaveLength(n.c.length);
			n.c.forEach(walk);
		};
		walk(tree);
	});

	it('gives a fresh tree each time, with fresh ids', () => {
		const a = leavesOf(PRESETS.split.make()).map((l) => l.id);
		const b = leavesOf(PRESETS.split.make()).map((l) => l.id);
		expect(a.filter((id) => b.includes(id))).toEqual([]);
	});

	it('opens focus on the session', () => {
		expect(PRESETS.focus.make().act).toBe('session');
	});
});

describe('leaf', () => {
	it('fronts its first tab unless told', () => {
		expect(leaf(['flow', 'ask']).act).toBe('flow');
		expect(leaf(['flow', 'ask'], 'ask').act).toBe('ask');
	});

	it('gives each its own id', () => {
		expect(leaf(['flow']).id).not.toBe(leaf(['flow']).id);
	});
});

describe('norm', () => {
	it('keeps nothing as nothing', () => {
		expect(norm(null)).toBeNull();
	});

	it('drops an empty leaf', () => {
		expect(norm(leaf([]))).toBeNull();
	});

	it('moves the front tab off one that is gone', () => {
		const l = { ...leaf(['flow', 'ask']), act: 'logs' as ViewId };
		expect(asLeaf(norm(l)).act).toBe('flow');
	});

	it('keeps a good front tab', () => {
		expect(asLeaf(norm(leaf(['flow', 'ask'], 'ask'))).act).toBe('ask');
	});

	it('collapses a split down to its one survivor', () => {
		const kept = leaf(['flow']);
		expect(norm(split('row', [0.5, 0.5], [kept, leaf([])]))).toEqual(kept);
	});

	it('drops a split of nothing', () => {
		expect(norm(split('row', [0.5, 0.5], [leaf([]), leaf([])]))).toBeNull();
	});

	it('renormalises what is left', () => {
		const out = asSplit(
			norm(split('row', [0.2, 0.2, 0.6], [leaf(['flow']), leaf([]), leaf(['ask'])]))
		);
		expect(out.s[0]).toBeCloseTo(0.25);
		expect(out.s[1]).toBeCloseTo(0.75);
	});

	it('flattens same-direction nesting, multiplying the fractions', () => {
		const out = asSplit(
			norm(
				split(
					'row',
					[0.5, 0.5],
					[leaf(['flow']), split('row', [0.5, 0.5], [leaf(['ask']), leaf(['logs'])])]
				)
			)
		);
		expect(out.c).toHaveLength(3);
		expect(out.s[0]).toBeCloseTo(0.5);
		expect(out.s[1]).toBeCloseTo(0.25);
		expect(out.s[2]).toBeCloseTo(0.25);
	});

	it('keeps a nested split of the other direction', () => {
		const out = asSplit(
			norm(
				split(
					'row',
					[0.5, 0.5],
					[leaf(['flow']), split('col', [0.5, 0.5], [leaf(['ask']), leaf(['logs'])])]
				)
			)
		);
		expect(out.c).toHaveLength(2);
		expect(out.c[1].k).toBe('split');
	});

	it('flattens what a collapse exposed', () => {
		// The col collapses to its row, which then belongs in the outer row.
		const inner = split('row', [0.5, 0.5], [leaf(['ask']), leaf(['logs'])]);
		const out = asSplit(
			norm(split('row', [0.5, 0.5], [leaf(['flow']), split('col', [0.5, 0.5], [inner, leaf([])])]))
		);
		expect(out.c).toHaveLength(3);
		expect(out.c.every((c) => c.k === 'leaf')).toBe(true);
	});

	it('does not change what it was given', () => {
		const tree = split('row', [0.2, 0.8], [leaf(['flow']), leaf([])]);
		norm(tree);
		expect(tree.c).toHaveLength(2);
	});
});

describe('walking the tree', () => {
	const tree = () =>
		split(
			'row',
			[0.5, 0.5],
			[leaf(['flow']), split('col', [0.5, 0.5], [leaf(['ask']), leaf(['logs'])])]
		);

	it('lists the leaves in order', () => {
		expect(leavesOf(tree()).flatMap((l) => l.tabs)).toEqual(['flow', 'ask', 'logs']);
		expect(leavesOf(null)).toEqual([]);
	});

	it('maps one leaf by id, deep, the rest kept', () => {
		const t = tree();
		const [, deep] = leavesOf(t);
		const out = mapLeaf(t, deep.id, (l) => ({ ...l, tabs: ['terminal'], act: 'terminal' }));
		expect(leavesOf(out).flatMap((l) => l.tabs)).toEqual(['flow', 'terminal', 'logs']);
		expect(leavesOf(t).flatMap((l) => l.tabs)).toEqual(['flow', 'ask', 'logs']);
	});

	it('maps nothing for an unknown id or an empty tree', () => {
		const t = tree();
		expect(leavesOf(mapLeaf(t, 'nope', () => leaf(['vitals']))).flatMap((l) => l.tabs)).toEqual([
			'flow',
			'ask',
			'logs'
		]);
		expect(mapLeaf(null, 'x', () => leaf(['vitals']))).toBeNull();
	});

	it('finds a node by path', () => {
		const t = tree();
		expect(getAt(t, [])).toBe(t);
		expect(asLeaf(getAt(t, [1, 1])).tabs).toEqual(['logs']);
	});

	it('stays put at a leaf the path runs past', () => {
		const t = tree();
		expect(asLeaf(getAt(t, [0, 5])).tabs).toEqual(['flow']);
	});

	it('changes a node by path, the tree not mutated', () => {
		const t = tree();
		const out = setAt(t, [1], (n) => ({ ...(n as Split), s: [0.1, 0.9] }));
		expect(asSplit(getAt(out, [1])).s).toEqual([0.1, 0.9]);
		expect(asSplit(getAt(t, [1])).s).toEqual([0.5, 0.5]);
	});

	it('changes the root with no path, and nothing past a leaf', () => {
		const l = leaf(['flow']);
		const other = leaf(['ask']);
		expect(setAt(l, [], () => other)).toBe(other);
		expect(setAt(l, [0], () => other)).toBe(l);
	});
});

describe('lay', () => {
	const laid = (t: Node, w = 1000, h = 500, gap = 10) => {
		const out = { leaves: [] as LeafRect[], divs: [] as Divider[] };
		lay(t, 0, 0, w, h, [], out, gap);
		return out;
	};

	it('fills the desk with one leaf', () => {
		const { leaves, divs } = laid(leaf(['flow']));
		expect(leaves[0]).toMatchObject({ x: 0, y: 0, w: 1000, h: 500 });
		expect(divs).toEqual([]);
	});

	it('divides a row by its fractions, minus the gap', () => {
		const { leaves, divs } = laid(split('row', [0.25, 0.75], [leaf(['flow']), leaf(['ask'])]));
		expect(leaves[0]).toMatchObject({ x: 0, w: 247.5, h: 500 });
		expect(leaves[1]).toMatchObject({ x: 257.5, w: 742.5, h: 500 });
		expect(divs).toEqual([{ path: [], i: 0, row: true, tot: 990, x: 247.5, y: 0, w: 10, h: 500 }]);
	});

	it('divides a column the same way', () => {
		const { leaves, divs } = laid(split('col', [0.5, 0.5], [leaf(['flow']), leaf(['ask'])]));
		expect(leaves[1]).toMatchObject({ x: 0, y: 255, w: 1000, h: 245 });
		expect(divs[0]).toMatchObject({ row: false, x: 0, y: 245, w: 1000, h: 10 });
	});

	it('finds the path of a nested gutter', () => {
		const { divs } = laid(
			split(
				'row',
				[0.5, 0.5],
				[leaf(['flow']), split('col', [0.5, 0.5], [leaf(['ask']), leaf(['logs'])])]
			)
		);
		expect(divs.map((d) => d.path)).toEqual([[], [1]]);
	});

	it('offsets a leaf by where its split stands', () => {
		const out = { leaves: [] as LeafRect[], divs: [] as Divider[] };
		lay(leaf(['flow']), 5, 6, 10, 10, [], out, 0);
		expect(out.leaves[0]).toMatchObject({ x: 5, y: 6 });
	});

	it('leaves no gap past the last child', () => {
		const { leaves } = laid(split('row', [0.5, 0.5], [leaf(['flow']), leaf(['ask'])]));
		expect(leaves[1].x + leaves[1].w).toBe(1000);
	});
});

describe('validTree', () => {
	const good = () => split('row', [0.5, 0.5], [leaf(['flow']), leaf(['ask'])]);

	it.each<[string, unknown]>([
		['nothing, an empty bench', null],
		['a leaf', { k: 'leaf', id: 'L1', tabs: ['flow'] }],
		['a split', good()]
	])('trusts %s', (_, tree) => {
		expect(validTree(tree)).toBe(true);
	});

	it.each<[string, unknown]>([
		['undefined', undefined],
		['a number', 3],
		['a string', 'tree'],
		['an empty object', {}],
		['an unknown kind', { k: 'nope' }],
		['a leaf with no id', { k: 'leaf', tabs: ['flow'] }],
		['a leaf with a numeric id', { k: 'leaf', id: 1, tabs: ['flow'] }],
		['a leaf with no tabs', { k: 'leaf', id: 'L1', tabs: [] }],
		['a leaf with tabs that are no list', { k: 'leaf', id: 'L1', tabs: 'flow' }],
		['an unknown view', { k: 'leaf', id: 'L1', tabs: ['gone'] }],
		['a view twice in a leaf', { k: 'leaf', id: 'L1', tabs: ['flow', 'flow'] }],
		['a view in two leaves', split('row', [0.5, 0.5], [leaf(['flow']), leaf(['flow'])])],
		['a bad direction', { k: 'split', dir: 'diag', s: [1], c: [leaf(['flow'])] }],
		['a split with no children', { k: 'split', dir: 'row', s: [1] }],
		['a split with no fractions', { k: 'split', dir: 'row', c: [leaf(['flow'])] }],
		['fractions that do not match', { k: 'split', dir: 'row', s: [1, 2], c: [leaf(['flow'])] }],
		['a child that is nothing', { k: 'split', dir: 'row', s: [1], c: [null] }]
	])('refuses %s', (_, tree) => {
		expect(validTree(tree)).toBe(false);
	});

	it('refuses fractions that are not numbers', () => {
		expect(validTree({ k: 'split', dir: 'row', s: ['a', 'b'], c: good().c })).toBe(false);
	});
});

describe('bumpIds', () => {
	it('keeps new ids ahead of the ones restored', () => {
		const restored: Leaf = { k: 'leaf', id: 'L900000', tabs: ['flow'], act: 'flow' };
		bumpIds(restored);
		expect(Number(leaf(['ask']).id.slice(1))).toBeGreaterThan(900000);
	});

	it('takes an empty tree', () => {
		expect(() => bumpIds(null)).not.toThrow();
	});

	it('never takes ids backward', () => {
		const before = Number(leaf(['ask']).id.slice(1));
		bumpIds({ k: 'leaf', id: 'L1', tabs: ['flow'], act: 'flow' });
		expect(Number(leaf(['ask']).id.slice(1))).toBe(before + 1);
	});
});

describe('hit', () => {
	const l = leaf(['flow']);
	const rects: LeafRect[] = [{ n: l, x: 100, y: 100, w: 400, h: 400 }];
	const HEAD = 30;

	it('finds nothing outside every leaf', () => {
		expect(hit(rects, { x: 50, y: 300 }, HEAD)).toBeNull();
		expect(hit([], { x: 300, y: 300 }, HEAD)).toBeNull();
	});

	it.each<[string, number, number, DropZone]>([
		['the middle', 300, 300, 'center'],
		['the tab strip, even at an edge', 110, 110, 'center'],
		['the left edge', 110, 300, 'left'],
		['the right edge', 490, 300, 'right'],
		['the top edge, under the strip', 300, 140, 'top'],
		['the bottom edge', 300, 490, 'bottom'],
		['just inside the 26% band', 203, 300, 'left'],
		['just past it', 205, 300, 'center']
	])('reads %s', (_, x, y, zone) => {
		expect(hit(rects, { x, y }, HEAD)?.zone).toBe(zone);
	});

	it('takes the nearest edge in a corner', () => {
		expect(hit(rects, { x: 105, y: 480 }, HEAD)?.zone).toBe('left');
		expect(hit(rects, { x: 130, y: 495 }, HEAD)?.zone).toBe('bottom');
	});

	it.each<[DropZone, number, number, number, number, number, number, string]>([
		['center', 300, 300, 100, 100, 400, 400, 'Stack here'],
		['left', 110, 300, 100, 100, 200, 400, 'Split left'],
		['right', 490, 300, 300, 100, 200, 400, 'Split right'],
		['top', 300, 140, 100, 100, 400, 200, 'Split top'],
		['bottom', 300, 490, 100, 300, 400, 200, 'Split bottom']
	])('previews %s where it would land', (zone, px, py, x, y, w, h, label) => {
		expect(hit(rects, { x: px, y: py }, HEAD)).toEqual({
			id: l.id,
			zone,
			x,
			y,
			w,
			h,
			label
		});
	});
});
