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

describe('the Dark theme reads', () => {
	const body = css.match(/\[data-theme='dark'\]\s*\{([^}]*)\}/)?.[1] ?? '';
	const tokens = new Map(
		[...body.matchAll(/(--[\w-]+):\s*([^;]+);/g)].map((m) => [m[1], m[2].trim()])
	);
	const rgb = (name: string): number[] => {
		const value = tokens.get(name) ?? '';
		if (!/^#[0-9a-f]{6}$/i.test(value)) throw new Error(`${name} is not a 6-digit hex: ${value}`);
		const n = parseInt(value.slice(1), 16);
		return [n >> 16, (n >> 8) & 255, n & 255];
	};
	const luminance = (c: number[]) => {
		const [r, g, b] = c.map((v) => {
			const s = v / 255;
			return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
		});
		return 0.2126 * r + 0.7152 * g + 0.0722 * b;
	};
	const contrast = (a: number[], b: number[]) => {
		const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
		return (hi + 0.05) / (lo + 0.05);
	};

	it.each(
		['--text', '--text-muted', '--text-faint'].flatMap((text) =>
			['--frame', '--island-bg'].map((bg) => [text, bg])
		)
	)('sets %s over %s at 4.5:1 or more', (text, bg) => {
		expect(contrast(rgb(text), rgb(bg))).toBeGreaterThanOrEqual(4.5);
	});

	it.each([
		['text', 1],
		['muted text', 0.74],
		['faint text', 0.62]
	])('sets the ping card %s at 4.5:1 or more', (_, alpha) => {
		const fill = rgb('--attention-fill');
		// what color-mix(in srgb, ink p%, transparent) shows over the fill
		const ink = rgb('--ink').map((c, i) => c * alpha + fill[i] * (1 - alpha));
		expect(contrast(ink, fill)).toBeGreaterThanOrEqual(4.5);
	});

	it('labels the ping card button at 4.5:1 or more', () => {
		expect(contrast(rgb('--on-ink'), rgb('--ink'))).toBeGreaterThanOrEqual(4.5);
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
