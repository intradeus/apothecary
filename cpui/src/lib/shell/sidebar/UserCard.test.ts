import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { session } from '$lib/app/session.svelte';
import type { Me } from '$lib/domain';
import { bindable } from '$test/fixtures/app.svelte';
import UserCard from './UserCard.svelte';

vi.mock('$lib/api', () => ({ api: {} }));

const signIn = (over: Partial<Me['user']> = {}) => {
	session.me = {
		user: { id: 'u', handle: 'lu', name: 'Lucas Roland', admin: false, ...over }
	} as Me;
};
const trigger = () => screen.getByRole('button', { name: 'Account menu' });

beforeEach(() => {
	session.me = null;
});

describe('UserCard', () => {
	it('shows the person: initials, name and handle', () => {
		signIn();
		render(UserCard, { btn: null, onmenu: vi.fn() });
		expect(trigger()).toHaveTextContent('Lucas Roland');
		expect(trigger()).toHaveTextContent('@lu');
		expect(trigger()).toHaveTextContent('LR');
		expect(trigger()).not.toHaveTextContent('admin');
	});

	it('says so for an admin', () => {
		signIn({ admin: true });
		render(UserCard, { btn: null, onmenu: vi.fn() });
		expect(trigger()).toHaveTextContent('admin');
	});

	it('keeps only the avatar on the rail, with the name as its title', () => {
		signIn();
		render(UserCard, { rail: true, btn: null, onmenu: vi.fn() });
		expect(trigger()).toHaveAttribute('title', 'Lucas Roland');
		expect(trigger()).not.toHaveTextContent('@lu');
	});

	it.each([false, true])('opens the menu (rail: %s)', async (rail) => {
		signIn();
		const onmenu = vi.fn();
		render(UserCard, { rail, btn: null, onmenu });
		await userEvent.click(trigger());
		expect(onmenu).toHaveBeenCalledOnce();
	});

	it.each([false, true])('hands the trigger up for the menu to anchor to (rail: %s)', (rail) => {
		signIn();
		const props = bindable({ rail, btn: null as HTMLElement | null, onmenu: vi.fn() });
		render(UserCard, props);
		expect(props.btn).toBe(trigger());
	});

	it('carries the theme picker in both shapes', () => {
		signIn();
		const { unmount } = render(UserCard, { btn: null, onmenu: vi.fn() });
		expect(screen.getByRole('button', { name: 'Change theme' })).toBeInTheDocument();
		unmount();
		render(UserCard, { rail: true, btn: null, onmenu: vi.fn() });
		expect(screen.getByRole('button', { name: 'Change theme' })).toBeInTheDocument();
	});
});
