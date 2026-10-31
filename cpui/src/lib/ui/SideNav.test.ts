import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { I } from '$lib/design/icons';
import SideNav from './SideNav.svelte';

const steps = [
	{ id: 'repo', label: 'Repository', step: 1, done: true },
	{ id: 'flow', label: 'Flow', step: 2, sub: 'Pick one' }
];
const links = [
	{ id: 'general', label: 'General', href: '/system/general', icon: I.system },
	{ id: 'users', label: 'Users', href: '/system/users', count: 4, hot: true },
	{ id: 'keys', label: 'Keys', href: '/system/keys', count: 0 }
];

describe('SideNav', () => {
	it('is a navigation landmark, "Sections" unless named', async () => {
		const { rerender } = render(SideNav, { items: links, active: 'general' });
		expect(screen.getByRole('navigation', { name: 'Sections' })).toBeInTheDocument();
		await rerender({ label: 'System' });
		expect(screen.getByRole('navigation', { name: 'System' })).toBeInTheDocument();
	});

	it('shows its title', () => {
		render(SideNav, { items: links, active: 'general', title: 'System' });
		expect(screen.getByText('System')).toBeInTheDocument();
	});

	it('renders an item with an href as a link marked as the current page', () => {
		render(SideNav, { items: links, active: 'users' });
		expect(screen.getByRole('link', { name: /Users/ })).toHaveAttribute('aria-current', 'page');
		const general = screen.getByRole('link', { name: /General/ });
		expect(general).toHaveAttribute('href', '/system/general');
		expect(general).not.toHaveAttribute('aria-current');
	});

	it('renders an in-page item as a button marked as the current step', () => {
		render(SideNav, { items: steps, active: 'flow' });
		expect(screen.getByRole('button', { name: /Flow/ })).toHaveAttribute('aria-current', 'step');
		expect(screen.getByRole('button', { name: /Repository/ })).not.toHaveAttribute('aria-current');
	});

	it('asks for an in-page item by id', async () => {
		const onselect = vi.fn();
		render(SideNav, { items: steps, active: 'repo', onselect });

		await userEvent.click(screen.getByRole('button', { name: /Flow/ }));

		expect(onselect).toHaveBeenCalledExactlyOnceWith('flow');
	});

	it('numbers a step, and ticks it once done', () => {
		render(SideNav, { items: steps, active: 'repo' });
		expect(screen.getByRole('button', { name: /Flow/ })).toHaveTextContent('2');
		const done = screen.getByRole('button', { name: /Repository/ });
		expect(done).toHaveTextContent(I.ok);
		expect(done).not.toHaveTextContent('1');
	});

	it('shows the second line and the icon', () => {
		render(SideNav, { items: [...steps, ...links], active: 'repo' });
		expect(screen.getByText('Pick one')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: /General/ })).toHaveTextContent(I.system);
	});

	it('shows a count, hot when it needs someone, and none at zero', () => {
		render(SideNav, { items: links, active: 'general' });
		expect(screen.getByText('4')).toHaveClass('hot');
		expect(screen.getByRole('link', { name: 'Keys' })).toHaveTextContent(/^Keys$/);
	});

	it('moves the selection with active', async () => {
		const { rerender } = render(SideNav, { items: links, active: 'general' });
		await rerender({ active: 'keys' });
		expect(screen.getByRole('link', { name: 'Keys' })).toHaveAttribute('aria-current', 'page');
		expect(screen.getByRole('link', { name: /General/ })).not.toHaveAttribute('aria-current');
	});
});
