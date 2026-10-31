import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import StableText from './StableText.svelte';

describe('StableText', () => {
	it('shows the text', () => {
		render(StableText, { text: '3s ago' });
		expect(screen.getByText('3s ago')).toBeVisible();
	});

	it('lays out each reserved string for sizing, hidden from assistive tech', () => {
		const { container } = render(StableText, { text: 'now', reserve: ['just now', '88s ago'] });
		const hidden = [...container.querySelectorAll('[aria-hidden="true"]')];
		expect(hidden.map((n) => n.textContent)).toEqual(['just now', '88s ago']);
	});

	it('keeps the reserve when the text ticks over', async () => {
		const { container, rerender } = render(StableText, { text: 'now', reserve: ['88s ago'] });
		await rerender({ text: '3s ago' });
		expect(screen.getByText('3s ago')).toBeVisible();
		expect(container.querySelector('[aria-hidden="true"]')).toHaveTextContent('88s ago');
	});
});
