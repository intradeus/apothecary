// Design tokens for the few things CSS cannot style: the sigils canvas and
// the xterm palette. They read the resolved value from app.css rather than
// keeping a copy, so app.css stays the only place a colour is written.

/** A token's resolved value, e.g. token('--az') → "#7fb2ff". Read it after
 *  the theme is applied; re-read on theme change for theme-dependent ones. */
export const token = (name: string, el: Element = document.documentElement): string =>
	getComputedStyle(el).getPropertyValue(name).trim();
