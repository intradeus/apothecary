import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import {
	EDGE_HUE,
	GIT_HUE,
	hueOf,
	STAGE_HUE,
	STAGE_WORD,
	TONE_HUE,
	TOOL_HUE,
	type Hue
} from './hues';

const HUES: Hue[] = [
	'running',
	'attention',
	'warning',
	'success',
	'danger',
	'accent',
	'accent-2',
	'muted',
	'brand'
];

const styles = (name: string) =>
	readFileSync(resolve(import.meta.dirname, `../../styles/${name}.css`), 'utf8');
const base = styles('base');
const components = styles('components');
const themes = styles('themes');

describe('the hue family', () => {
	it.each(HUES)('gives %s its classes', (hue) => {
		expect(base).toContain(`.hue-${hue} {`);
		expect(components).toContain(`.tint-${hue} {`);
		expect(components).toContain(`.tone-${hue} {`);
	});

	it.each(HUES)('gives %s its tokens', (hue) => {
		expect(themes).toMatch(new RegExp(`--${hue}:`));
		expect(themes).toMatch(new RegExp(`--${hue}-bg:`));
	});

	it.each([
		['STAGE_HUE', STAGE_HUE],
		['EDGE_HUE', EDGE_HUE],
		['TOOL_HUE', TOOL_HUE],
		['GIT_HUE', GIT_HUE],
		['TONE_HUE', TONE_HUE]
	])('keeps %s to the hues there are', (_, table) => {
		for (const hue of Object.values(table)) expect(HUES).toContain(hue);
	});
});

describe('the domain’s hues', () => {
	it.each<[string, Hue]>([
		['agent', 'running'],
		['command', 'success'],
		['human_gate', 'warning'],
		['gate', 'warning'],
		['action', 'accent'],
		['wait', 'accent-2'],
		['service', 'success'],
		['sequence', 'brand']
	])('colours a %s stage %s', (type, hue) => {
		expect(STAGE_HUE[type]).toBe(hue);
	});

	it.each<[string, Hue]>([
		['success', 'success'],
		['on_fail', 'danger'],
		['on_timeout', 'accent-2'],
		['on_reject', 'warning']
	])('colours a %s edge %s', (kind, hue) => {
		expect(EDGE_HUE[kind]).toBe(hue);
	});

	it.each<[keyof typeof TONE_HUE, Hue]>([
		['ok', 'success'],
		['bad', 'danger'],
		['warn', 'attention'],
		['info', 'running'],
		['muted', 'muted']
	])('colours the tone %s %s', (tone, hue) => {
		expect(TONE_HUE[tone]).toBe(hue);
	});

	it.each(['A', 'M', 'D', 'R', 'C', '?'])('colours the git status %s', (status) => {
		expect(GIT_HUE[status]).toBeDefined();
	});

	it('words a human gate as a gate', () => {
		expect(STAGE_WORD.human_gate).toBe('gate');
	});
});

describe('hueOf', () => {
	it('finds a key', () => {
		expect(hueOf(TOOL_HUE, 'edit')).toBe('warning');
	});

	it('falls back on muted, or what it is told', () => {
		expect(hueOf(TOOL_HUE, 'unknown')).toBe('muted');
		expect(hueOf(TOOL_HUE, 'unknown', 'brand')).toBe('brand');
	});
});
