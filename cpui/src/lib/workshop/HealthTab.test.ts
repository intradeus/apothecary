import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { BuildCache, EnvCheck, Preflight } from '$lib/domain';
import { confirmState } from '$lib/ui/confirm.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import { aWorkshop, confirmed } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import HealthTab from './HealthTab.svelte';

vi.mock('$lib/api', () => ({
	api: {
		projects: {
			preflight: vi.fn(),
			startEnvCheck: vi.fn(),
			envCheck: vi.fn(),
			cancelEnvCheck: vi.fn(),
			latestEnvCheck: vi.fn(),
			buildCache: vi.fn(),
			clearBuildCache: vi.fn()
		}
	}
}));

const project = aWorkshop({ id: 'p1' });
const check = (over: Partial<EnvCheck> = {}): EnvCheck => ({
	id: '01CHECKABCDEFGH',
	projectId: 'p1',
	status: 'pass',
	keep: false,
	tearingDown: false,
	steps: [],
	startedAt: '2026-10-31T12:00:00Z',
	finishedAt: '2026-10-31T12:01:30Z',
	...over
});
const noCache: BuildCache = { available: false, bytes: 0 };
const setup = () => render(HealthTab, { project });
const pane = (name: string) =>
	within(screen.getByRole('heading', { name }).closest('section') as HTMLElement);

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.projects.latestEnvCheck).mockResolvedValue(null as never);
	vi.mocked(api.projects.buildCache).mockResolvedValue(noCache);
	toasts.list = [];
	confirmState.current = null;
});
afterEach(() => {
	vi.useRealTimers();
});

describe('HealthTab, envcheck', () => {
	it('never starts a check on opening', async () => {
		setup();
		expect(await screen.findByText('Never checked')).toBeInTheDocument();
		expect(api.projects.startEnvCheck).not.toHaveBeenCalled();
	});

	it('treats an id-less answer as no check', async () => {
		vi.mocked(api.projects.latestEnvCheck).mockResolvedValue({ status: 'running' } as EnvCheck);
		setup();
		expect(await screen.findByText('Never checked')).toBeInTheDocument();
	});

	it('shows the latest check with its verdict, duration and id', async () => {
		vi.mocked(api.projects.latestEnvCheck).mockResolvedValue(
			check({
				steps: [
					{ id: 'image', status: 'pass' },
					{ id: 'firewall', status: 'fail', detail: 'blocked' },
					{ id: 'browser', status: 'skip', detail: 'off' },
					{ id: 'services', status: 'skip' }
				]
			})
		);
		setup();

		expect(await screen.findByText('all good')).toBeInTheDocument();
		expect(screen.getByText('1m 30s')).toBeInTheDocument();
		expect(screen.getByText('abcdefgh')).toBeInTheDocument();
		expect(screen.getByText('image')).toBeInTheDocument();
		expect(screen.getByText('blocked')).toBeInTheDocument();
		expect(screen.getByText('skipped: off')).toBeInTheDocument();
		expect(screen.getByText('skipped')).toBeInTheDocument();
	});

	it('takes up the keep setting of the check it found', async () => {
		vi.mocked(api.projects.latestEnvCheck).mockResolvedValue(check({ keep: true }));
		setup();
		expect(await screen.findByText('workspace kept')).toBeInTheDocument();
		expect(screen.getByRole('switch', { name: 'keep workspace' })).toBeChecked();
	});

	it('says when a finished check reported no steps', async () => {
		vi.mocked(api.projects.latestEnvCheck).mockResolvedValue(check());
		setup();
		expect(await screen.findByText('No steps were reported.')).toBeInTheDocument();
	});

	it('leaves out a date it cannot read', async () => {
		vi.mocked(api.projects.latestEnvCheck).mockResolvedValue(check({ startedAt: '' }));
		setup();
		await screen.findByText('all good');
		expect(screen.queryByText(/^started/)).not.toBeInTheDocument();
	});

	it('shows a failure to read the latest check', async () => {
		vi.mocked(api.projects.latestEnvCheck).mockRejectedValue(new Error('down'));
		setup();
		expect(await screen.findByText('Envcheck request failed')).toBeInTheDocument();
	});

	it('starts one, keeping the workspace if asked, and shows it', async () => {
		vi.mocked(api.projects.startEnvCheck).mockResolvedValue('c1');
		vi.mocked(api.projects.envCheck).mockResolvedValue(
			check({ status: 'running', finishedAt: undefined })
		);
		setup();
		await screen.findByText('Never checked');
		await userEvent.click(screen.getByRole('switch', { name: 'keep workspace' }));
		await userEvent.click(screen.getByRole('button', { name: /Run envcheck/ }));

		expect(api.projects.startEnvCheck).toHaveBeenCalledWith('p1', true);
		expect(await screen.findByText('running')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: /Restart/ })).toBeInTheDocument();
		expect(screen.getByRole('button', { name: /Cancel/ })).toBeInTheDocument();
	});

	it('shows a failure to start', async () => {
		vi.mocked(api.projects.startEnvCheck).mockRejectedValue(new Error('no capacity'));
		setup();
		await screen.findByText('Never checked');
		await userEvent.click(screen.getByRole('button', { name: /Run envcheck/ }));
		expect(await screen.findByText('Envcheck request failed')).toBeInTheDocument();
	});

	it('offers Run again after a finished check', async () => {
		vi.mocked(api.projects.latestEnvCheck).mockResolvedValue(check());
		setup();
		expect(await screen.findByRole('button', { name: /Run again/ })).toBeInTheDocument();
	});

	it('follows a live check until its teardown is done too', async () => {
		vi.useFakeTimers();
		const running = check({ id: 'c1', status: 'running', finishedAt: undefined });
		vi.mocked(api.projects.latestEnvCheck).mockResolvedValue(running);
		const verdict = check({ id: 'c1', tearingDown: true });
		vi.mocked(api.projects.envCheck)
			.mockResolvedValueOnce(running)
			.mockResolvedValueOnce(verdict)
			.mockResolvedValue(check({ id: 'c1' }));
		setup();
		await vi.advanceTimersByTimeAsync(0);
		expect(api.projects.envCheck).not.toHaveBeenCalled();

		await vi.advanceTimersByTimeAsync(2000);
		expect(api.projects.envCheck).toHaveBeenCalledTimes(1);

		await vi.advanceTimersByTimeAsync(2000);
		expect(screen.getByText('removing the workspace')).toBeInTheDocument();

		await vi.advanceTimersByTimeAsync(2000);
		expect(screen.queryByText('removing the workspace')).not.toBeInTheDocument();
		expect(api.projects.envCheck).toHaveBeenCalledTimes(3);

		await vi.advanceTimersByTimeAsync(10_000);
		expect(api.projects.envCheck).toHaveBeenCalledTimes(3);
	});

	it('keeps following through a failed poll', async () => {
		vi.useFakeTimers();
		vi.mocked(api.projects.latestEnvCheck).mockResolvedValue(
			check({ status: 'running', finishedAt: undefined })
		);
		vi.mocked(api.projects.envCheck)
			.mockRejectedValueOnce(new Error('blip'))
			.mockResolvedValue(check());
		setup();
		await vi.advanceTimersByTimeAsync(4000);
		expect(screen.getByText('all good')).toBeInTheDocument();
	});

	it('stops following when it leaves the page', async () => {
		vi.useFakeTimers();
		vi.mocked(api.projects.latestEnvCheck).mockResolvedValue(
			check({ status: 'running', finishedAt: undefined })
		);
		vi.mocked(api.projects.envCheck).mockResolvedValue(check({ status: 'running' }));
		const { unmount } = setup();
		await vi.advanceTimersByTimeAsync(0);
		unmount();
		await vi.advanceTimersByTimeAsync(10_000);
		expect(api.projects.envCheck).not.toHaveBeenCalled();
	});

	it('cancels the check under way and reads it back', async () => {
		vi.mocked(api.projects.latestEnvCheck).mockResolvedValue(
			check({ id: 'c1', status: 'running', finishedAt: undefined })
		);
		vi.mocked(api.projects.cancelEnvCheck).mockResolvedValue(undefined as never);
		vi.mocked(api.projects.envCheck).mockResolvedValue(check({ id: 'c1', status: 'cancelled' }));
		setup();
		await userEvent.click(await screen.findByRole('button', { name: /Cancel/ }));

		expect(api.projects.cancelEnvCheck).toHaveBeenCalledWith('c1');
		expect(await screen.findByText('cancelled')).toBeInTheDocument();
	});

	it('shows a failure to cancel', async () => {
		vi.mocked(api.projects.latestEnvCheck).mockResolvedValue(
			check({ status: 'running', finishedAt: undefined })
		);
		vi.mocked(api.projects.cancelEnvCheck).mockRejectedValue(new Error('gone'));
		setup();
		await userEvent.click(await screen.findByRole('button', { name: /Cancel/ }));
		expect(await screen.findByText('Envcheck request failed')).toBeInTheDocument();
	});
});

describe('HealthTab, preflight', () => {
	const run = () => screen.getByRole('button', { name: /Run preflight/ });

	it('waits to be asked', async () => {
		setup();
		expect(await screen.findByText('Not run yet')).toBeInTheDocument();
		expect(api.projects.preflight).not.toHaveBeenCalled();
	});

	it('holds the button while it checks', async () => {
		const answer = deferred<Preflight>();
		vi.mocked(api.projects.preflight).mockReturnValue(answer.promise);
		setup();
		await userEvent.click(run());

		expect(screen.getByRole('button', { name: 'Checking…' })).toBeDisabled();
		answer.resolve({ ok: true, steps: [] });
		expect(await screen.findByRole('button', { name: /Check again/ })).toBeEnabled();
	});

	it('shows each step and the tally', async () => {
		vi.mocked(api.projects.preflight).mockResolvedValue({
			ok: false,
			steps: [
				{ id: 'token', ok: true },
				{ id: 'docker', ok: false, error: 'daemon down' },
				{ id: 'flows', ok: false }
			]
		});
		setup();
		await userEvent.click(run());

		const p = pane('Preflight');
		expect(await p.findByText('1/3 ok')).toBeInTheDocument();
		expect(p.getByText('daemon down')).toBeInTheDocument();
		expect(p.getAllByText('failed')).toHaveLength(2);
	});

	it('reads a passing preflight as all good', async () => {
		vi.mocked(api.projects.preflight).mockResolvedValue({
			ok: true,
			steps: [{ id: 'token', ok: true }]
		});
		setup();
		await userEvent.click(run());
		expect(await pane('Preflight').findByText('all good')).toBeInTheDocument();
	});

	it('shows a failed request as a failed step', async () => {
		vi.mocked(api.projects.preflight).mockRejectedValue(new Error('engine down'));
		setup();
		await userEvent.click(run());
		expect(await screen.findByText('engine down')).toBeInTheDocument();
		expect(pane('Preflight').getByText('0/1 ok')).toBeInTheDocument();
	});
});

describe('HealthTab, the build cache', () => {
	const cached = (bytes: number) =>
		vi.mocked(api.projects.buildCache).mockResolvedValue({ available: true, bytes });
	const clear = () => screen.findByRole('button', { name: 'Clear…' });

	it('says when the provisioner keeps none', async () => {
		setup();
		expect(await screen.findByText('No cache here')).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Clear…' })).not.toBeInTheDocument();
	});

	it('shows what it holds', async () => {
		cached(2048);
		setup();
		expect(await screen.findByText('kept')).toBeInTheDocument();
		expect(screen.getByText('2 KB')).toBeInTheDocument();
		expect(await clear()).toBeEnabled();
	});

	it('has nothing to clear when empty', async () => {
		cached(0);
		setup();
		expect(await screen.findByText('empty')).toBeInTheDocument();
		expect(await clear()).toBeDisabled();
	});

	it('asks first, and does nothing on no', async () => {
		cached(2048);
		setup();
		await userEvent.click(await clear());
		await confirmed(false);
		await Promise.resolve();
		expect(api.projects.clearBuildCache).not.toHaveBeenCalled();
	});

	it('clears, reads it back and confirms', async () => {
		cached(2048);
		vi.mocked(api.projects.clearBuildCache).mockResolvedValue(undefined as never);
		setup();
		await userEvent.click(await clear());
		const asked = await confirmed(true);

		expect(asked.title).toBe('Clear the build cache?');
		expect(asked.body).toContain('2 KB');
		await waitFor(() => expect(toasts.list.at(-1)?.text).toBe('Build cache cleared'));
		expect(api.projects.buildCache).toHaveBeenCalledTimes(2);
	});

	it('shows a failure to clear', async () => {
		cached(2048);
		vi.mocked(api.projects.clearBuildCache).mockRejectedValue(new Error('busy'));
		setup();
		await userEvent.click(await clear());
		await confirmed(true);
		expect(await screen.findByText('Build cache request failed')).toBeInTheDocument();
		expect(toasts.list).toEqual([]);
	});

	it('offers a retry when reading fails', async () => {
		vi.mocked(api.projects.buildCache).mockRejectedValueOnce(new Error('down'));
		setup();
		expect(await screen.findByText('Build cache request failed')).toBeInTheDocument();
		expect(screen.queryByText('No cache here')).not.toBeInTheDocument();

		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('No cache here')).toBeInTheDocument();
	});
});
