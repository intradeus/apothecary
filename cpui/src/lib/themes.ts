// The theme registry. A theme is a `:root[data-theme='<id>']` block of
// tokens in src/styles/tokens.css plus one entry here; nothing else in the app knows which
// themes exist.
//
// Adding a theme:
//   1. In src/styles/tokens.css, copy a block (each one states every themed token),
//      rename the selector to your id and change the values.
//   2. Add an entry below. `scheme` is the base the browser and third-party
//      widgets (Svelte Flow, native form controls) should render in.
//   3. `npm run check:themes` verifies the block defines every themed token.

import { t } from '$lib/i18n/index.svelte';
import type { MessageKey } from '$lib/i18n/locales/en';

export type Scheme = 'dark' | 'light';

export interface ThemeDef {
	/** The data-theme value and the tokens.css selector. */
	id: string;
	label: string;
	/** A text-presentation glyph for compact toggles. */
	icon: string;
	scheme: Scheme;
	/** The theme's two story hues, for the picker's swatch. */
	pair: readonly [string, string];
}

const theme = (id: string, icon: string, scheme: Scheme, pair: [string, string]): ThemeDef => ({
	id,
	get label() {
		return t(`theme.${id}` as MessageKey);
	},
	icon,
	scheme,
	pair
});

export const THEMES: readonly ThemeDef[] = [
	theme('ember-sky', '☾', 'dark', ['#ff6a2b', '#8fb8ea']),
	theme('solarized', '☾', 'dark', ['#dd5f1f', '#35b5aa']),
	theme('forest', '☾', 'dark', ['#e0a93a', '#a7c4a0']),
	theme('brown', '☾', 'dark', ['#e8702a', '#e8d9bd']),
	theme('light', '☀', 'light', ['#e0601f', '#8fb8ea']),
	theme('dark', '☾', 'dark', ['#ff6a2b', '#c0c3ca']),
	theme('pink', '☾', 'dark', ['#ff7a5c', '#f0a0bc'])
];

/** The `:root` default in tokens.css; used when nothing valid is stored. */
export const DEFAULT_THEME = 'ember-sky';

export const isTheme = (id: string | null | undefined): id is string =>
	!!id && THEMES.some((th) => th.id === id);

export const themeById = (id: string): ThemeDef =>
	THEMES.find((th) => th.id === id) ?? THEMES.find((th) => th.id === DEFAULT_THEME)!;
