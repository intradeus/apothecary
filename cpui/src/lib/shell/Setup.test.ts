import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Me } from '$lib/domain';
import { session } from '$lib/app/session.svelte';
import { deferred } from '$test/helpers';
import Setup from './Setup.svelte';

vi.mock('$lib/api', () => ({ api: { auth: { setup: vi.fn() } } }));

const me = { user: { id: 'u', handle: 'lu', name: 'Lu', admin: true } } as Me;
const action = () => screen.getByRole('button', { name: 'Create the first account' });

async function fill(
	over: Partial<Record<'handle' | 'name' | 'email' | 'pw' | 'again', string>> = {}
) {
	const v = {
		handle: 'lu',
		name: '',
		email: 'lu@test.dev',
		pw: 'hunter22',
		again: 'hunter22',
		...over
	};
	await userEvent.type(screen.getByLabelText('Handle'), v.handle);
	if (v.name) await userEvent.type(screen.getByLabelText('Name'), v.name);
	await userEvent.type(screen.getByLabelText('Email'), v.email);
	await userEvent.type(screen.getByLabelText('Password'), v.pw);
	await userEvent.type(screen.getByLabelText('Password, again'), v.again);
}

beforeEach(() => {
	vi.resetAllMocks();
	session.me = null;
	session.setupRequired = true;
});

describe('Setup', () => {
	it('claims the instance with the fields trimmed and the password as typed', async () => {
		vi.mocked(api.auth.setup).mockResolvedValue(me);
		render(Setup);
		await fill({ handle: 'lu', name: ' Lucas ', pw: ' hunter22', again: ' hunter22' });
		await userEvent.click(action());

		expect(api.auth.setup).toHaveBeenCalledWith({
			handle: 'lu',
			email: 'lu@test.dev',
			name: 'Lucas',
			password: ' hunter22'
		});
		expect(session.setupRequired).toBe(false);
		expect(session.admin).toBe(true);
	});

	it('refuses two different passwords before asking the engine', async () => {
		render(Setup);
		await fill({ again: 'hunter23' });
		await userEvent.click(action());

		expect(screen.getByText('The two passwords differ.')).toBeInTheDocument();
		expect(api.auth.setup).not.toHaveBeenCalled();
		expect(action()).toBeEnabled();
	});

	it('clears the mismatch once the passwords agree', async () => {
		vi.mocked(api.auth.setup).mockResolvedValue(me);
		render(Setup);
		await fill({ again: 'hunter23' });
		await userEvent.click(action());

		await userEvent.clear(screen.getByLabelText('Password, again'));
		await userEvent.type(screen.getByLabelText('Password, again'), 'hunter22');
		await userEvent.click(action());

		expect(screen.queryByText('The two passwords differ.')).not.toBeInTheDocument();
		expect(api.auth.setup).toHaveBeenCalledOnce();
	});

	// HTML compiles a pattern with the `v` flag: a bare `-` in a character class
	// is a syntax error there, and a browser drops a pattern that does not compile.
	it.each([
		['upper case', 'Lucas'],
		['one letter', 'l'],
		['a leading dash', '-lu']
	])('refuses a handle with %s', async (_, handle) => {
		render(Setup);
		await fill({ handle });
		await userEvent.click(action());
		expect(api.auth.setup).not.toHaveBeenCalled();
	});

	// jsdom does not enforce minlength, so this states the rule the browser gets.
	it('asks the browser for at least eight characters', () => {
		render(Setup);
		expect(screen.getByLabelText('Password')).toHaveAttribute('minlength', '8');
	});

	it('holds the button while the engine answers', async () => {
		const setup = deferred<Me>();
		vi.mocked(api.auth.setup).mockReturnValue(setup.promise);
		render(Setup);
		await fill();
		await userEvent.click(action());

		expect(action()).toBeDisabled();
		setup.resolve(me);
		await waitFor(() => expect(action()).toBeEnabled());
	});

	it('shows a refusal and stays on the page', async () => {
		vi.mocked(api.auth.setup).mockRejectedValue(new Error('handle taken'));
		render(Setup);
		await fill();
		await userEvent.click(action());

		expect(await screen.findByText('handle taken')).toBeInTheDocument();
		expect(session.setupRequired).toBe(true);
		expect(action()).toBeEnabled();
	});
});
