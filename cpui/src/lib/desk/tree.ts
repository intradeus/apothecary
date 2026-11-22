// The live run's window desk (DESIGN §6): a split tree of leaves, each leaf a
// stack of views. Pure model + geometry, ported from the reference's logic:
// every change goes through norm() so the tree never holds an empty leaf, a
// one-child split or two nested splits in the same direction.

import { t } from '$lib/i18n/index.svelte';

export const VIEWS = [
	'flow',
	'session',
	'ask',
	'pings',
	'phases',
	'timeline',
	'record',
	'changes',
	'files',
	'terminal',
	'logs',
	'egress',
	'services',
	'ports',
	'vitals'
] as const;
export type ViewId = (typeof VIEWS)[number];

export const VIEW_LABEL: Record<ViewId, string> = {
	get flow() {
		return t('desk.view.flow');
	},
	get session() {
		return t('desk.view.session');
	},
	get ask() {
		return t('desk.view.ask');
	},
	get pings() {
		return t('desk.view.pings');
	},
	get phases() {
		return t('desk.view.phases');
	},
	get timeline() {
		return t('desk.view.timeline');
	},
	get record() {
		return t('desk.view.record');
	},
	get changes() {
		return t('desk.view.changes');
	},
	get files() {
		return t('desk.view.files');
	},
	get terminal() {
		return t('desk.view.terminal');
	},
	get logs() {
		return t('desk.view.logs');
	},
	get egress() {
		return t('desk.view.egress');
	},
	get services() {
		return t('desk.view.services');
	},
	get ports() {
		return t('desk.view.ports');
	},
	get vitals() {
		return t('desk.view.vitals');
	}
};

export interface Leaf {
	k: 'leaf';
	id: string;
	tabs: ViewId[];
	act: ViewId;
}
export interface Split {
	k: 'split';
	dir: 'row' | 'col';
	/** Fractions, summing to 1. */
	s: number[];
	c: Node[];
}
export type Node = Leaf | Split;
/** null = every view hidden ("an empty bench"). */
export type Tree = Node | null;

/** The desk's geometry in px: the gutter between panes, a leaf's tab
 *  strip, and the smallest a pane resizes to. The values live in tokens.css
 *  (--desk-gap, --leaf-head, --pane-min); the desk reads them at mount. */
export interface Geo {
	gap: number;
	head: number;
	min: number;
}

let LID = 0;
export const leaf = (tabs: ViewId[], act?: ViewId): Leaf => ({
	k: 'leaf',
	id: 'L' + ++LID,
	tabs,
	act: act ?? tabs[0]
});
export const split = (dir: 'row' | 'col', s: number[], c: Node[]): Split => ({
	k: 'split',
	dir,
	s,
	c
});

export const PRESETS = {
	split: {
		get label() {
			return t('desk.preset.split');
		},
		make: () =>
			split(
				'col',
				[0.16, 0.84],
				[
					leaf(['flow']),
					split(
						'row',
						[0.65, 0.35],
						[
							leaf([
								'session',
								'ask',
								'pings',
								'phases',
								'timeline',
								'changes',
								'files',
								'terminal',
								'logs',
								'record'
							]),
							split('col', [0.3, 0.7], [leaf(['vitals']), leaf(['egress', 'services', 'ports'])])
						]
					)
				]
			)
	},
	// One window holding every view: the layout for a single screen of
	// attention — and the one a phone always gets (DeskState.compact).
	focus: {
		get label() {
			return t('desk.preset.focus');
		},
		make: () => leaf([...VIEWS], 'session')
	},
	dock: {
		get label() {
			return t('desk.preset.dock');
		},
		make: () =>
			split(
				'col',
				[0.16, 0.52, 0.32],
				[
					leaf(['flow']),
					split(
						'row',
						[0.6, 0.4],
						[
							leaf(['session', 'ask', 'pings', 'phases', 'timeline', 'record']),
							leaf(['changes', 'files'])
						]
					),
					split(
						'row',
						[0.62, 0.38],
						[leaf(['terminal', 'logs', 'egress', 'services', 'ports']), leaf(['vitals'])]
					)
				]
			)
	},
	recipe: {
		get label() {
			return t('desk.preset.recipe');
		},
		make: () =>
			split(
				'row',
				[0.19, 0.45, 0.36],
				[
					leaf(['flow']),
					leaf(['session', 'ask', 'pings', 'phases', 'timeline', 'record', 'logs']),
					split(
						'col',
						[0.68, 0.32],
						[
							leaf(['changes', 'files', 'terminal', 'egress', 'services', 'ports']),
							leaf(['vitals'])
						]
					)
				]
			)
	},
	bench: {
		get label() {
			return t('desk.preset.bench');
		},
		make: () =>
			split(
				'row',
				[0.34, 0.66],
				[
					leaf(['session', 'ask', 'pings', 'phases']),
					split(
						'col',
						[0.17, 0.83],
						[
							leaf(['flow']),
							leaf([
								'changes',
								'files',
								'terminal',
								'timeline',
								'logs',
								'record',
								'egress',
								'services',
								'ports',
								'vitals'
							])
						]
					)
				]
			)
	}
} satisfies Record<string, { label: string; make: () => Node }>;

export type PresetId = keyof typeof PRESETS;
export const PRESET_IDS = Object.keys(PRESETS) as PresetId[];
/** A preset id, or 'custom' once the user rearranged anything. */
export type LayoutId = PresetId | 'custom';
export const isPreset = (id: string): id is PresetId => id in PRESETS;

/** Drop empty leaves, collapse one-child splits, flatten same-direction
 *  nesting (multiplying fractions), renormalise. */
export function norm(n: Tree): Tree {
	if (!n) return null;
	if (n.k === 'leaf')
		return n.tabs.length ? { ...n, act: n.tabs.includes(n.act) ? n.act : n.tabs[0] } : null;
	const kids: Node[] = [];
	const ss: number[] = [];
	n.c.forEach((c, i) => {
		const r = norm(c);
		if (!r) return;
		if (r.k === 'split' && r.dir === n.dir)
			r.c.forEach((cc, j) => {
				kids.push(cc);
				ss.push(n.s[i] * r.s[j]);
			});
		else {
			kids.push(r);
			ss.push(n.s[i]);
		}
	});
	if (!kids.length) return null;
	if (kids.length === 1) return kids[0];
	const tot = ss.reduce((a, b) => a + b, 0);
	return { ...n, c: kids, s: ss.map((x) => x / tot) };
}

export function mapLeaf(n: Tree, id: string, fn: (l: Leaf) => Node): Tree {
	if (!n) return n;
	if (n.k === 'leaf') return n.id === id ? fn(n) : n;
	return { ...n, c: n.c.map((c) => mapLeaf(c, id, fn) as Node) };
}

export function leavesOf(n: Tree, out: Leaf[] = []): Leaf[] {
	if (!n) return out;
	if (n.k === 'leaf') out.push(n);
	else n.c.forEach((c) => leavesOf(c, out));
	return out;
}

export function setAt(n: Node, path: number[], fn: (n: Node) => Node): Node {
	if (!path.length) return fn(n);
	if (n.k !== 'split') return n;
	return { ...n, c: n.c.map((c, i) => (i === path[0] ? setAt(c, path.slice(1), fn) : c)) };
}

export function getAt(n: Node, path: number[]): Node {
	return path.reduce<Node>((m, i) => (m.k === 'split' ? m.c[i] : m), n);
}

export interface LeafRect {
	n: Leaf;
	x: number;
	y: number;
	w: number;
	h: number;
}
export interface Divider {
	/** Path of the split this gutter resizes, and the child index before it. */
	path: number[];
	i: number;
	row: boolean;
	/** The split's size along its axis, minus gaps (px per fraction unit). */
	tot: number;
	x: number;
	y: number;
	w: number;
	h: number;
}

/** Absolute rects for every leaf and gutter, from the tree and the desk size. */
export function lay(
	n: Node,
	x: number,
	y: number,
	w: number,
	h: number,
	path: number[],
	out: { leaves: LeafRect[]; divs: Divider[] },
	gap: number
) {
	if (n.k === 'leaf') {
		out.leaves.push({ n, x, y, w, h });
		return;
	}
	const row = n.dir === 'row';
	const tot = (row ? w : h) - gap * (n.c.length - 1);
	let p = row ? x : y;
	n.c.forEach((c, i) => {
		const sz = tot * n.s[i];
		if (row) lay(c, p, y, sz, h, [...path, i], out, gap);
		else lay(c, x, p, w, sz, [...path, i], out, gap);
		p += sz;
		if (i < n.c.length - 1) {
			out.divs.push({
				path,
				i,
				row,
				tot,
				x: row ? p : x,
				y: row ? y : p,
				w: row ? gap : w,
				h: row ? h : gap
			});
			p += gap;
		}
	});
}

/** A stored tree is trusted only if every view appears at most once and
 *  every node is well formed. */
export function validTree(n: unknown): n is Tree {
	try {
		const seen = new Set<string>();
		const ok = (m: Node): boolean =>
			m.k === 'leaf'
				? typeof m.id === 'string' &&
					Array.isArray(m.tabs) &&
					m.tabs.length > 0 &&
					m.tabs.every(
						(v) => (VIEWS as readonly string[]).includes(v) && !seen.has(v) && !!seen.add(v)
					)
				: m.k === 'split' &&
					(m.dir === 'row' || m.dir === 'col') &&
					Array.isArray(m.c) &&
					Array.isArray(m.s) &&
					m.c.length === m.s.length &&
					m.s.every((x) => Number.isFinite(x)) &&
					m.c.every(ok);
		return n === null || ok(n as Node);
	} catch {
		return false;
	}
}

/** Keep generated leaf ids ahead of ids restored from storage. */
export function bumpIds(t: Tree) {
	for (const l of leavesOf(t)) {
		const k = Number(String(l.id).slice(1));
		if (k > LID) LID = k;
	}
}

export type DropZone = 'center' | 'left' | 'right' | 'top' | 'bottom';
export interface Drop {
	id: string;
	zone: DropZone;
	x: number;
	y: number;
	w: number;
	h: number;
	label: string;
}

/** Where a dragged tab would land: the centre of a leaf stacks it, the outer
 *  26% of an edge (below the tab strip, `head` px) splits that side. */
export function hit(rects: LeafRect[], p: { x: number; y: number }, head: number): Drop | null {
	const r = rects.find((r) => p.x >= r.x && p.x <= r.x + r.w && p.y >= r.y && p.y <= r.y + r.h);
	if (!r) return null;
	const fx = (p.x - r.x) / r.w;
	const fy = (p.y - r.y) / r.h;
	let zone: DropZone = 'center';
	if (p.y - r.y > head) {
		const e = (
			[
				['left', fx],
				['right', 1 - fx],
				['top', fy],
				['bottom', 1 - fy]
			] as [DropZone, number][]
		).sort((a, b) => a[1] - b[1])[0];
		if (e[1] < 0.26) zone = e[0];
	}
	const q: Record<DropZone, [number, number, number, number]> = {
		center: [r.x, r.y, r.w, r.h],
		left: [r.x, r.y, r.w / 2, r.h],
		right: [r.x + r.w / 2, r.y, r.w / 2, r.h],
		top: [r.x, r.y, r.w, r.h / 2],
		bottom: [r.x, r.y + r.h / 2, r.w, r.h / 2]
	};
	const [x, y, w, h] = q[zone];
	return {
		id: r.n.id,
		zone,
		x,
		y,
		w,
		h,
		label: zone === 'center' ? t('desk.drop.stack') : t(`desk.drop.split_${zone}`)
	};
}
