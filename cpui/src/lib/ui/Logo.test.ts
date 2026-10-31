import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import Logo from './Logo.svelte';

const logo = () => screen.getByRole('img', { name: 'Apothecary' });

describe('Logo', () => {
	it('is the named mark at 36px by default', () => {
		render(Logo);
		expect(logo()).toHaveAttribute('width', '36');
		expect(logo()).toHaveAttribute('height', '36');
	});

	it('scales its corners with its size', () => {
		render(Logo, { size: 100 });
		expect(logo()).toHaveAttribute('width', '100');
		expect(logo().style.borderRadius).toBe('24px');
	});
});
