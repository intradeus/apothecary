import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const THEME_KEY = 'apothecary.theme.v2';
const SIDEBAR_KEY = 'apothecary.sidebar';

/** A fresh Prefs: it reads the browser once, as it's built. */
const load = async () => {
	vi.resetModules();
	return (await import('./prefs.svelte')).prefs;
};

const blockStorage = () => {
	vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
		throw new DOMException('blocked', 'SecurityError');
	});
	vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
		throw new DOMException('blocked', 'SecurityError');
	});
};

beforeEach(() => {
	localStorage.clear();
	history.replaceState(null, '', '/');
	delete document.documentElement.dataset.theme;
});
afterEach(() => {
	delete (document as { startViewTransition?: unknown }).startViewTransition;
});

describe('prefs on load', () => {
	it('starts from the default theme and a full sidebar', async () => {
		const prefs = await load();
		expect(prefs.theme).toBe('ember-sky');
		expect(prefs.collapsed).toBe(false);
	});

	it('restores the saved theme and the rail', async () => {
		localStorage.setItem(THEME_KEY, 'forest');
		localStorage.setItem(SIDEBAR_KEY, 'rail');
		const prefs = await load();
		expect(prefs.theme).toBe('forest');
		expect(prefs.themeDef.label).toBe('Forest');
		expect(prefs.collapsed).toBe(true);
	});

	it('ignores a saved theme that no longer exists', async () => {
		localStorage.setItem(THEME_KEY, 'retired');
		expect((await load()).theme).toBe('ember-sky');
	});

	it('lets ?theme= win for this load without touching the saved one', async () => {
		localStorage.setItem(THEME_KEY, 'forest');
		history.replaceState(null, '', '/?theme=light');
		const prefs = await load();
		expect(prefs.theme).toBe('light');
		expect(localStorage.getItem(THEME_KEY)).toBe('forest');
	});

	it('falls back to the saved theme past an unknown ?theme=', async () => {
		localStorage.setItem(THEME_KEY, 'forest');
		history.replaceState(null, '', '/?theme=nope');
		expect((await load()).theme).toBe('forest');
	});

	it('works with storage blocked', async () => {
		blockStorage();
		const prefs = await load();
		expect(prefs.theme).toBe('ember-sky');
		expect(prefs.collapsed).toBe(false);
	});
});

describe('prefs.setTheme', () => {
	it('applies a theme to the page and saves it', async () => {
		const prefs = await load();
		prefs.setTheme('solarized');
		expect(prefs.theme).toBe('solarized');
		expect(document.documentElement.dataset.theme).toBe('solarized');
		expect(localStorage.getItem(THEME_KEY)).toBe('solarized');
	});

	it('refuses an unknown theme', async () => {
		const prefs = await load();
		prefs.setTheme('nope');
		expect(prefs.theme).toBe('ember-sky');
		expect(document.documentElement.dataset.theme).toBeUndefined();
		expect(localStorage.getItem(THEME_KEY)).toBeNull();
	});

	it('does nothing for the theme already on', async () => {
		const prefs = await load();
		const transition = vi.fn((cb: () => void) => cb());
		Object.assign(document, { startViewTransition: transition });
		prefs.setTheme('ember-sky');
		expect(transition).not.toHaveBeenCalled();
	});

	it('cross-fades where the browser can', async () => {
		const prefs = await load();
		let swap = () => {};
		const transition = vi.fn((cb: () => void) => (swap = cb));
		Object.assign(document, { startViewTransition: transition });

		prefs.setTheme('forest');
		expect(transition).toHaveBeenCalledOnce();
		expect(prefs.theme).toBe('ember-sky');

		swap();
		expect(prefs.theme).toBe('forest');
	});

	it('jumps straight there under reduced motion', async () => {
		const prefs = await load();
		const transition = vi.fn((cb: () => void) => cb());
		Object.assign(document, { startViewTransition: transition });
		vi.spyOn(globalThis, 'matchMedia').mockReturnValue({ matches: true } as MediaQueryList);

		prefs.setTheme('forest');

		expect(transition).not.toHaveBeenCalled();
		expect(prefs.theme).toBe('forest');
	});

	it('keeps the choice for this tab when storage is blocked', async () => {
		const prefs = await load();
		blockStorage();
		prefs.setTheme('forest');
		expect(prefs.theme).toBe('forest');
		expect(document.documentElement.dataset.theme).toBe('forest');
	});
});

describe('prefs.toggleCollapsed', () => {
	it('flips the sidebar and saves which way', async () => {
		const prefs = await load();
		prefs.toggleCollapsed();
		expect(prefs.collapsed).toBe(true);
		expect(localStorage.getItem(SIDEBAR_KEY)).toBe('rail');

		prefs.toggleCollapsed();
		expect(prefs.collapsed).toBe(false);
		expect(localStorage.getItem(SIDEBAR_KEY)).toBe('full');
	});
});
