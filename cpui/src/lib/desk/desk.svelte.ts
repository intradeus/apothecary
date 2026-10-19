// The desk's state: the tree, which preset it came from, the focused and
// maximised leaf, and an in-flight tab drag. Persisted per browser
// ({tree, preset}); the server can take this over later without the
// component noticing.
//
// Every discrete layout change (split, move, hide, preset, maximise) also
// sets `motion`, which tells the desk to spring the windows from where they
// are to their new rects; a gutter drag tracks 1:1 with motion off.

import { t } from '$lib/i18n/index.svelte';
import { ms } from '$lib/ui/motion';

import {
	PRESETS,
	bumpIds,
	isPreset,
	leaf,
	leavesOf,
	mapLeaf,
	norm,
	split,
	validTree,
	type Drop,
	type LayoutId,
	type LeafRect,
	type PresetId,
	type Tree,
	type ViewId
} from './tree';

const KEY = 'apothecary.desk.v1';

function load(): { tree: Tree; preset: LayoutId } {
	try {
		const saved = JSON.parse(localStorage.getItem(KEY) || 'null') as {
			tree?: unknown;
			preset?: string;
		} | null;
		if (saved && saved.tree !== undefined && validTree(saved.tree)) {
			bumpIds(saved.tree);
			return {
				tree: norm(saved.tree),
				preset:
					saved.preset && (isPreset(saved.preset) || saved.preset === 'custom')
						? (saved.preset as LayoutId)
						: 'custom'
			};
		}
	} catch {
		/* storage blocked or corrupt: start from the default */
	}
	return { tree: PRESETS.split.make(), preset: 'split' };
}

/** A tab being dragged: the view, the pointer (desk-relative), where on
 *  the tab it was grabbed, and the tab's home spot to fly back to. */
export interface TabDrag {
	v: ViewId;
	x: number;
	y: number;
	ox: number;
	oy: number;
	home: { x: number; y: number };
	/** Released over nothing: flying home before it disappears. */
	back?: boolean;
}

export type DeskMotion = '' | 'spring' | 'bounce';

export class DeskState {
	tree = $state<Tree>(null);
	preset = $state<LayoutId>('split');
	focus = $state<string | null>(null);
	max = $state<string | null>(null);
	drag = $state<TabDrag | null>(null);
	drop = $state<Drop | null>(null);
	/** How window rects move right now: spring to a new layout, bounce back
	 *  from a released gutter, or '' (track 1:1). */
	motion = $state<DeskMotion>('');
	#settle: ReturnType<typeof setTimeout> | undefined;
	/** The Layouts popover (opened from the header or the empty bench). */
	menuOpen = $state(false);
	/** The leaf rects of the last layout pass (for hit-testing and showView). */
	rects: LeafRect[] = [];

	constructor() {
		const s = load();
		this.tree = s.tree;
		this.preset = s.preset;
	}

	readonly visible = $derived(new Set(leavesOf(this.tree).flatMap((l) => l.tabs)));
	readonly label = $derived(
		isPreset(this.preset) ? PRESETS[this.preset].label : t('desk.preset.custom')
	);

	/** A structural change (split, move, hide, preset): restores a maximised
	 *  window, since the layout it was hiding just changed. */
	set(t: Tree, preset: LayoutId = 'custom') {
		this.max = null;
		this.animate();
		this.#write(t, preset);
	}

	/** Spring the windows to wherever the next layout puts them. Interrupting
	 *  one move with another retargets from the live rects (CSS transitions
	 *  start from the current value). */
	animate(kind: DeskMotion = 'spring') {
		this.motion = kind;
		clearTimeout(this.#settle);
		const d = kind === 'bounce' ? ms('--dur-bounce', 600) : ms('--dur-spring', 450);
		this.#settle = setTimeout(() => (this.motion = ''), d + 50);
	}

	/** A gutter drag in progress: 1:1, not saved until it is released. */
	resizeLive(t: Tree) {
		clearTimeout(this.#settle);
		this.motion = '';
		this.tree = norm(t);
	}

	/** The gutter was released; `snap` when it sprang back from a limit. */
	resizeDone(t: Tree, snap: boolean) {
		if (snap) this.animate('bounce');
		this.#write(t, 'custom');
	}

	#write(t: Tree, preset: LayoutId) {
		this.tree = norm(t);
		this.preset = preset;
		try {
			localStorage.setItem(KEY, JSON.stringify({ tree: this.tree, preset }));
		} catch {
			/* lasts this tab only */
		}
	}

	applyPreset(id: PresetId) {
		this.set(PRESETS[id].make(), id);
	}

	leafOf(v: ViewId) {
		return leavesOf(this.tree).find((l) => l.tabs.includes(v));
	}

	/** Make v the active tab of its leaf, keeping the preset (not a rearrange)
	 *  and a maximised window maximised: switching tabs is not a restore. */
	activate(id: string, v: ViewId) {
		this.focus = id;
		this.#write(
			mapLeaf(this.tree, id, (n) => ({ ...n, act: v })),
			this.preset
		);
	}

	closeView(v: ViewId) {
		const l = this.leafOf(v);
		if (l)
			this.set(mapLeaf(this.tree, l.id, (n) => ({ ...n, tabs: n.tabs.filter((x) => x !== v) })));
	}

	closeLeaf(id: string) {
		this.set(mapLeaf(this.tree, id, (n) => ({ ...n, tabs: [] })));
	}

	/** Bring a hidden view back, into the focused leaf (else the biggest). */
	showView(v: ViewId) {
		const t = this.tree;
		if (!t) {
			const nl = leaf([v]);
			this.focus = nl.id;
			return this.set(nl);
		}
		const all = leavesOf(t);
		const f =
			all.find((l) => l.id === this.focus) ??
			[...this.rects].sort((a, b) => b.w * b.h - a.w * a.h)[0]?.n ??
			all[0];
		this.set(mapLeaf(t, f.id, (n) => ({ ...n, tabs: [...n.tabs, v], act: v })));
	}

	toggleView(v: ViewId) {
		if (this.visible.has(v)) this.closeView(v);
		else this.showView(v);
	}

	/** Focus a view wherever it is; show it when hidden. */
	reveal(v: ViewId) {
		const l = this.leafOf(v);
		if (!l) return this.showView(v);
		if (this.max && this.max !== l.id) {
			this.animate();
			this.max = null;
		}
		if (l.act !== v) this.activate(l.id, v);
		else this.focus = l.id;
	}

	/** Move a tab: onto a leaf's centre (stack) or one of its edges (split). */
	moveView(v: ViewId, from: string, d: Pick<Drop, 'id' | 'zone'>) {
		const src = leavesOf(this.tree).find((l) => l.id === from);
		if (!src) return;
		if (d.id === from && (d.zone === 'center' || src.tabs.length === 1))
			return this.activate(from, v);
		let t = mapLeaf(this.tree, from, (n) => ({ ...n, tabs: n.tabs.filter((x) => x !== v) }));
		if (d.zone === 'center') {
			t = mapLeaf(t, d.id, (n) => ({ ...n, tabs: [...n.tabs, v], act: v }));
			this.focus = d.id;
		} else {
			const nl = leaf([v]);
			const row = d.zone === 'left' || d.zone === 'right';
			const first = d.zone === 'left' || d.zone === 'top';
			t = mapLeaf(t, d.id, (n) =>
				split(row ? 'row' : 'col', [0.5, 0.5], first ? [nl, n] : [n, nl])
			);
			this.focus = nl.id;
		}
		this.set(t);
	}

	toggleMax(id: string) {
		this.animate();
		this.max = this.max === id ? null : id;
	}
}
