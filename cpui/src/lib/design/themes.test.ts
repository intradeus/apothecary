import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { en } from '$lib/i18n/locales/en';
import { DEFAULT_THEME, isTheme, themeById, THEMES } from './themes';

const css = readFileSync(resolve(import.meta.dirname, '../../styles/themes.css'), 'utf8').replace(
	/\/\*[\s\S]*?\*\//g,
	''
);

const IDS = ['ember-sky', 'solarized', 'forest', 'brown', 'light', 'dark', 'pink'];

describe('the theme registry', () => {
	it('holds the themes the app offers, in picker order', () => {
		expect(THEMES.map((th) => th.id)).toEqual(IDS);
	});

	it('starts on a registered theme', () => {
		expect(DEFAULT_THEME).toBe('ember-sky');
		expect(isTheme(DEFAULT_THEME)).toBe(true);
	});

	it.each(IDS)('backs %s with a block in themes.css', (id) => {
		expect(css).toContain(`[data-theme='${id}']`);
	});

	it('registers every block in themes.css', () => {
		const blocks = [...css.matchAll(/\[data-theme='([\w-]+)'\]/g)].map((m) => m[1]);
		expect(new Set(blocks)).toEqual(new Set(IDS));
	});

	it.each(THEMES.map((th) => [th.id, th] as const))('labels %s from the catalogue', (id, th) => {
		expect(th.label).toBe(en[`theme.${id}` as keyof typeof en]);
		expect(th.label).not.toBe(`theme.${id}`);
	});

	it('marks only the light theme as a light base for the browser', () => {
		expect(THEMES.filter((th) => th.scheme === 'light').map((th) => th.id)).toEqual(['light']);
		expect(THEMES.every((th) => th.scheme === 'dark' || th.scheme === 'light')).toBe(true);
	});

	it('states `color-scheme` in step with its scheme', () => {
		const scheme = [
			...css.matchAll(/\[data-theme='([\w-]+)'\][^{]*\{[^}]*?color-scheme:\s*(\w+)/g)
		];
		for (const [, id, value] of scheme) expect(themeById(id).scheme).toBe(value);
	});
});

describe('isTheme', () => {
	it.each(IDS)('accepts %s', (id) => {
		expect(isTheme(id)).toBe(true);
	});

	it.each([[null], [undefined], [''], ['neon'], ['Ember-Sky']])('refuses %j', (id) => {
		expect(isTheme(id)).toBe(false);
	});
});

describe('themeById', () => {
	it('finds a theme', () => {
		expect(themeById('forest').id).toBe('forest');
	});

	it('falls back on the default for an id it does not know', () => {
		expect(themeById('neon').id).toBe(DEFAULT_THEME);
	});
});
