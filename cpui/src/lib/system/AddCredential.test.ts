import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Credential, ProviderInfo, SignInPending } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { choose } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import AddCredential from './AddCredential.svelte';

vi.mock('$lib/api', () => ({
	api: {
		credentials: {
			addKey: vi.fn(),
			importFile: vi.fn(),
			signIn: vi.fn(),
			completeSignIn: vi.fn()
		}
	}
}));

const providers = [
	{ id: 'anth', name: 'Anthropic', key: true, signIn: true },
	{ id: 'oai', name: 'OpenAI', key: true, signIn: false },
	{ id: 'gh', name: 'GitHub', key: false, signIn: true }
] as ProviderInfo[];
const cred = (name = 'Team key') => ({ id: 'c1', name }) as Credential;
const pending = (over: Partial<SignInPending> = {}): SignInPending => ({
	id: 's1',
	provider: 'anth',
	url: 'https://login.test',
	paste: false,
	intervalSeconds: 5,
	expiresAt: '2026-10-31T13:00:00Z',
	...over
});
const setup = (mode: 'key' | 'signin' | 'import') => {
	const onadded = vi.fn();
	const oncancel = vi.fn();
	const view = render(AddCredential, { mode, providers, onadded, oncancel });
	return { onadded, oncancel, ...view };
};

beforeEach(() => {
	vi.resetAllMocks();
	vi.spyOn(window, 'open').mockReturnValue(null);
	toasts.list = [];
});
afterEach(() => {
	vi.useRealTimers();
});

describe('AddCredential, an API key', () => {
	const save = () => screen.getByRole('button', { name: 'Save key' });

	it('offers only the providers that take a key', async () => {
		setup('key');
		await userEvent.click(screen.getByRole('combobox', { name: 'Provider' }));
		expect(await screen.findByRole('option', { name: 'Anthropic' })).toBeInTheDocument();
		expect(screen.getByRole('option', { name: 'OpenAI' })).toBeInTheDocument();
		expect(screen.queryByRole('option', { name: 'GitHub' })).not.toBeInTheDocument();
	});

	it('waits for a provider', () => {
		setup('key');
		expect(save()).toBeDisabled();
	});

	it('needs the key itself', async () => {
		setup('key');
		await choose('Provider', 'Anthropic');
		await userEvent.click(save());
		expect(api.credentials.addKey).not.toHaveBeenCalled();
	});

	it('saves the key and name trimmed, and closes the panel', async () => {
		const c = cred();
		vi.mocked(api.credentials.addKey).mockResolvedValue(c);
		const { onadded } = setup('key');
		await choose('Provider', 'Anthropic');
		await userEvent.type(screen.getByLabelText('API key'), ' sk-123 ');
		await userEvent.type(screen.getByLabelText('Name (optional)'), ' Mine ');
		await userEvent.click(save());

		expect(api.credentials.addKey).toHaveBeenCalledWith('anth', 'sk-123', 'Mine');
		expect(onadded).toHaveBeenCalledWith([c], true);
		expect(toasts.list.at(-1)?.text).toBe('Team key added');
	});

	it('sends no name when none was given', async () => {
		vi.mocked(api.credentials.addKey).mockResolvedValue(cred());
		setup('key');
		await choose('Provider', 'Anthropic');
		await userEvent.type(screen.getByLabelText('API key'), 'sk');
		await userEvent.click(save());
		expect(api.credentials.addKey).toHaveBeenCalledWith('anth', 'sk', undefined);
	});

	it('holds the button while it saves', async () => {
		const add = deferred<Credential>();
		vi.mocked(api.credentials.addKey).mockReturnValue(add.promise);
		setup('key');
		await choose('Provider', 'Anthropic');
		await userEvent.type(screen.getByLabelText('API key'), 'sk');
		await userEvent.click(save());

		expect(screen.getByRole('button', { name: 'Saving…' })).toBeDisabled();
		add.resolve(cred());
		await waitFor(() => expect(save()).toBeEnabled());
	});

	it('shows a refusal and stays open', async () => {
		vi.mocked(api.credentials.addKey).mockRejectedValue(new Error('invalid key'));
		const { onadded } = setup('key');
		await choose('Provider', 'Anthropic');
		await userEvent.type(screen.getByLabelText('API key'), 'sk');
		await userEvent.click(save());

		expect(await screen.findByText('invalid key')).toBeInTheDocument();
		expect(onadded).not.toHaveBeenCalled();
	});

	it('cancels', async () => {
		const { oncancel } = setup('key');
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(oncancel).toHaveBeenCalledOnce();
	});
});

describe('AddCredential, importing a file', () => {
	const action = () => screen.getByRole('button', { name: 'Import' });
	const file = () => screen.getByLabelText('A credential file a harness CLI wrote');

	it('needs the file', async () => {
		setup('import');
		await userEvent.click(action());
		expect(api.credentials.importFile).not.toHaveBeenCalled();
	});

	it('imports and closes when all of it went in', async () => {
		const c = cred();
		vi.mocked(api.credentials.importFile).mockResolvedValue({ created: [c], rejected: [] });
		const { onadded } = setup('import');
		await userEvent.type(file(), 'body');
		await userEvent.click(action());

		expect(api.credentials.importFile).toHaveBeenCalledWith('body');
		expect(onadded).toHaveBeenCalledWith([c], true);
		expect(toasts.list.at(-1)?.text).toBe('1 credential imported');
	});

	it('stays open to show what was refused alongside what went in', async () => {
		const c = cred();
		vi.mocked(api.credentials.importFile).mockResolvedValue({
			created: [c],
			rejected: ['expired token']
		});
		const { onadded } = setup('import');
		await userEvent.type(file(), 'body');
		await userEvent.click(action());

		expect(await screen.findByText('expired token')).toBeInTheDocument();
		expect(screen.getByText('Not imported')).toBeInTheDocument();
		expect(onadded).toHaveBeenCalledWith([c], false);
	});

	it('adds nothing and raises no toast when all of it was refused', async () => {
		vi.mocked(api.credentials.importFile).mockResolvedValue({ created: [], rejected: ['bad'] });
		const { onadded } = setup('import');
		await userEvent.type(file(), 'body');
		await userEvent.click(action());

		expect(await screen.findByText('bad')).toBeInTheDocument();
		expect(onadded).not.toHaveBeenCalled();
		expect(toasts.list).toEqual([]);
	});

	it('clears old refusals on the next try', async () => {
		vi.mocked(api.credentials.importFile).mockResolvedValueOnce({ created: [], rejected: ['bad'] });
		setup('import');
		await userEvent.type(file(), 'body');
		await userEvent.click(action());
		await screen.findByText('bad');

		vi.mocked(api.credentials.importFile).mockRejectedValueOnce(new Error('unreadable'));
		await userEvent.click(action());
		expect(await screen.findByText('unreadable')).toBeInTheDocument();
		expect(screen.queryByText('bad')).not.toBeInTheDocument();
	});
});

describe('AddCredential, signing in', () => {
	const start = () => screen.getByRole('button', { name: 'Start sign-in' });

	async function begin(p = pending()) {
		vi.mocked(api.credentials.signIn).mockResolvedValue(p);
		const view = setup('signin');
		await choose('Provider', 'Anthropic');
		return view;
	}

	it('offers only the providers with a sign-in', async () => {
		setup('signin');
		await userEvent.click(screen.getByRole('combobox', { name: 'Provider' }));
		expect(await screen.findByRole('option', { name: 'GitHub' })).toBeInTheDocument();
		expect(screen.queryByRole('option', { name: 'OpenAI' })).not.toBeInTheDocument();
	});

	it("opens the provider's page in a new tab", async () => {
		await begin(pending({ paste: true }));
		await userEvent.click(start());

		expect(api.credentials.signIn).toHaveBeenCalledWith('anth');
		expect(window.open).toHaveBeenCalledWith('https://login.test', '_blank', 'noopener');
		expect(screen.getByRole('link', { name: 'Open Anthropic' })).toHaveAttribute(
			'href',
			'https://login.test'
		);
	});

	it('shows a failure to start and stays on the form', async () => {
		vi.mocked(api.credentials.signIn).mockRejectedValue(new Error('provider down'));
		setup('signin');
		await choose('Provider', 'Anthropic');
		await userEvent.click(start());

		expect(await screen.findByText('provider down')).toBeInTheDocument();
		expect(start()).toBeEnabled();
	});

	describe('a pasted code', () => {
		const finish = () => screen.getByRole('button', { name: 'Finish sign-in' });
		const paste = () => screen.getByRole('textbox', { name: /Paste the code Anthropic shows/ });

		it('finishes with the code trimmed and the name', async () => {
			const c = cred('Signed in');
			vi.mocked(api.credentials.completeSignIn).mockResolvedValue(c);
			const { onadded } = await begin(pending({ paste: true }));
			await userEvent.type(screen.getByLabelText('Name (optional)'), 'Mine');
			await userEvent.click(start());
			await userEvent.type(paste(), ' code#state ');
			await userEvent.click(finish());

			expect(api.credentials.completeSignIn).toHaveBeenCalledWith('s1', 'code#state', 'Mine');
			expect(onadded).toHaveBeenCalledWith([c], true);
			expect(toasts.list.at(-1)?.text).toBe('Signed in: Signed in');
		});

		it('does not poll', async () => {
			vi.useFakeTimers();
			const user = userEvent.setup({ delay: null });
			vi.mocked(api.credentials.signIn).mockResolvedValue(pending({ paste: true }));
			setup('signin');
			await user.click(screen.getByRole('combobox', { name: 'Provider' }));
			await tick();
			await user.click(screen.getByRole('option', { name: 'Anthropic' }));
			await user.click(start());
			await vi.advanceTimersByTimeAsync(60_000);
			expect(api.credentials.completeSignIn).not.toHaveBeenCalled();
		});

		it('shows a refused code and lets the person try again', async () => {
			vi.mocked(api.credentials.completeSignIn).mockRejectedValueOnce(new Error('wrong code'));
			await begin(pending({ paste: true }));
			await userEvent.click(start());
			await userEvent.type(paste(), 'nope');
			await userEvent.click(finish());

			expect(await screen.findByText('wrong code')).toBeInTheDocument();
			expect(finish()).toBeEnabled();
		});
	});

	describe('a device code', () => {
		const device = () => pending({ userCode: 'ABCD-EFGH', intervalSeconds: 5 });
		const complete = () => vi.mocked(api.credentials.completeSignIn);

		/** Fake timers and a sign-in under way. */
		async function waiting(p = device(), name = '') {
			vi.useFakeTimers();
			const user = userEvent.setup({ delay: null });
			vi.mocked(api.credentials.signIn).mockResolvedValue(p);
			const view = setup('signin');
			await user.click(screen.getByRole('combobox', { name: 'Provider' }));
			await tick();
			await user.click(screen.getByRole('option', { name: 'Anthropic' }));
			if (name) await user.type(screen.getByLabelText('Name (optional)'), name);
			await user.click(start());
			return { user, ...view };
		}

		it('shows the code and waits', async () => {
			await waiting();
			expect(screen.getByText('ABCD-EFGH')).toBeInTheDocument();
			expect(screen.getByText(/Waiting for you to approve/)).toBeInTheDocument();
		});

		it("asks at the provider's own cadence until it is approved", async () => {
			complete().mockResolvedValueOnce(null as never);
			complete().mockResolvedValueOnce(null as never);
			const c = cred('Signed in');
			complete().mockResolvedValueOnce(c);
			const { onadded } = await waiting(device(), 'Mine');

			await vi.advanceTimersByTimeAsync(4999);
			expect(complete()).not.toHaveBeenCalled();
			await vi.advanceTimersByTimeAsync(1);
			expect(complete()).toHaveBeenCalledWith('s1', undefined, 'Mine');

			await vi.advanceTimersByTimeAsync(5000);
			expect(complete()).toHaveBeenCalledTimes(2);
			expect(onadded).not.toHaveBeenCalled();

			await vi.advanceTimersByTimeAsync(5000);
			expect(onadded).toHaveBeenCalledWith([c], true);
			expect(toasts.list.at(-1)?.text).toBe('Signed in: Signed in');

			await vi.advanceTimersByTimeAsync(60_000);
			expect(complete()).toHaveBeenCalledTimes(3);
		});

		it('stops on a failure and goes back to the form with the reason', async () => {
			complete().mockRejectedValue(new Error('sign-in expired'));
			await waiting();
			await vi.advanceTimersByTimeAsync(5000);

			expect(screen.getByText('sign-in expired')).toBeInTheDocument();
			expect(start()).toBeInTheDocument();
			await vi.advanceTimersByTimeAsync(60_000);
			expect(complete()).toHaveBeenCalledOnce();
		});

		it('stops polling when the panel closes', async () => {
			const { unmount } = await waiting();
			unmount();
			await vi.advanceTimersByTimeAsync(60_000);
			expect(complete()).not.toHaveBeenCalled();
		});

		// A poll in flight at unmount answers "not yet": nothing is left to clear a
		// new timer, so none may be armed.
		it('stops polling when the panel closes during a request', async () => {
			const answer = deferred<never>();
			complete().mockReturnValueOnce(answer.promise);
			complete().mockResolvedValue(null as never);
			const { unmount } = await waiting();
			await vi.advanceTimersByTimeAsync(5000);
			expect(complete()).toHaveBeenCalledOnce();

			unmount();
			answer.resolve(null as never);
			await vi.advanceTimersByTimeAsync(60_000);
			expect(complete()).toHaveBeenCalledOnce();
		});

		it('copies the code', async () => {
			const { user } = await waiting();
			const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
			await user.click(screen.getByRole('button', { name: 'Copy' }));
			await vi.advanceTimersByTimeAsync(0);

			expect(writeText).toHaveBeenCalledWith('ABCD-EFGH');
			expect(toasts.list.at(-1)?.text).toBe('Code copied');
		});

		it('keeps quiet when the clipboard is refused: the code is on screen', async () => {
			const { user } = await waiting();
			vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'));
			await user.click(screen.getByRole('button', { name: 'Copy' }));
			await vi.advanceTimersByTimeAsync(0);

			expect(toasts.list).toEqual([]);
		});
	});
});
