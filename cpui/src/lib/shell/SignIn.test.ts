import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Me } from '$lib/domain';
import { session } from '$lib/app/session.svelte';
import { deferred } from '$test/helpers';
import SignIn from './SignIn.svelte';

vi.mock('$lib/api', () => ({ api: { auth: { login: vi.fn() } } }));

const me = { user: { id: 'u', handle: 'lu', name: 'Lu', admin: false } } as Me;
const action = () => screen.getByRole('button', { name: 'Sign in' });

async function fill(login: string, password: string) {
	await userEvent.type(screen.getByLabelText('Handle or email'), login);
	await userEvent.type(screen.getByLabelText('Password'), password);
}

beforeEach(() => {
	vi.resetAllMocks();
	session.me = null;
});

describe('SignIn', () => {
	it('signs in with the login trimmed and the password as typed', async () => {
		vi.mocked(api.auth.login).mockResolvedValue(me);
		render(SignIn);
		await fill('  lu ', ' secret ');
		await userEvent.click(action());

		expect(api.auth.login).toHaveBeenCalledWith('lu', ' secret ');
		expect(session.user?.handle).toBe('lu');
	});

	it('submits on Enter in a field', async () => {
		vi.mocked(api.auth.login).mockResolvedValue(me);
		render(SignIn);
		await fill('lu', 'secret{Enter}');
		expect(api.auth.login).toHaveBeenCalledOnce();
	});

	it('does not send an empty form', async () => {
		render(SignIn);
		await userEvent.click(action());
		expect(api.auth.login).not.toHaveBeenCalled();
	});

	it('holds the button while the engine answers', async () => {
		const login = deferred<Me>();
		vi.mocked(api.auth.login).mockReturnValue(login.promise);
		render(SignIn);
		await fill('lu', 'secret');
		await userEvent.click(action());

		expect(action()).toBeDisabled();
		login.resolve(me);
		await waitFor(() => expect(action()).toBeEnabled());
	});

	it('relays what the engine said and lets the person try again', async () => {
		vi.mocked(api.auth.login).mockRejectedValueOnce(new Error('too many attempts'));
		render(SignIn);
		await fill('lu', 'wrong');
		await userEvent.click(action());

		expect(await screen.findByText('too many attempts')).toBeInTheDocument();
		expect(action()).toBeEnabled();

		vi.mocked(api.auth.login).mockResolvedValueOnce(me);
		await userEvent.click(action());
		await waitFor(() => expect(screen.queryByText('too many attempts')).not.toBeInTheDocument());
	});

	it('names the page', () => {
		render(SignIn);
		expect(document.title).toBe('Sign in · Apothecary');
	});
});
