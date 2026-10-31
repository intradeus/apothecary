// Design tokens for the few things CSS cannot style, like the xterm palette.
// They read the resolved value rather than keeping a copy, so
// src/styles/themes.css stays the only place a colour is written.

/** A token's resolved value, e.g. token('--term-text') → "#e6edf6". Read it after
 *  the theme is applied; re-read on theme change for theme-dependent ones. */
export const token = (name: string, el: Element = document.documentElement): string =>
	getComputedStyle(el).getPropertyValue(name).trim();
