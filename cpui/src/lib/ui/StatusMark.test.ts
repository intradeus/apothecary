import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { I } from '$lib/design/icons';
import StatusMark, { type MarkTone } from './StatusMark.svelte';

describe('StatusMark', () => {
	it.each<[MarkTone, string]>([
		['ok', I.ok],
		['bad', I.bad],
		['warn', I.warn],
		['muted', I.none],
		['info', ''],
		['pending', '']
	])('draws the %s glyph', (tone, glyph) => {
		render(StatusMark, { tone });
		expect(screen.getByRole('img', { name: tone }).textContent?.trim()).toBe(glyph);
	});

	it('pulses a dot while live, whatever glyph it was given', () => {
		render(StatusMark, { tone: 'live', glyph: 'x' });
		const mark = screen.getByRole('img', { name: 'live' });
		expect(mark.querySelector('.live-dot.pulse')).not.toBeNull();
		expect(mark).not.toHaveTextContent('x');
	});

	it('takes a glyph over the tone default', () => {
		render(StatusMark, { tone: 'ok', glyph: '7' });
		expect(screen.getByRole('img', { name: 'ok' })).toHaveTextContent('7');
	});

	it('is named by its title when it has one', () => {
		render(StatusMark, { tone: 'bad', title: 'envcheck failed' });
		const mark = screen.getByRole('img', { name: 'envcheck failed' });
		expect(mark).toHaveAttribute('title', 'envcheck failed');
	});
});
