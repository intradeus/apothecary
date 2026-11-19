import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { TokenCall } from '$lib/domain';
import TokenCalls from './TokenCalls.svelte';

vi.mock('$lib/api', () => ({ api: { auth: { tokenCalls: vi.fn() } } }));

const call = (over: Partial<TokenCall> = {}): TokenCall => ({
	at: '2026-11-18T11:00:00Z',
	method: 'POST',
	route: 'createWorkshopTicket',
	path: '/api/projects/p1/tickets',
	status: 201,
	...over
});

beforeEach(() => vi.resetAllMocks());

describe('TokenCalls', () => {
	it('lists a token’s calls: the tool, or the method and path; refusals marked', async () => {
		vi.mocked(api.auth.tokenCalls).mockResolvedValue([
			call({ tool: 'start_run', path: '/api/projects/p1/tickets/T-1/runs' }),
			call({ method: 'PUT', path: '/api/projects/p1/secrets/x', status: 403 })
		]);
		render(TokenCalls, { tokenId: 't1' });
		expect(await screen.findByText('start_run')).toHaveAttribute(
			'title',
			'POST /api/projects/p1/tickets/T-1/runs'
		);
		expect(screen.getByText('PUT /api/projects/p1/secrets/x')).toBeInTheDocument();
		expect(screen.getByText('403')).toHaveClass('tint-danger');
		expect(screen.getByText('201')).not.toHaveClass('tint-danger');
		expect(api.auth.tokenCalls).toHaveBeenCalledWith('t1');
	});

	it('says when it made none', async () => {
		vi.mocked(api.auth.tokenCalls).mockResolvedValue([]);
		render(TokenCalls, { tokenId: 't1' });
		expect(await screen.findByText('No calls yet')).toBeInTheDocument();
	});

	it('says when it cannot read them, and retries', async () => {
		vi.mocked(api.auth.tokenCalls)
			.mockRejectedValueOnce(new Error('engine down'))
			.mockResolvedValueOnce([call({ tool: 'get_run' })]);
		render(TokenCalls, { tokenId: 't1' });
		expect(await screen.findByText('engine down')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('get_run')).toBeInTheDocument();
	});
});
