import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Credential, ProviderInfo } from '$lib/domain';
import { confirmState } from '$lib/ui/confirm.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import { confirmed } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import CredentialCard from './CredentialCard.svelte';

vi.mock('$lib/api', () => ({
	api: { credentials: { test: vi.fn(), rename: vi.fn(), remove: vi.fn() } }
}));

const credential = (over: Partial<Credential> = {}): Credential => ({
	id: 'c1',
	provider: 'anthropic',
	kind: 'api_key',
	name: 'Team key',
	status: 'ok',
	createdAt: '2026-10-01T00:00:00Z',
	...over
});
const setup = (over: Record<string, unknown> = {}) => {
	const onchanged = vi.fn();
	render(CredentialCard, { credential: credential(), onchanged, ...over });
	return { onchanged };
};
const test = () => screen.getByRole('button', { name: 'Test' });
const del = () => screen.getByRole('button', { name: 'Delete' });

beforeEach(() => {
	vi.resetAllMocks();
	toasts.list = [];
	confirmState.current = null;
});
afterEach(() => {
	vi.useRealTimers();
});

describe('CredentialCard, the summary', () => {
	it('names the provider and the kind', () => {
		setup({ provider: { name: 'Anthropic' } as ProviderInfo });
		expect(screen.getByText('Team key')).toBeInTheDocument();
		expect(screen.getByText('API key')).toBeInTheDocument();
		expect(screen.getByText('Anthropic')).toBeInTheDocument();
		expect(screen.getByText('not used by any harness')).toBeInTheDocument();
	});

	it('falls back to the provider id', () => {
		setup();
		expect(screen.getByText('anthropic')).toBeInTheDocument();
	});

	it('shows a login: its account, expiry and last refresh', () => {
		vi.useFakeTimers({ now: new Date('2026-10-31T12:00:00Z') });
		setup({
			credential: credential({
				kind: 'oauth',
				account: 'lu@test.dev',
				lastRefresh: '2026-10-31T10:00:00Z'
			})
		});
		expect(screen.getByText('login')).toBeInTheDocument();
		expect(screen.getByText('lu@test.dev')).toBeInTheDocument();
		expect(screen.getByText('does not expire')).toBeInTheDocument();
		expect(screen.getByText('2h ago')).toBeInTheDocument();
	});

	it('shows no expiry on a key', () => {
		setup();
		expect(screen.queryByText('valid until')).not.toBeInTheDocument();
	});

	it('names the harnesses using it', () => {
		setup({ usedBy: ['claude', 'codex'] });
		expect(screen.getByText('used by claude, codex')).toBeInTheDocument();
	});
});

describe('CredentialCard, testing', () => {
	it('says a disabled login must sign in again, over anything else', () => {
		setup({ credential: credential({ status: 'disabled', error: 'revoked' }) });
		expect(screen.getByText('signed out — sign in again')).toBeInTheDocument();
	});

	it('starts untested', () => {
		setup();
		expect(screen.getByText('not tested')).toBeInTheDocument();
	});

	it('shows a pass and refreshes the page', async () => {
		vi.mocked(api.credentials.test).mockResolvedValue({ ok: true, warnings: [] } as never);
		const { onchanged } = setup();
		await userEvent.click(test());

		expect(await screen.findByText('works')).toBeInTheDocument();
		expect(onchanged).toHaveBeenCalledOnce();
	});

	it('shows a pass it could not verify', async () => {
		vi.mocked(api.credentials.test).mockResolvedValue({
			ok: true,
			warnings: ['no probe']
		} as never);
		setup();
		await userEvent.click(test());
		expect(await screen.findByText('ok, unverified')).toBeInTheDocument();
	});

	it('shows a failure, whether reported or thrown, and still refreshes', async () => {
		vi.mocked(api.credentials.test).mockRejectedValue(new Error('expired'));
		const { onchanged } = setup();
		await userEvent.click(test());

		expect(await screen.findByText('expired')).toBeInTheDocument();
		expect(onchanged).toHaveBeenCalledOnce();
	});

	it('holds the button while it tests', async () => {
		const run = deferred<never>();
		vi.mocked(api.credentials.test).mockReturnValue(run.promise);
		setup();
		await userEvent.click(test());

		expect(test()).toBeDisabled();
		expect(screen.getByText('testing…')).toBeInTheDocument();
		run.resolve({ ok: true, warnings: [] } as never);
		await waitFor(() => expect(test()).toBeEnabled());
	});
});

describe('CredentialCard, renaming', () => {
	const open = async () => {
		await userEvent.click(screen.getByRole('button', { name: 'Rename' }));
		return screen.getByRole('textbox', { name: 'Rename' });
	};

	it('starts from the current name and saves the new one, trimmed', async () => {
		vi.mocked(api.credentials.rename).mockResolvedValue(undefined as never);
		const { onchanged } = setup();
		const input = await open();
		expect(input).toHaveValue('Team key');

		await userEvent.clear(input);
		await userEvent.type(input, ' Personal {Enter}');

		expect(api.credentials.rename).toHaveBeenCalledWith('c1', 'Personal');
		await waitFor(() => expect(onchanged).toHaveBeenCalledOnce());
	});

	it('shows a refusal and stays open', async () => {
		vi.mocked(api.credentials.rename).mockRejectedValue(new Error('name taken'));
		setup();
		await open();
		await userEvent.click(screen.getByRole('button', { name: 'Save' }));

		expect(await screen.findByText('name taken')).toBeInTheDocument();
		expect(screen.getByRole('textbox', { name: 'Rename' })).toBeInTheDocument();
	});
});

describe('CredentialCard, deleting', () => {
	it('refuses up front while a harness uses it', async () => {
		setup({ usedBy: ['claude'] });
		await userEvent.click(del());
		expect(screen.getAllByText('used by claude')).toHaveLength(2);
		expect(confirmState.current).toBeNull();
	});

	it('does nothing when the dialog is declined', async () => {
		setup();
		await userEvent.click(del());
		await confirmed(false);
		await Promise.resolve();
		expect(api.credentials.remove).not.toHaveBeenCalled();
	});

	it('removes it and tells the page', async () => {
		vi.mocked(api.credentials.remove).mockResolvedValue(undefined as never);
		const { onchanged } = setup();
		await userEvent.click(del());
		await confirmed(true);

		await waitFor(() => expect(onchanged).toHaveBeenCalledOnce());
		expect(api.credentials.remove).toHaveBeenCalledWith('c1');
		expect(toasts.list.at(-1)?.text).toBe('Team key deleted');
	});

	it('shows a refusal on the card', async () => {
		vi.mocked(api.credentials.remove).mockRejectedValue(new Error('locked'));
		const { onchanged } = setup();
		await userEvent.click(del());
		await confirmed(true);

		expect(await screen.findByText('locked')).toBeInTheDocument();
		expect(onchanged).not.toHaveBeenCalled();
	});
});
