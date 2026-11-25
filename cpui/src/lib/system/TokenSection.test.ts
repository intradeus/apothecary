import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { fmtAbs } from '$lib/core/format';
import type { Permission, Token } from '$lib/domain';
import { confirmState } from '$lib/ui/confirm.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import { aProject } from '$test/fixtures';
import { confirmed } from '$test/fixtures/app.svelte';
import TokenSection from './TokenSection.svelte';

vi.mock('$lib/api', () => ({
	api: {
		auth: {
			tokens: vi.fn(),
			tokenCalls: vi.fn(),
			permissions: vi.fn(),
			createToken: vi.fn(),
			revokeToken: vi.fn()
		}
	}
}));

const NOW = new Date('2026-11-18T12:00:00Z');
const aToken = (over: Partial<Token> = {}): Token => ({
	id: 't1',
	name: 'laptop',
	prefix: 'apo_ab',
	createdAt: '2026-11-17T12:00:00Z',
	...over
});
const PERMS: Permission[] = [
	{
		action: 'run:read',
		kind: 'run',
		doc: 'See runs.',
		grantable: true,
		purposes: ['read', 'runs']
	},
	{ action: 'run:operate', kind: 'run', doc: 'Steer runs.', grantable: true, purposes: ['runs'] }
];

const row = (name: string) => screen.getByText(name).closest('.r') as HTMLElement;
const submit = () => screen.getByRole('button', { name: /Mint a token/ });

beforeEach(() => {
	vi.resetAllMocks();
	vi.useFakeTimers({ toFake: ['Date'] });
	vi.setSystemTime(NOW);
	toasts.list = [];
	confirmState.current = null;
	fleet.projects = [aProject({ id: 'p1', name: 'billing' }), aProject({ id: 'p2', name: 'web' })];
	vi.mocked(api.auth.permissions).mockResolvedValue(PERMS);
	vi.mocked(api.auth.tokens).mockResolvedValue([]);
});
afterEach(() => vi.useRealTimers());

describe('TokenSection', () => {
	it('lists tokens with what each may do', async () => {
		vi.mocked(api.auth.tokens).mockResolvedValue([
			aToken({ id: 'a', name: 'full', lastUsedAt: '2026-11-18T11:00:00Z' }),
			aToken({ id: 'b', name: 'reader', actions: ['run:read'] }),
			aToken({
				id: 'c',
				name: 'ops',
				actions: ['run:read', 'run:operate'],
				projectIds: ['p1', 'gone-0123456']
			}),
			aToken({ id: 'd', name: 'cli', idleSeconds: 7 * 86_400 })
		]);
		render(TokenSection);

		expect(await screen.findByText('full')).toBeInTheDocument();
		expect(row('full')).toHaveTextContent('Full access');
		expect(row('full')).toHaveTextContent('last used 1h ago');
		expect(row('reader')).toHaveTextContent('1 action · all workshops');
		// An id no longer listed reads as a short id.
		expect(row('ops')).toHaveTextContent('2 actions · billing, 123456');
		expect(row('cli')).toHaveTextContent('never used');
		expect(row('cli')).toHaveTextContent('signs out after 7 days unused');
		expect(within(row('cli')).getByText('CLI')).toBeInTheDocument();
		expect(row('full')).toHaveTextContent('never expires');
	});

	describe('a token past its expiry', () => {
		const HOURLY = '2026-11-18T14:30:00Z';
		const GONE = '2026-11-18T09:00:00Z';
		const view = (name: RegExp) => screen.getByRole('radio', { name });
		const some = () => [
			aToken({ id: 'a', name: 'ci' }),
			aToken({ id: 'b', name: 'session', expiresAt: HOURLY }),
			aToken({ id: 'c', name: 'old session', expiresAt: GONE }),
			aToken({
				id: 'd',
				name: 'old laptop',
				createdAt: '2026-11-01T12:00:00Z',
				lastUsedAt: '2026-11-09T12:00:00Z',
				idleSeconds: 7 * 86_400
			})
		];

		it('is left out of the list, which says how long each token has left', async () => {
			vi.mocked(api.auth.tokens).mockResolvedValue(some());
			render(TokenSection);

			const left = await screen.findByText('expires in 2h');
			expect(row('session')).toContainElement(left);
			// The moment itself is behind the words.
			expect(left).toHaveAttribute('title', fmtAbs(HOURLY));
			expect(within(row('ci')).getByText('never expires')).not.toHaveAttribute('title');
			expect(screen.queryByText('old session')).not.toBeInTheDocument();
			expect(screen.queryByText('old laptop')).not.toBeInTheDocument();
			expect(view(/Active/)).toBeChecked();
			expect(view(/Active/)).toHaveTextContent('2');
			expect(view(/Expired/)).toHaveTextContent('2');
		});

		it('is shown on the expired side, with when it ended, and can be revoked there', async () => {
			vi.mocked(api.auth.tokens).mockResolvedValue(some());
			vi.mocked(api.auth.revokeToken).mockResolvedValue(undefined as never);
			render(TokenSection);
			await screen.findByText('ci');
			await userEvent.click(view(/Expired/));

			expect(screen.queryByText('ci')).not.toBeInTheDocument();
			const ended = within(row('old session')).getByText('expired 3h ago');
			expect(ended).toHaveAttribute('title', fmtAbs(GONE));
			// A CLI token's window ran out a week after its last use.
			expect(within(row('old laptop')).getByText('signed out 2d ago')).toHaveAttribute(
				'title',
				fmtAbs('2026-11-16T12:00:00Z')
			);

			vi.mocked(api.auth.tokens).mockResolvedValue(some().filter((tk) => tk.id !== 'c'));
			await userEvent.click(within(row('old session')).getByRole('button', { name: 'Revoke' }));
			await confirmed(true);
			await waitFor(() => expect(api.auth.revokeToken).toHaveBeenCalledWith('c'));
			await waitFor(() => expect(screen.queryByText('old session')).not.toBeInTheDocument());
			// Still the expired side.
			expect(screen.getByText('old laptop')).toBeInTheDocument();
			expect(view(/Expired/)).toHaveTextContent('1');
		});

		it('says when no token is on the side shown', async () => {
			vi.mocked(api.auth.tokens).mockResolvedValue([aToken({ name: 'gone', expiresAt: GONE })]);
			render(TokenSection);
			expect(await screen.findByText('No active tokens')).toBeInTheDocument();
			expect(screen.queryByText('gone')).not.toBeInTheDocument();
			await userEvent.click(view(/Expired/));
			expect(screen.getByText('gone')).toBeInTheDocument();
			expect(screen.queryByText('No active tokens')).not.toBeInTheDocument();
		});

		it('says when none has expired', async () => {
			vi.mocked(api.auth.tokens).mockResolvedValue([aToken({ name: 'ci' })]);
			render(TokenSection);
			await screen.findByText('ci');
			await userEvent.click(view(/Expired/));
			expect(screen.getByText('No expired tokens')).toBeInTheDocument();
			expect(screen.queryByText('ci')).not.toBeInTheDocument();
		});

		it('gives way to a token just minted', async () => {
			const tk = aToken({ id: 'new', name: 'fresh' });
			vi.mocked(api.auth.tokens).mockResolvedValue([aToken({ name: 'gone', expiresAt: GONE })]);
			vi.mocked(api.auth.createToken).mockResolvedValue({ token: tk, secret: 'apo_s3cret' });
			render(TokenSection);
			await screen.findByText('No active tokens');
			await userEvent.click(view(/Expired/));
			vi.mocked(api.auth.tokens).mockResolvedValue([aToken({ name: 'gone', expiresAt: GONE }), tk]);
			await userEvent.type(screen.getByRole('textbox', { name: 'Token name' }), 'fresh');
			await userEvent.click(submit());

			expect(await screen.findByText('fresh')).toBeInTheDocument();
			expect(view(/Active/)).toBeChecked();
			expect(screen.queryByText('gone')).not.toBeInTheDocument();
		});

		it('leaves the active ones when its time comes, the page still open', async () => {
			vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] });
			vi.setSystemTime(NOW);
			vi.mocked(api.auth.tokens).mockResolvedValue([
				aToken({ name: 'session', expiresAt: '2026-11-18T12:01:00Z' })
			]);
			const { unmount } = render(TokenSection);
			expect(await screen.findByText('expires in 1m')).toBeInTheDocument();

			vi.advanceTimersByTime(30_000);
			expect(await screen.findByText('expires in a moment')).toBeInTheDocument();
			vi.advanceTimersByTime(30_000);
			expect(await screen.findByText('No active tokens')).toBeInTheDocument();
			expect(view(/Expired/)).toHaveTextContent('1');

			// The clock stops with the page.
			unmount();
			expect(vi.getTimerCount()).toBe(0);
		});
	});

	it('says when there is no token', async () => {
		render(TokenSection);
		expect(await screen.findByText('No tokens yet')).toBeInTheDocument();
	});

	it('shows a failed read', async () => {
		vi.mocked(api.auth.tokens).mockRejectedValue(new Error('engine down'));
		render(TokenSection);
		expect(await screen.findByText('engine down')).toBeInTheDocument();
		expect(screen.getByText('The engine refused')).toBeInTheDocument();
	});

	it('mints a full-access token, shows its secret once and lists it', async () => {
		const tk = aToken({ name: 'ci' });
		vi.mocked(api.auth.createToken).mockResolvedValue({ token: tk, secret: 'apo_s3cret' });
		render(TokenSection);
		await screen.findByText('No tokens yet');
		vi.mocked(api.auth.tokens).mockResolvedValue([tk]);
		await userEvent.type(screen.getByRole('textbox', { name: 'Token name' }), 'ci');
		await userEvent.click(submit());

		expect(await screen.findByText('apo_s3cret')).toBeInTheDocument();
		expect(api.auth.createToken).toHaveBeenCalledWith('ci', { full: true });
		expect(await screen.findByText('ci')).toBeInTheDocument();
		expect(row('ci')).toHaveTextContent('Full access');
	});

	it('mints a limited token for chosen workshops', async () => {
		vi.mocked(api.auth.createToken).mockResolvedValue({
			token: aToken({ name: 'ci' }),
			secret: 'apo_s3cret'
		});
		render(TokenSection);
		await userEvent.type(screen.getByRole('textbox', { name: 'Token name' }), 'ci');
		await userEvent.click(screen.getByRole('radio', { name: 'Limited' }));
		await userEvent.click(await screen.findByRole('checkbox', { name: /run:operate/ }));
		await userEvent.click(screen.getByRole('checkbox', { name: 'billing' }));
		await userEvent.click(submit());

		await waitFor(() =>
			expect(api.auth.createToken).toHaveBeenCalledWith('ci', {
				full: false,
				actions: ['run:operate'],
				projectIds: ['p1']
			})
		);
		expect(await screen.findByText('apo_s3cret')).toBeInTheDocument();
	});

	it('shows an MCP client token with the configuration to connect it', async () => {
		vi.mocked(api.auth.createToken).mockResolvedValue({ token: aToken(), secret: 'apo_s3cret' });
		const writeText = vi.fn().mockResolvedValue(undefined);
		Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
		render(TokenSection);
		await userEvent.type(screen.getByRole('textbox', { name: 'Token name' }), 'agent');
		await userEvent.click(screen.getByRole('radio', { name: 'MCP client' }));
		await screen.findByText('run:read');
		await userEvent.click(submit());

		const url = `${location.origin}/mcp`;
		expect(
			await screen.findByText(new RegExp(`Connect your MCP client to ${url}`))
		).toBeInTheDocument();
		const config = JSON.parse(screen.getByText(/mcpServers/).textContent ?? '');
		expect(config.mcpServers.apothecary).toEqual({
			type: 'http',
			url,
			headers: { Authorization: 'Bearer apo_s3cret' }
		});
		expect(screen.queryByText(/Send it as a bearer token/)).not.toBeInTheDocument();
		const [, copyConfig] = screen.getAllByRole('button', { name: 'Copy' });
		await userEvent.click(copyConfig);
		expect(writeText).toHaveBeenCalledWith(expect.stringContaining('"mcpServers"'));
	});

	it('copies the secret, and dismisses it', async () => {
		vi.mocked(api.auth.createToken).mockResolvedValue({ token: aToken(), secret: 'apo_s3cret' });
		const writeText = vi.fn().mockResolvedValue(undefined);
		Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
		render(TokenSection);
		await userEvent.type(screen.getByRole('textbox', { name: 'Token name' }), 'ci');
		await userEvent.click(submit());
		await userEvent.click(await screen.findByRole('button', { name: 'Copy' }));

		expect(writeText).toHaveBeenCalledWith('apo_s3cret');
		expect(toasts.list.at(-1)?.text).toBe('Token copied');
		await userEvent.click(screen.getByRole('button', { name: /Dismiss/ }));
		expect(screen.queryByText('apo_s3cret')).not.toBeInTheDocument();
	});

	it('leaves the secret on screen when there is no clipboard', async () => {
		vi.mocked(api.auth.createToken).mockResolvedValue({ token: aToken(), secret: 'apo_s3cret' });
		const writeText = vi.fn().mockRejectedValue(new Error('denied'));
		Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
		render(TokenSection);
		await userEvent.type(screen.getByRole('textbox', { name: 'Token name' }), 'ci');
		await userEvent.click(submit());
		await userEvent.click(await screen.findByRole('button', { name: 'Copy' }));

		expect(writeText).toHaveBeenCalled();
		expect(toasts.list).toEqual([]);
		expect(screen.getByText('apo_s3cret')).toBeInTheDocument();
	});

	it('revokes a token after the confirm, dropping its shown secret', async () => {
		const tk = aToken({ name: 'ci' });
		vi.mocked(api.auth.createToken).mockResolvedValue({ token: tk, secret: 'apo_s3cret' });
		vi.mocked(api.auth.revokeToken).mockResolvedValue(undefined as never);
		render(TokenSection);
		await screen.findByText('No tokens yet');
		vi.mocked(api.auth.tokens).mockResolvedValue([tk]);
		await userEvent.type(screen.getByRole('textbox', { name: 'Token name' }), 'ci');
		await userEvent.click(submit());
		await screen.findByText('apo_s3cret');

		vi.mocked(api.auth.tokens).mockResolvedValue([]);
		await userEvent.click(within(row('ci')).getByRole('button', { name: 'Revoke' }));
		const asked = await confirmed(true);
		expect(asked.title).toBe('Revoke · ci');

		await waitFor(() => expect(api.auth.revokeToken).toHaveBeenCalledWith('t1'));
		await waitFor(() => expect(toasts.list.at(-1)?.text).toBe('Token revoked'));
		expect(screen.queryByText('apo_s3cret')).not.toBeInTheDocument();
		expect(await screen.findByText('No tokens yet')).toBeInTheDocument();
	});

	it('keeps the shown secret when another token is revoked', async () => {
		vi.mocked(api.auth.createToken).mockResolvedValue({
			token: aToken({ id: 'new', name: 'ci' }),
			secret: 'apo_s3cret'
		});
		vi.mocked(api.auth.revokeToken).mockResolvedValue(undefined as never);
		vi.mocked(api.auth.tokens).mockResolvedValue([aToken({ name: 'old' })]);
		render(TokenSection);
		await userEvent.type(screen.getByRole('textbox', { name: 'Token name' }), 'ci');
		await userEvent.click(submit());
		await screen.findByText('apo_s3cret');

		await userEvent.click(within(row('old')).getByRole('button', { name: 'Revoke' }));
		await confirmed(true);
		await waitFor(() => expect(api.auth.revokeToken).toHaveBeenCalledWith('t1'));
		expect(screen.getByText('apo_s3cret')).toBeInTheDocument();
	});

	it('does nothing when the revoke is declined', async () => {
		vi.mocked(api.auth.tokens).mockResolvedValue([aToken()]);
		render(TokenSection);
		await userEvent.click(await screen.findByRole('button', { name: 'Revoke' }));
		await confirmed(false);
		await Promise.resolve();
		expect(api.auth.revokeToken).not.toHaveBeenCalled();
	});

	it('shows a refused revoke', async () => {
		vi.mocked(api.auth.tokens).mockResolvedValue([aToken()]);
		vi.mocked(api.auth.revokeToken).mockRejectedValue(new Error('locked'));
		render(TokenSection);
		await userEvent.click(await screen.findByRole('button', { name: 'Revoke' }));
		await confirmed(true);
		expect(await screen.findByText('locked')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Revoke' })).not.toHaveAttribute('aria-busy', 'true');
	});

	it('opens and closes what a token did', async () => {
		vi.mocked(api.auth.tokens).mockResolvedValue([aToken()]);
		vi.mocked(api.auth.tokenCalls).mockResolvedValue([
			{
				at: '2026-11-18T11:00:00Z',
				method: 'POST',
				route: 'r',
				path: '/api/x',
				tool: 'create_ticket',
				status: 201
			}
		]);
		render(TokenSection);
		const calls = await screen.findByRole('button', { name: 'Calls' });
		expect(calls).toHaveAttribute('aria-expanded', 'false');
		await userEvent.click(calls);
		expect(await screen.findByText('create_ticket')).toBeInTheDocument();
		expect(api.auth.tokenCalls).toHaveBeenCalledWith('t1');
		await userEvent.click(calls);
		expect(screen.queryByText('create_ticket')).not.toBeInTheDocument();
	});
});
