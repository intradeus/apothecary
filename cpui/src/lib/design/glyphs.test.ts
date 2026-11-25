import { describe, expect, it } from 'vitest';
import { GLYPHS } from './glyphs';

const SHAPES = new Set(['path', 'circle', 'rect', 'line', 'polyline', 'polygon', 'ellipse']);

describe('GLYPHS', () => {
	it('holds SVG shapes only, with drawing attributes only', () => {
		for (const [name, shapes] of Object.entries(GLYPHS)) {
			expect(shapes.length, name).toBeGreaterThan(0);
			for (const [tag, attrs] of shapes) {
				expect(SHAPES.has(tag), `${name}: ${tag}`).toBe(true);
				for (const k of Object.keys(attrs)) expect(k, name).not.toMatch(/^on|style|href/i);
			}
		}
	});
});
