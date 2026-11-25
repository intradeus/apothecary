import { render } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { GLYPHS, type GlyphName } from '$lib/design/glyphs';
import Glyph from './Glyph.svelte';

describe('Glyph', () => {
	it.each(Object.keys(GLYPHS) as GlyphName[])('draws %s on the shared 24px grid', (name) => {
		const { container } = render(Glyph, { name });
		const svg = container.querySelector('svg')!;
		expect(svg.getAttribute('viewBox')).toBe('0 0 24 24');
		expect(svg.getAttribute('stroke-width')).toBe('2');
		expect(svg.children.length).toBeGreaterThan(0);
		// Real SVG elements, or the browser draws nothing.
		for (const shape of svg.children) expect(shape.namespaceURI).toBe('http://www.w3.org/2000/svg');
		expect(svg).toHaveAttribute('aria-hidden', 'true');
	});

	it('takes its size, the text’s by default', () => {
		const { container } = render(Glyph, { name: 'inbox' });
		expect(container.querySelector('svg')).toHaveAttribute('width', '1em');
		const big = render(Glyph, { name: 'inbox', size: '20px' });
		expect(big.container.querySelector('svg')).toHaveAttribute('height', '20px');
	});
});
