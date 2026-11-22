import { describe, expect, it } from 'vitest';
import { I, SPARKLE_PATH } from './icons';

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
		['members', I.members]
	])('forces %s, a glyph with an emoji form, to text', (_, glyph) => {
		expect(glyph.endsWith(TEXT)).toBe(true);
	});

	it('uses no emoji presentation anywhere', () => {
		for (const glyph of Object.values(I)) expect(glyph).not.toContain('️');
	});
});

describe('the sparkle', () => {
	it('is two closed four-point stars inside its 24×24 box', () => {
		const stars = SPARKLE_PATH.split('M').filter(Boolean);
		expect(stars).toHaveLength(2);
		for (const s of stars) {
			expect(s.endsWith('Z')).toBe(true);
			expect(s.match(/C/g)).toHaveLength(4);
		}
		const coords = SPARKLE_PATH.match(/-?\d+(\.\d+)?/g)!.map(Number);
		expect(coords.every((n) => n >= 0 && n <= 24)).toBe(true);
	});
});
