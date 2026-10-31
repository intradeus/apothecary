import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { NavItem } from '$lib/app/nav';
import NavList from './NavList.svelte';

const none = () => 0;
const link = (name: RegExp) =>
	within(screen.getByRole('navigation', { name: 'Primary' })).getByRole('link', { name });

describe('NavList', () => {
	it('links every destination', () => {
		render(NavList, { path: '/', count: none });
		expect(link(/Desk/)).toHaveAttribute('href', '/');
		expect(link(/Inbox/)).toHaveAttribute('href', '/inbox');
		expect(link(/Runs/)).toHaveAttribute('href', '/runs');
		expect(link(/Tickets/)).toHaveAttribute('href', '/tickets');
		expect(link(/System/)).toHaveAttribute('href', '/system/integrations');
	});

	it('marks the destination the path is inside, and only that one', () => {
		render(NavList, { path: '/runs/01RUN', count: none });
		expect(link(/Runs/)).toHaveAttribute('aria-current', 'page');
		expect(screen.getAllByRole('link').filter((a) => a.hasAttribute('aria-current'))).toHaveLength(
			1
		);
	});

	it('counts what each destination reports, nothing for a zero', () => {
		const count = (n: NavItem) => (n.id === 'inbox' ? 3 : 0);
		render(NavList, { path: '/', count });
		expect(link(/Inbox/)).toHaveTextContent('3');
		expect(link(/Runs/)).not.toHaveTextContent(/\d/);
	});

	it('marks a waiting inbox hot', () => {
		render(NavList, { path: '/', count: () => 2 });
		expect(link(/Inbox/).querySelector('.count')).toHaveClass('hot');
		expect(link(/Runs/).querySelector('.count')).not.toHaveClass('hot');
	});

	it('tells the drawer a link was followed', async () => {
		const onnavigate = vi.fn();
		render(NavList, { path: '/', count: none, onnavigate });
		link(/Inbox/).addEventListener('click', (e) => e.preventDefault());
		await userEvent.click(link(/Inbox/));
		expect(onnavigate).toHaveBeenCalledOnce();
	});
});
