import { describe, expect, it } from 'vitest';
import { I } from './icons';

const TEXT = '︎';

describe('the icon vocabulary', () => {
	it('gives every name a glyph', () => {
		expect(Object.entries(I).filter(([, glyph]) => !glyph)).toEqual([]);
	});

	it.each([
		['desk', I.desk],
		['inbox', I.inbox],
		['runs', I.runs],
		['tickets', I.tickets],
		['system', I.system],
		['play', I.play],
		['workshop', I.workshop],
		['dark', I.dark],
		['light', I.light],
		['channels', I.channels],
		['members', I.members],
		['reaper', I.reaper]
	])('forces %s, a glyph with an emoji form, to text', (_, glyph) => {
		expect(glyph.endsWith(TEXT)).toBe(true);
	});

	it('uses no emoji presentation anywhere', () => {
		for (const glyph of Object.values(I)) expect(glyph).not.toContain('️');
	});
});
