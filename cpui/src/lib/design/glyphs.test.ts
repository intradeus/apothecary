import { describe, expect, it } from 'vitest';
import { GLYPHS } from './glyphs';

describe('GLYPHS', () => {
	it('holds shapes only: no <svg> of their own, no script, no style', () => {
		for (const [name, markup] of Object.entries(GLYPHS)) {
			expect(markup, name).toMatch(/^<(path|circle|rect|line|polyline|polygon|ellipse)\b/);
			expect(markup, name).not.toMatch(/<svg|<script|style=|on\w+=/i);
		}
	});
});
