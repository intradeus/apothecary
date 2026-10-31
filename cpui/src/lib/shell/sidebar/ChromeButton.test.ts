import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import ChromeButton from './ChromeButton.svelte';

describe('ChromeButton', () => {
	it('is named by its label, not its glyph, and carries its title', () => {
		render(ChromeButton, { icon: '›', title: 'Expand  ⌘\\', label: 'Expand', onclick: vi.fn() });
		expect(screen.getByRole('button', { name: 'Expand' })).toHaveAttribute('title', 'Expand  ⌘\\');
	});

	it('fires on a click', async () => {
		const onclick = vi.fn();
		render(ChromeButton, { icon: '›', title: 't', label: 'Expand', onclick });
		await userEvent.click(screen.getByRole('button'));
		expect(onclick).toHaveBeenCalledOnce();
	});
});
