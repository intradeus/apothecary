import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { api } from '$lib/api';
import { session } from '$lib/app/session.svelte';
import type { Me } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { bindable } from '$test/fixtures/app.svelte';
import UserMenu from './UserMenu.svelte';

vi.mock('$lib/api', () => ({ api: { auth: { logout: vi.fn() } } }));

const signIn = (admin = false) => {
	session.me = { user: { id: 'u', handle: 'lu', name: 'Lu', admin } } as Me;
};
const setup = (onnavigate = vi.fn()) => {
	const props = bindable({ anchor: document.body, open: true, onnavigate });
	render(UserMenu, { props });
	return props;
};

beforeEach(() => {
	vi.resetAllMocks();
	session.me = null;
	toasts.list = [];
});

describe('UserMenu', () => {
	it('stays closed until it is opened', () => {
		signIn();
		render(UserMenu, { props: { anchor: null, open: false } });
		expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
	});

	it('names the person it belongs to', () => {
		signIn();
		setup();
		const menu = screen.getByRole('dialog');
		expect(menu).toHaveTextContent('Lu');
		expect(menu).toHaveTextContent('@lu');
	});

	it('goes to the account page, closing itself and the drawer', async () => {
		signIn();
		const onnavigate = vi.fn();
		const props = setup(onnavigate);

		await userEvent.click(screen.getByRole('button', { name: /Account/ }));

		expect(goto).toHaveBeenCalledWith('/system/account');
		expect(onnavigate).toHaveBeenCalledOnce();
		expect(props.open).toBe(false);
	});

	it('offers the users page to an admin only', async () => {
		signIn(false);
		const { unmount } = render(UserMenu, { props: { anchor: document.body, open: true } });
		expect(screen.queryByRole('button', { name: /Users/ })).not.toBeInTheDocument();
		unmount();

		signIn(true);
		setup();
		await userEvent.click(screen.getByRole('button', { name: /Users/ }));
		expect(goto).toHaveBeenCalledWith('/system/users');
	});

	it('signs out', async () => {
		signIn();
		vi.mocked(api.auth.logout).mockResolvedValue(undefined);
		const props = setup();

		await userEvent.click(screen.getByRole('button', { name: /Sign out/ }));

		expect(api.auth.logout).toHaveBeenCalledOnce();
		expect(session.me).toBeNull();
		expect(props.open).toBe(false);
	});

	it('says so when the engine could not sign out', async () => {
		signIn();
		vi.mocked(api.auth.logout).mockRejectedValue(new Error('engine down'));
		const props = setup();

		await userEvent.click(screen.getByRole('button', { name: /Sign out/ }));

		expect(api.auth.logout).toHaveBeenCalledOnce();
		expect(session.me).toBeNull();
		expect(props.open).toBe(false);
		expect(toasts.list.at(-1)?.text).toBe('engine down');
	});
});
