import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { DeskState } from './desk.svelte';
import { leaf, leavesOf, PRESETS, split, type LeafRect, type Tree, type ViewId } from './tree';

const KEY = 'apothecary.desk.v1';

const stored = () => JSON.parse(localStorage.getItem(KEY) ?? 'null');
const tabs = (d: DeskState) => leavesOf(d.tree).map((l) => l.tabs);
const allViews = (d: DeskState) => tabs(d).flat();

/** A desk on a known layout: flow beside session and ask. */
function deskOf(tree: Tree = split('row', [0.5, 0.5], [leaf(['flow']), leaf(['session', 'ask'])])) {
	const d = new DeskState();
	d.tree = tree;
	d.preset = 'custom';
	return d;
}
const leafWith = (d: DeskState, v: ViewId) => d.leafOf(v)!;

beforeEach(() => {
	localStorage.clear();
});
afterEach(() => {
	localStorage.clear();
	vi.useRealTimers();
});

describe('starting up', () => {
	it('starts on the split preset with nothing stored', () => {
		const d = new DeskState();
		expect(d.preset).toBe('split');
		expect(d.label).toBe('Split');
		expect(allViews(d)).toContain('session');
	});

	it('restores what was stored', () => {
		const tree = leaf(['flow', 'logs'], 'logs');
		localStorage.setItem(KEY, JSON.stringify({ tree, preset: 'dock' }));
		const d = new DeskState();
		expect(d.tree).toEqual(tree);
		expect(d.preset).toBe('dock');
	});

	it.each([
		['no preset', undefined],
		['an unknown one', 'gone']
	])('reads a stored tree with %s as custom', (_, preset) => {
		localStorage.setItem(KEY, JSON.stringify({ tree: leaf(['flow']), preset }));
		const d = new DeskState();
		expect(d.preset).toBe('custom');
		expect(d.label).toBe('Custom');
	});

	it('keeps a stored custom layout custom', () => {
		localStorage.setItem(KEY, JSON.stringify({ tree: leaf(['flow']), preset: 'custom' }));
		expect(new DeskState().preset).toBe('custom');
	});

	it('restores an empty bench', () => {
		localStorage.setItem(KEY, JSON.stringify({ tree: null, preset: 'custom' }));
		expect(new DeskState().tree).toBeNull();
	});

	it.each([
		['corrupt JSON', '{nope'],
		['a bad tree', JSON.stringify({ tree: { k: 'leaf', id: 'L1', tabs: ['gone'] } })],
		['no tree', JSON.stringify({ preset: 'dock' })],
		['null', 'null']
	])('starts from the default on %s', (_, raw) => {
		localStorage.setItem(KEY, raw);
		expect(new DeskState().preset).toBe('split');
	});

	it('starts from the default with storage blocked', () => {
		vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
			throw new Error('blocked');
		});
		expect(new DeskState().preset).toBe('split');
	});

	it('normalises what it restores', () => {
		const tree = split(
			'row',
			[0.5, 0.5],
			[leaf(['flow']), split('row', [0.5, 0.5], [leaf(['ask']), leaf(['logs'])])]
		);
		localStorage.setItem(KEY, JSON.stringify({ tree, preset: 'custom' }));
		expect(tabs(new DeskState())).toEqual([['flow'], ['ask'], ['logs']]);
	});

	it('hands out ids past the restored ones', () => {
		// The very id the counter would give next.
		const taken = `L${Number(leaf(['flow']).id.slice(1)) + 1}`;
		const tree = { k: 'leaf', id: taken, tabs: ['flow', 'ask'], act: 'flow' };
		localStorage.setItem(KEY, JSON.stringify({ tree, preset: 'custom' }));
		const d = new DeskState();
		d.moveView('ask', taken, { id: taken, zone: 'right' });
		const ids = leavesOf(d.tree).map((l) => l.id);
		expect(new Set(ids).size).toBe(2);
	});
});

describe('structural changes', () => {
	it('saves the tree and its preset', () => {
		const d = deskOf();
		d.set(leaf(['flow']), 'custom');
		expect(stored()).toMatchObject({ preset: 'custom', tree: { k: 'leaf', tabs: ['flow'] } });
	});

	it('defaults to custom: anything rearranged is', () => {
		const d = deskOf();
		d.preset = 'dock';
		d.set(leaf(['flow']));
		expect(d.preset).toBe('custom');
	});

	it('restores a maximised window: the layout it hid just changed', () => {
		const d = deskOf();
		d.max = 'x';
		d.set(leaf(['flow']));
		expect(d.max).toBeNull();
	});

	it('survives storage being full', () => {
		const d = deskOf();
		vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
			throw new Error('full');
		});
		expect(() => d.set(leaf(['flow']))).not.toThrow();
		expect(tabs(d)).toEqual([['flow']]);
	});

	it('applies a preset under its name', () => {
		const d = deskOf();
		d.applyPreset('recipe');
		expect(d.preset).toBe('recipe');
		expect(d.label).toBe('Recipe');
		expect(stored().preset).toBe('recipe');
	});
});

describe('motion', () => {
	beforeEach(() => {
		vi.useFakeTimers();
	});

	it('springs on a change, settling after', () => {
		const d = deskOf();
		d.animate();
		expect(d.motion).toBe('spring');
		vi.advanceTimersByTime(499);
		expect(d.motion).toBe('spring');
		vi.advanceTimersByTime(1);
		expect(d.motion).toBe('');
	});

	it('bounces longer', () => {
		const d = deskOf();
		d.animate('bounce');
		vi.advanceTimersByTime(649);
		expect(d.motion).toBe('bounce');
		vi.advanceTimersByTime(1);
		expect(d.motion).toBe('');
	});

	it('restarts the settling when interrupted', () => {
		const d = deskOf();
		d.animate();
		vi.advanceTimersByTime(400);
		d.animate();
		vi.advanceTimersByTime(400);
		expect(d.motion).toBe('spring');
	});

	it('tracks a gutter drag 1:1, unsaved', () => {
		const d = deskOf();
		d.animate();
		d.resizeLive(split('row', [0.2, 0.8], [leaf(['flow']), leaf(['ask'])]));
		expect(d.motion).toBe('');
		vi.advanceTimersByTime(1000);
		expect(d.motion).toBe('');
		expect(stored()).toBeNull();
		expect(tabs(d)).toEqual([['flow'], ['ask']]);
	});

	it('saves a released gutter as custom', () => {
		const d = deskOf();
		d.preset = 'dock';
		d.resizeDone(leaf(['flow']), false);
		expect(d.preset).toBe('custom');
		expect(d.motion).toBe('');
		expect(stored().preset).toBe('custom');
	});

	it('bounces a gutter back from a limit', () => {
		const d = deskOf();
		d.resizeDone(leaf(['flow']), true);
		expect(d.motion).toBe('bounce');
	});

	it('springs a maximise, and toggles it', () => {
		const d = deskOf();
		d.toggleMax('a');
		expect(d.max).toBe('a');
		expect(d.motion).toBe('spring');
		d.toggleMax('b');
		expect(d.max).toBe('b');
		d.toggleMax('b');
		expect(d.max).toBeNull();
	});
});

describe('tabs and views', () => {
	it('finds the leaf holding a view', () => {
		const d = deskOf();
		expect(d.leafOf('ask')?.tabs).toEqual(['session', 'ask']);
		expect(d.leafOf('logs')).toBeUndefined();
	});

	it('knows what is visible', () => {
		const d = deskOf();
		expect(d.visible.has('flow')).toBe(true);
		expect(d.visible.has('logs')).toBe(false);
	});

	it('activates a tab, keeping the preset and a maximised window', () => {
		const d = deskOf();
		d.preset = 'dock';
		const l = leafWith(d, 'ask');
		d.max = l.id;
		d.activate(l.id, 'ask');
		expect(leafWith(d, 'ask').act).toBe('ask');
		expect(d.focus).toBe(l.id);
		expect(d.preset).toBe('dock');
		expect(d.max).toBe(l.id);
	});

	it('closes a view, its leaf going with its last', () => {
		const d = deskOf();
		d.closeView('flow');
		expect(tabs(d)).toEqual([['session', 'ask']]);
	});

	it('moves the front off a closed view', () => {
		const d = deskOf();
		d.closeView('session');
		expect(leafWith(d, 'ask').act).toBe('ask');
	});

	it('does nothing closing a hidden view', () => {
		const d = deskOf();
		const before = d.tree;
		d.closeView('logs');
		expect(d.tree).toBe(before);
	});

	it('closes a whole leaf', () => {
		const d = deskOf();
		d.closeLeaf(leafWith(d, 'ask').id);
		expect(tabs(d)).toEqual([['flow']]);
	});

	it('leaves an empty bench when the last goes', () => {
		const d = deskOf(leaf(['flow']));
		d.closeView('flow');
		expect(d.tree).toBeNull();
	});

	it('brings a view back into the focused leaf, in front', () => {
		const d = deskOf();
		d.focus = leafWith(d, 'flow').id;
		d.showView('logs');
		expect(leafWith(d, 'logs').tabs).toEqual(['flow', 'logs']);
		expect(leafWith(d, 'logs').act).toBe('logs');
	});

	it('picks the biggest leaf with nothing focused', () => {
		const d = deskOf();
		const small = leafWith(d, 'flow');
		const big = leafWith(d, 'ask');
		d.rects = [
			{ n: small, x: 0, y: 0, w: 10, h: 10 },
			{ n: big, x: 0, y: 0, w: 100, h: 100 }
		] as LeafRect[];
		d.showView('logs');
		expect(leafWith(d, 'logs').id).toBe(big.id);
	});

	it('picks the first leaf with nothing else to go by', () => {
		const d = deskOf();
		d.showView('logs');
		expect(leafWith(d, 'logs').tabs).toContain('flow');
	});

	it('starts the bench again from nothing', () => {
		const d = deskOf(null);
		d.showView('logs');
		expect(tabs(d)).toEqual([['logs']]);
		expect(d.focus).toBe(leafWith(d, 'logs').id);
	});

	it('toggles a view', () => {
		const d = deskOf();
		d.toggleView('flow');
		expect(d.visible.has('flow')).toBe(false);
		d.toggleView('flow');
		expect(d.visible.has('flow')).toBe(true);
	});
});

describe('reveal', () => {
	it('brings a view forward and focuses its leaf', () => {
		const d = deskOf();
		d.reveal('ask');
		expect(leafWith(d, 'ask').act).toBe('ask');
		expect(d.focus).toBe(leafWith(d, 'ask').id);
	});

	it('just focuses a view already in front', () => {
		const d = deskOf();
		d.reveal('flow');
		expect(d.focus).toBe(leafWith(d, 'flow').id);
		expect(stored()).toBeNull();
	});

	it('shows a hidden view', () => {
		const d = deskOf();
		d.reveal('logs');
		expect(d.visible.has('logs')).toBe(true);
	});

	it('restores a window maximised over it', () => {
		const d = deskOf();
		d.max = leafWith(d, 'flow').id;
		d.reveal('ask');
		expect(d.max).toBeNull();
	});

	it('leaves its own window maximised', () => {
		const d = deskOf();
		const id = leafWith(d, 'ask').id;
		d.max = id;
		d.reveal('ask');
		expect(d.max).toBe(id);
	});
});

describe('moving a tab', () => {
	it('stacks it on another leaf’s centre', () => {
		const d = deskOf();
		const from = leafWith(d, 'ask').id;
		const to = leafWith(d, 'flow').id;
		d.moveView('ask', from, { id: to, zone: 'center' });
		expect(tabs(d)).toEqual([['flow', 'ask'], ['session']]);
		expect(leafWith(d, 'ask').act).toBe('ask');
		expect(d.focus).toBe(to);
		expect(d.preset).toBe('custom');
	});

	it('removes a leaf it empties', () => {
		const d = deskOf();
		d.moveView('flow', leafWith(d, 'flow').id, { id: leafWith(d, 'ask').id, zone: 'center' });
		expect(tabs(d)).toEqual([['session', 'ask', 'flow']]);
	});

	it.each([
		['left', 'row', ['ask'], ['flow']],
		['right', 'row', ['flow'], ['ask']],
		['top', 'col', ['ask'], ['flow']],
		['bottom', 'col', ['flow'], ['ask']]
	] as const)('splits on the %s edge', (zone, dir, first, second) => {
		const d = deskOf();
		const from = leafWith(d, 'ask').id;
		const to = leafWith(d, 'flow').id;
		d.moveView('ask', from, { id: to, zone });
		const nested = (d.tree as { c: { dir?: string; c?: unknown[] }[] }).c.find((c) => c.c);
		if (dir === 'row') {
			expect(tabs(d)).toEqual([first, second, ['session']]);
		} else {
			expect(nested?.dir).toBe('col');
			expect(tabs(d)).toEqual([first, second, ['session']]);
		}
		expect(d.focus).toBe(leafWith(d, 'ask').id);
	});

	it('just activates a tab dropped on its own centre', () => {
		const d = deskOf();
		const from = leafWith(d, 'ask').id;
		d.moveView('ask', from, { id: from, zone: 'center' });
		expect(tabs(d)).toEqual([['flow'], ['session', 'ask']]);
		expect(leafWith(d, 'ask').act).toBe('ask');
	});

	it('leaves a lone tab where it is: there is nothing to split from', () => {
		const d = deskOf();
		d.preset = 'dock';
		const from = leafWith(d, 'flow').id;
		d.moveView('flow', from, { id: from, zone: 'left' });
		expect(tabs(d)).toEqual([['flow'], ['session', 'ask']]);
		expect(leafWith(d, 'flow').id).toBe(from);
		expect(d.preset).toBe('dock');
	});

	it('splits a leaf by one of its own tabs', () => {
		const d = deskOf();
		const from = leafWith(d, 'ask').id;
		d.moveView('ask', from, { id: from, zone: 'right' });
		expect(tabs(d)).toEqual([['flow'], ['session'], ['ask']]);
	});

	it('does nothing from a leaf that is gone', () => {
		const d = deskOf();
		const before = d.tree;
		d.moveView('ask', 'gone', { id: 'x', zone: 'center' });
		expect(d.tree).toBe(before);
	});
});

describe('a narrow screen', () => {
	it('goes to one window of every view', () => {
		const d = deskOf();
		d.setCompact(true);
		expect(d.compact).toBe(true);
		expect(d.preset).toBe('focus');
		expect(tabs(d)).toHaveLength(1);
		expect(tabs(d)[0]).toEqual(PRESETS.focus.make().tabs);
		expect(d.focus).toBe(leavesOf(d.tree)[0].id);
	});

	it('opens on the session with nothing focused', () => {
		const d = deskOf();
		d.setCompact(true);
		expect(leavesOf(d.tree)[0].act).toBe('session');
	});

	it('keeps the view in front in front', () => {
		const d = deskOf();
		d.reveal('ask');
		d.setCompact(true);
		expect(leavesOf(d.tree)[0].act).toBe('ask');
	});

	it('restores a maximised window', () => {
		const d = deskOf();
		d.max = 'x';
		d.setCompact(true);
		expect(d.max).toBeNull();
	});

	it('does not save the phone’s layout', () => {
		const d = deskOf();
		d.setCompact(true);
		d.activate(leavesOf(d.tree)[0].id, 'logs');
		expect(stored()).toBeNull();
	});

	it('gives the saved layout back, unharmed by what was done meanwhile', () => {
		const d = deskOf();
		d.preset = 'bench';
		const before = tabs(d);
		d.setCompact(true);
		d.closeView('logs');
		d.setCompact(false);
		expect(d.compact).toBe(false);
		expect(tabs(d)).toEqual(before);
		expect(d.preset).toBe('bench');
	});

	it('keeps the view in front in front on the way back', () => {
		const d = deskOf();
		d.setCompact(true);
		d.activate(leavesOf(d.tree)[0].id, 'ask');
		d.setCompact(false);
		expect(leafWith(d, 'ask').act).toBe('ask');
		expect(d.focus).toBe(leafWith(d, 'ask').id);
	});

	it('does not grow a view the wide layout hid', () => {
		const d = deskOf();
		d.setCompact(true);
		d.activate(leavesOf(d.tree)[0].id, 'logs');
		d.setCompact(false);
		expect(d.visible.has('logs')).toBe(false);
	});

	it('does nothing asked twice', () => {
		const d = deskOf();
		d.setCompact(true);
		const tree = d.tree;
		d.setCompact(true);
		expect(d.tree).toBe(tree);
		const wide = deskOf();
		const t = wide.tree;
		wide.setCompact(false);
		expect(wide.tree).toBe(t);
	});
});
