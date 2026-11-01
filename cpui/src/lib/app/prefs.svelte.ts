// Per-browser UI preferences: theme, sidebar state, file lists as a list or a tree.
//
// These are conveniences, not records: they live in localStorage and every
// read tolerates its absence (private windows, blocked storage).

import { DEFAULT_THEME, isTheme, themeById, type ThemeDef } from '$lib/design/themes';

const read = (k: string): string | null => {
	try {
		return localStorage.getItem(k);
	} catch {
		return null;
	}
};
const write = (k: string, v: string) => {
	try {
		localStorage.setItem(k, v);
	} catch {
		/* storage blocked: the preference lasts this tab only */
	}
};

// ?theme=<id> wins for this page load (screenshots, support links) without
// overwriting the saved preference.
const urlTheme = () => {
	try {
		return new URLSearchParams(location.search).get('theme');
	} catch {
		return null;
	}
};

/** How a pane lists files: flat paths, or folders you can fold. */
export type FileLayout = 'list' | 'tree';

class Prefs {
	/** A registered theme id (see $lib/design/themes). */
	theme = $state<string>(
		[urlTheme(), read('apothecary.theme.v2')].find((t) => isTheme(t)) ?? DEFAULT_THEME
	);
	collapsed = $state(read('apothecary.sidebar') === 'rail');
	fileLayout = $state<FileLayout>(read('apothecary.files.layout') === 'list' ? 'list' : 'tree');

	/** The current theme's definition: label, icon, base scheme. */
	readonly themeDef: ThemeDef = $derived(themeById(this.theme));

	/** Switch theme. Where the browser supports view transitions the whole
	 *  page cross-fades (--dur-theme) instead of jumping in brightness. */
	setTheme(id: string) {
		if (!isTheme(id) || id === this.theme) return;
		const apply = () => {
			this.theme = id;
			write('apothecary.theme.v2', id);
			document.documentElement.dataset.theme = id;
		};
		const doc = document as Document & { startViewTransition?: (cb: () => void) => unknown };
		const still = matchMedia('(prefers-reduced-motion: reduce)').matches;
		if (doc.startViewTransition && !still) doc.startViewTransition(apply);
		else apply();
	}
	toggleCollapsed() {
		this.collapsed = !this.collapsed;
		write('apothecary.sidebar', this.collapsed ? 'rail' : 'full');
	}
	setFileLayout(layout: FileLayout) {
		this.fileLayout = layout;
		write('apothecary.files.layout', layout);
	}
}

export const prefs = new Prefs();
