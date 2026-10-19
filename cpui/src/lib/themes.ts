// The theme registry. A theme is a `:root[data-theme='<id>']` block of
// tokens in src/styles/tokens.css plus one entry here; nothing else in the app knows which
// themes exist.
//
// Adding a theme:
//   1. In src/styles/tokens.css, copy the light block (it overrides every themed token),
//      rename the selector to your id and change the values.
//   2. Add an entry below. `scheme` is the base the browser and third-party
//      widgets (Svelte Flow, native form controls) should render in.
//   3. `npm run check:themes` verifies the block defines every themed token.

export type Scheme = 'dark' | 'light';

export interface ThemeDef {
	/** The data-theme value and the tokens.css selector. */
	id: string;
	label: string;
	/** A text-presentation glyph for compact toggles. */
	icon: string;
	scheme: Scheme;
}

export const THEMES: readonly ThemeDef[] = [
	{ id: 'dark', label: 'Dark', icon: '☾', scheme: 'dark' },
	{ id: 'light', label: 'Light', icon: '☀', scheme: 'light' }
];

/** The `:root` default in tokens.css; used when nothing valid is stored. */
export const DEFAULT_THEME = 'dark';

export const isTheme = (id: string | null | undefined): id is string =>
	!!id && THEMES.some((t) => t.id === id);

export const themeById = (id: string): ThemeDef =>
	THEMES.find((t) => t.id === id) ?? THEMES.find((t) => t.id === DEFAULT_THEME)!;

/** The theme after `id`, wrapping around: what a one-button toggle steps to. */
export const nextTheme = (id: string): ThemeDef => {
	const i = THEMES.findIndex((t) => t.id === id);
	return THEMES[(i + 1) % THEMES.length];
};
