// The theme registry. A theme is a `[data-theme='<id>']` block in
// src/styles/themes.css plus one entry here; nothing else in the app knows
// which themes exist. Its colours are never repeated here: a swatch previews
// a theme by carrying data-theme itself (ThemePicker).
//
// Adding a theme:
//   1. In src/styles/themes.css, copy a block (each one states every themed
//      token), rename the selector to your id and change the values.
//   2. Add an entry below and a `theme.<id>` label in i18n. `scheme` is the
//      base the browser and third-party widgets (Svelte Flow, native form
//      controls) should render in.
//   3. `npm run check:themes` verifies the block defines every themed token.

import { t } from '$lib/i18n/index.svelte';
import type { MessageKey } from '$lib/i18n/locales/en';

export type Scheme = 'dark' | 'light';

export interface ThemeDef {
	/** The data-theme value and the themes.css selector. */
	id: string;
	label: string;
	scheme: Scheme;
}

const theme = (id: string, scheme: Scheme): ThemeDef => ({
	id,
	get label() {
		return t(`theme.${id}` as MessageKey);
	},
	scheme
});

export const THEMES: readonly ThemeDef[] = [
	theme('ember-sky', 'dark'),
	theme('solarized', 'dark'),
	theme('forest', 'dark'),
	theme('brown', 'dark'),
	theme('light', 'light'),
	theme('dark', 'dark'),
	theme('pink', 'dark')
];

/** The `:root` default in themes.css; used when nothing valid is stored. */
export const DEFAULT_THEME = 'ember-sky';

export const isTheme = (id: string | null | undefined): id is string =>
	!!id && THEMES.some((th) => th.id === id);

export const themeById = (id: string): ThemeDef =>
	THEMES.find((th) => th.id === id) ?? THEMES.find((th) => th.id === DEFAULT_THEME)!;
