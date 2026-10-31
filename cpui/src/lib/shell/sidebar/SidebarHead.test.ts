import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { prefs } from '$lib/app/prefs.svelte';
import SidebarHead from './SidebarHead.svelte';

beforeEach(() => {
	prefs.collapsed = false;
});

describe('SidebarHead', () => {
	it('collapses the sidebar', async () => {
		render(SidebarHead, { drawer: false, onsearch: vi.fn() });
		await userEvent.click(screen.getByRole('button', { name: 'Collapse sidebar' }));
		expect(prefs.collapsed).toBe(true);
	});

	it('has nothing to collapse in the drawer', () => {
		render(SidebarHead, { drawer: true, onsearch: vi.fn() });
		expect(screen.queryByRole('button', { name: 'Collapse sidebar' })).not.toBeInTheDocument();
	});

	it('opens the search', async () => {
		const onsearch = vi.fn();
		render(SidebarHead, { drawer: false, onsearch });
		await userEvent.click(screen.getByRole('button', { name: /Search, jump, do/ }));
		expect(onsearch).toHaveBeenCalledOnce();
	});
});
