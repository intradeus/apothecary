import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import type { Notice } from '$lib/domain';
import Notices from './Notices.svelte';

vi.mock('$lib/api', () => ({
	api: {
		notices: { list: vi.fn(), dismiss: vi.fn(), dismissAll: vi.fn() },
		pings: { pending: vi.fn() },
		runs: { list: vi.fn() },
		projects: { list: vi.fn() },
		tickets: { list: vi.fn() },
		system: { limits: vi.fn() }
	}
}));

const notice = (over: Partial<Notice> = {}): Notice => ({
	id: 'n1',
	kind: 'channel_failing',
	title: 'Your slack channel is failing',
	body: 'Notifications sent to it are not arriving: refused',
	link: '/system/channels',
	count: 1,
	createdAt: new Date().toISOString(),
	lastSeenAt: new Date().toISOString(),
	...over
});

/** The fleet as a poll left it; the next poll answers `next`. */
function loaded(list: Notice[], next = list) {
	fleet.notices = list;
	fleet.updatedAt = Date.now();
	vi.mocked(api.notices.list).mockResolvedValue(next);
}

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.runs.list).mockResolvedValue([]);
	vi.mocked(api.pings.pending).mockResolvedValue([]);
	vi.mocked(api.projects.list).mockResolvedValue([]);
	vi.mocked(api.tickets.list).mockResolvedValue([]);
	vi.mocked(api.system.limits).mockResolvedValue(null as never);
	vi.mocked(api.notices.list).mockResolvedValue([]);
	fleet.reset();
});

describe('Notices', () => {
	it('claims nothing until the fleet has landed', () => {
		render(Notices);
		expect(screen.queryByText('Nothing needs your attention.')).not.toBeInTheDocument();
	});

	it('says so when nothing needs attention', () => {
		loaded([]);
		render(Notices);
		expect(screen.getByText('Nothing needs your attention.')).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Dismiss all' })).not.toBeInTheDocument();
	});

	it('shows each notice with its body and repeat count', () => {
		loaded([notice({ count: 3 }), notice({ id: 'n2', title: 'Sign in again: work' })]);
		render(Notices);
		expect(screen.getByText('Your slack channel is failing')).toBeInTheDocument();
		expect(screen.getByText('Sign in again: work')).toBeInTheDocument();
		expect(screen.getAllByText(/refused/)).toHaveLength(2);
		expect(screen.getByText(/×3/)).toBeInTheDocument();
	});

	it('dismisses one and refreshes', async () => {
		vi.mocked(api.notices.dismiss).mockResolvedValue(undefined);
		loaded([notice()], []);
		render(Notices);
		await userEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
		expect(api.notices.dismiss).toHaveBeenCalledWith('n1');
		await waitFor(() =>
			expect(screen.getByText('Nothing needs your attention.')).toBeInTheDocument()
		);
	});

	it('says why a dismiss failed', async () => {
		vi.mocked(api.notices.dismiss).mockRejectedValue(new Error('engine down'));
		loaded([notice()]);
		render(Notices);
		await userEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
		expect(await screen.findByText('engine down')).toBeInTheDocument();
	});

	it('dismisses them all', async () => {
		vi.mocked(api.notices.dismissAll).mockResolvedValue(undefined);
		loaded([notice(), notice({ id: 'n2' })], []);
		render(Notices);
		await userEvent.click(screen.getByRole('button', { name: 'Dismiss all' }));
		expect(api.notices.dismissAll).toHaveBeenCalled();
		await waitFor(() =>
			expect(screen.getByText('Nothing needs your attention.')).toBeInTheDocument()
		);
	});

	it('opens where it can be fixed only when it has a link', async () => {
		loaded([notice(), notice({ id: 'n2', title: 'No link', link: '' })]);
		render(Notices);
		const open = screen.getAllByRole('button', { name: 'Open' });
		expect(open).toHaveLength(1);
		await userEvent.click(open[0]);
		expect(goto).toHaveBeenCalledWith('/system/channels');
	});
});
