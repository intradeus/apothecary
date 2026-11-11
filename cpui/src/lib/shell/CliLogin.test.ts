import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { CliLogin as Login } from '$lib/domain';
import { deferred } from '$test/helpers';
import CliLogin from './CliLogin.svelte';

vi.mock('$lib/api', () => ({
	api: { auth: { cliLogin: vi.fn(), approveCliLogin: vi.fn(), denyCliLogin: vi.fn() } },
	isNotFound: (e: unknown) => (e as { status?: number } | null)?.status === 404
}));

const LOGIN: Login = {
	userCode: 'ABCD-EFGH',
	name: 'apothecary CLI on laptop',
	expiresAt: '2026-11-11T09:10:00Z'
};
const refused = (status: number, message: string) => Object.assign(new Error(message), { status });
const approve = () => screen.getByRole('button', { name: 'Approve' });
const deny = () => screen.getByRole('button', { name: 'Deny' });

async function opened(code = 'abcd-efgh') {
	vi.mocked(api.auth.cliLogin).mockResolvedValue(LOGIN);
	render(CliLogin, { code });
	await screen.findByText('Sign in the apothecary CLI?');
}

describe('CliLogin', () => {
	beforeEach(() => vi.resetAllMocks());

	it('says it is looking the sign-in up', () => {
		vi.mocked(api.auth.cliLogin).mockReturnValue(deferred<Login>().promise);
		render(CliLogin, { code: 'ABCD-EFGH' });
		expect(screen.getByRole('status')).toHaveTextContent('Looking up the sign-in…');
	});

	it('shows what is signing in and the code to check against the terminal', async () => {
		await opened('  abcd-efgh ');
		expect(api.auth.cliLogin).toHaveBeenCalledWith('abcd-efgh');
		expect(screen.getByText('apothecary CLI on laptop')).toBeInTheDocument();
		expect(screen.getByLabelText('Sign-in code')).toHaveTextContent('ABCD-EFGH');
		expect(screen.getByText('Check that your terminal shows this code')).toBeInTheDocument();
		expect(approve()).toHaveClass('btn-primary');
		expect(deny()).toBeEnabled();
	});

	it('approves it by the code the engine knows, and says the terminal is ready', async () => {
		vi.mocked(api.auth.approveCliLogin).mockResolvedValue();
		await opened();
		await userEvent.click(approve());

		expect(api.auth.approveCliLogin).toHaveBeenCalledWith('ABCD-EFGH');
		expect(
			screen.getByText('Signed in — you can close this tab; the terminal is ready.')
		).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument();
	});

	it('denies it, and says the terminal stays signed out', async () => {
		vi.mocked(api.auth.denyCliLogin).mockResolvedValue();
		await opened();
		await userEvent.click(deny());

		expect(api.auth.denyCliLogin).toHaveBeenCalledWith('ABCD-EFGH');
		expect(api.auth.approveCliLogin).not.toHaveBeenCalled();
		expect(screen.getByText('Denied — the terminal stays signed out.')).toBeInTheDocument();
	});

	it('holds both buttons while the engine answers', async () => {
		const answer = deferred<void>();
		vi.mocked(api.auth.approveCliLogin).mockReturnValue(answer.promise);
		await opened();
		await userEvent.click(approve());

		expect(approve()).toBeDisabled();
		expect(approve()).toHaveAttribute('aria-busy', 'true');
		expect(deny()).toBeDisabled();
		answer.resolve();
		await screen.findByText(/^Signed in/);
	});

	it('shows a refusal beside the buttons and lets the person try again', async () => {
		vi.mocked(api.auth.approveCliLogin).mockRejectedValueOnce(
			refused(409, 'this sign-in was already answered')
		);
		await opened();
		await userEvent.click(approve());

		expect(screen.getByRole('alert')).toHaveTextContent('The engine refused');
		expect(screen.getByRole('alert')).toHaveTextContent('this sign-in was already answered');
		expect(approve()).toBeEnabled();
	});

	it('tells the person to start over when the code expired before they answered', async () => {
		vi.mocked(api.auth.denyCliLogin).mockRejectedValue(refused(404, 'expired'));
		await opened();
		await userEvent.click(deny());

		expect(screen.getByText('This sign-in expired or is unknown')).toBeInTheDocument();
		expect(screen.getByText(/Run apothecary login again/)).toBeInTheDocument();
	});

	it('tells the person to start over for an unknown or expired code', async () => {
		vi.mocked(api.auth.cliLogin).mockRejectedValue(refused(404, 'expired'));
		render(CliLogin, { code: 'ZZZZ-ZZZZ' });

		expect(await screen.findByText('This sign-in expired or is unknown')).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument();
	});

	it('does not ask the engine about a link without a code', () => {
		render(CliLogin, { code: '  ' });
		expect(api.auth.cliLogin).not.toHaveBeenCalled();
		expect(screen.getByText('This sign-in expired or is unknown')).toBeInTheDocument();
	});

	it('offers a retry when the engine cannot be asked, and recovers', async () => {
		vi.mocked(api.auth.cliLogin).mockRejectedValueOnce(new Error('engine down'));
		render(CliLogin, { code: 'ABCD-EFGH' });

		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent('engine down');

		vi.mocked(api.auth.cliLogin).mockResolvedValue(LOGIN);
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		await waitFor(() => expect(approve()).toBeInTheDocument());
		expect(screen.queryByRole('alert')).not.toBeInTheDocument();
	});
});
