// The desk under jsdom: no layout, so the size and the geometry tokens are
// stated here, and a stand-in for the page's views that records how each one
// was mounted and where it is drawn.
import { screen, within } from '@testing-library/svelte';
import { createRawSnippet, type Snippet } from 'svelte';
import { vi } from 'vitest';
import type { ViewPlace } from '$lib/desk/Desk.svelte';
import { DeskState } from '$lib/desk/desk.svelte';
import { leaf, split, type LayoutId, type Tree, type ViewId } from '$lib/desk/tree';

export const DESK_KEY = 'apothecary.desk.v1';
export const GAP = 8;
export const HEAD = 38;
export const MIN = 120;

/** Gives the desk a size and the tokens its geometry comes from. jsdom's
 *  viewport is 1024×768, so a desk larger than that shows a fullscreen window
 *  apart from a laid-out one. */
export function sizeDesk(w = 1200, h = 800) {
	vi.spyOn(Element.prototype, 'clientWidth', 'get').mockReturnValue(w);
	vi.spyOn(Element.prototype, 'clientHeight', 'get').mockReturnValue(h);
	const root = document.documentElement.style;
	root.setProperty('--desk-gap', `${GAP}px`);
	root.setProperty('--leaf-head', `${HEAD}px`);
	root.setProperty('--pane-min', `${MIN}px`);
}

/** A desk holding this tree, as if the person had arranged it. */
export function aDesk(tree: Tree, preset: LayoutId = 'custom') {
	const desk = new DeskState();
	desk.tree = tree;
	desk.preset = preset;
	return desk;
}

/** Two windows side by side, half and half: session/logs/terminal, then flow. */
export function twoWindows() {
	const a = leaf(['session', 'logs', 'terminal']);
	const b = leaf(['flow']);
	return { a, b, desk: aDesk(split('row', [0.5, 0.5], [a, b])) };
}

export interface Probe {
	view: Snippet<[ViewId, ViewPlace]>;
	/** Where the view is drawn right now. */
	place(v: ViewId): ViewPlace;
	/** How many times it mounted, and how many it is gone again. */
	mounts(v: ViewId): number;
	unmounts(v: ViewId): number;
}

/** The desk's `view` snippet: `<div data-testid="view-session">`. */
export function aProbe(): Probe {
	const places = new Map<ViewId, () => ViewPlace>();
	const mounts = new Map<ViewId, number>();
	const unmounts = new Map<ViewId, number>();
	const view = createRawSnippet<[ViewId, ViewPlace]>((v, place) => ({
		render: () => `<div data-testid="view-${v()}">${v()}</div>`,
		setup: () => {
			const id = v();
			places.set(id, place);
			mounts.set(id, (mounts.get(id) ?? 0) + 1);
			return () => unmounts.set(id, (unmounts.get(id) ?? 0) + 1);
		}
	}));
	return {
		view,
		place: (v) => places.get(v)!(),
		mounts: (v) => mounts.get(v) ?? 0,
		unmounts: (v) => unmounts.get(v) ?? 0
	};
}

/** Each window on screen, as the labels of its tabs. */
export const windows = () =>
	screen.queryAllByRole('tablist').map((list) =>
		within(list)
			.getAllByRole('tab')
			.map((tab) => tab.textContent!.replace(/[^\p{L}]/gu, ''))
	);

/** A tab by its label, wherever it is. */
export const tab = (label: string) => screen.getByRole('tab', { name: new RegExp(`^${label}\\b`) });

/** The window a tab is in. */
export const windowOf = (label: string) => tab(label).closest<HTMLElement>('section')!;
