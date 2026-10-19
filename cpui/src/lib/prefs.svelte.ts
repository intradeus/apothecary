// Per-browser UI preferences: theme, label flavor, sidebar state.
//
// These are conveniences, not records: they live in localStorage and every
// read tolerates its absence (private windows, blocked storage).

export type Theme = 'dark' | 'light';
/** 'lore' renames a handful of nouns (Guild, Workshop, Batch…); 'plain' keeps
 *  them technical. Status vocabulary is never renamed in either. */
export type Flavor = 'lore' | 'plain';

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

// ?theme=light|dark wins for this page load (screenshots, support links)
// without overwriting the saved preference.
const urlTheme = () => {
	try {
		return new URLSearchParams(location.search).get('theme');
	} catch {
		return null;
	}
};

class Prefs {
	theme = $state<Theme>((urlTheme() ?? read('apothecary.theme')) === 'light' ? 'light' : 'dark');
	flavor = $state<Flavor>(read('apothecary.flavor') === 'plain' ? 'plain' : 'lore');
	collapsed = $state(read('apothecary.sidebar') === 'rail');

	setTheme(t: Theme) {
		this.theme = t;
		write('apothecary.theme', t);
		document.documentElement.dataset.theme = t;
	}
	toggleTheme() {
		this.setTheme(this.theme === 'dark' ? 'light' : 'dark');
	}
	setFlavor(f: Flavor) {
		this.flavor = f;
		write('apothecary.flavor', f);
	}
	toggleCollapsed() {
		this.collapsed = !this.collapsed;
		write('apothecary.sidebar', this.collapsed ? 'rail' : 'full');
	}
}

export const prefs = new Prefs();
