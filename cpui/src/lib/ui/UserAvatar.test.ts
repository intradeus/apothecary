import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import UserAvatar from './UserAvatar.svelte';

describe('UserAvatar', () => {
	it('shows the initials with the full name on hover', () => {
		render(UserAvatar, { name: 'Ada Lovelace', initials: 'AL' });
		expect(screen.getByTitle('Ada Lovelace')).toHaveTextContent('AL');
	});

	it('scales the circle and its text together', () => {
		render(UserAvatar, { name: 'Ada', initials: 'A', size: 50 });
		const { style } = screen.getByTitle('Ada');
		expect(style.width).toBe('50px');
		expect(style.height).toBe('50px');
		expect(style.fontSize).toBe('19px');
	});

	it('shows a presence dot only when present', async () => {
		const { container, rerender } = render(UserAvatar, { name: 'Ada', initials: 'A' });
		expect(container.querySelector('.pr')).toBeNull();
		await rerender({ presence: true });
		expect(container.querySelector('.pr')).not.toBeNull();
	});
});
