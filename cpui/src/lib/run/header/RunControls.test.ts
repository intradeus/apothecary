import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { api } from '$lib/api';
import { to } from '$lib/core/urls';
import { confirmDialog } from '$lib/ui/confirm.svelte';
import { toast } from '$lib/ui/toast.svelte';
import type { RunEvent } from '$lib/domain';
import { aRun, aRunStore } from '$test/fixtures';
import { deferred } from '$test/helpers';
import { HeaderActions, type HeaderAct } from './actions.svelte';
import RunControls from './RunControls.svelte';

vi.mock('$lib/api', () => ({
	api: { runs: { pause: vi.fn(), play: vi.fn(), retry: vi.fn(), abort: vi.fn(), archive: vi.fn() } }
}));
vi.mock('$lib/ui/confirm.svelte', () => ({ confirmDialog: vi.fn() }));
vi.mock('$lib/ui/toast.svelte', () => ({ toast: vi.fn() }));

function setup(state: Parameters<typeof aRun>[0]) {
	const rs = aRunStore(aRun(state));
	const actions = new HeaderActions();
	const act: HeaderAct = (which, fn) => actions.run(which, () => rs.act(fn));
	const onretried = vi.fn();
	render(RunControls, { rs, actions, act, onrerun: vi.fn(), onretried });
	return { rs, actions, onretried };
}

describe('RunControls', () => {
	it('pauses a live run', async () => {
		setup({ state: 'running' });
		await userEvent.click(screen.getByRole('button', { name: /pause/i }));
		expect(api.runs.pause).toHaveBeenCalledOnce();
	});

	it('plays a paused run', async () => {
		setup({ state: 'paused' });
		await userEvent.click(screen.getByRole('button', { name: /play|resume/i }));
		expect(api.runs.play).toHaveBeenCalledOnce();
	});

	it('offers to continue a run its budget paused, saying what tripped', async () => {
		setup({
			state: 'paused',
			pause: { reason: 'budget', note: 'budget exceeded: 12 turns > max 10' }
		});
		const play = screen.getByRole('button', { name: /continue past budget/i });
		expect(play).toHaveAttribute(
			'title',
			expect.stringContaining('budget exceeded: 12 turns > max 10')
		);
		await userEvent.click(play);
		expect(api.runs.play).toHaveBeenCalledOnce();
	});

	it('retries provisioning when the workspace never came up', () => {
		setup({ state: 'failed', provisionFailed: true, currentStage: 'build' });
		expect(screen.getByRole('button', { name: /retry provisioning/i })).toHaveAttribute(
			'title',
			expect.stringContaining('provision it again')
		);
	});

	it('holds the run buttons while any header action runs', async () => {
		const { actions } = setup({ state: 'running' });
		const build = deferred<boolean>();
		const running = actions.run('rebuild', () => build.promise);

		await vi.waitFor(() => expect(screen.getByRole('button', { name: /pause/i })).toBeDisabled());

		build.resolve(true);
		await running;
		await vi.waitFor(() => expect(screen.getByRole('button', { name: /pause/i })).toBeEnabled());
	});

	it('tells the page a retry went through, and only then', async () => {
		const { onretried, rs } = setup({ state: 'failed' });
		vi.mocked(api.runs.retry).mockRejectedValueOnce(new Error('nope'));
		await userEvent.click(screen.getByRole('button', { name: /retry/i }));
		expect(rs.act).toHaveBeenCalledOnce();
		expect(onretried).not.toHaveBeenCalled();

		vi.mocked(api.runs.retry).mockResolvedValueOnce(undefined as never);
		await userEvent.click(screen.getByRole('button', { name: /retry/i }));
		expect(onretried).toHaveBeenCalledOnce();
	});
});

// Stop and archive are destructive: each asks first, and the question names
// the workspace that is kept (stop) or destroyed (archive) when there is one.
describe('RunControls: stop', () => {
	it('asks, naming the workspace it keeps, then aborts', async () => {
		vi.mocked(confirmDialog).mockResolvedValue(true);
		setup({ state: 'running', envId: 'env-1' });
		await userEvent.click(screen.getByRole('button', { name: /abort/i }));
		expect(vi.mocked(confirmDialog).mock.calls[0][0].body).toContain('env-1');
		expect(api.runs.abort).toHaveBeenCalledOnce();
	});

	it('says there is no workspace, and does nothing when the person backs out', async () => {
		vi.mocked(confirmDialog).mockResolvedValue(false);
		setup({ state: 'running' });
		await userEvent.click(screen.getByRole('button', { name: /abort/i }));
		expect(vi.mocked(confirmDialog).mock.calls[0][0].body).not.toContain('env-');
		expect(api.runs.abort).not.toHaveBeenCalled();
	});
});

describe('RunControls: archive', () => {
	it('asks, naming the workspace it destroys, then archives and offers the ticket', async () => {
		vi.mocked(confirmDialog).mockResolvedValue(true);
		vi.mocked(api.runs.archive).mockResolvedValue(undefined as never);
		setup({ state: 'completed', envId: 'env-1', ticketId: 'tk-1' });
		await userEvent.click(screen.getByRole('button', { name: /archive/i }));
		expect(vi.mocked(confirmDialog).mock.calls[0][0].body).toContain('env-1');
		expect(api.runs.archive).toHaveBeenCalledOnce();

		const [, opts] = vi.mocked(toast).mock.calls[0];
		opts!.action!.run();
		expect(goto).toHaveBeenCalledWith(to.ticket('tk-1'));
	});

	it('offers no ticket when the run has none, and no toast when the engine refused', async () => {
		vi.mocked(confirmDialog).mockResolvedValue(true);
		vi.mocked(api.runs.archive).mockRejectedValueOnce(new Error('nope'));
		setup({ state: 'failed' });
		await userEvent.click(screen.getByRole('button', { name: /archive/i }));
		expect(toast).not.toHaveBeenCalled();

		vi.mocked(api.runs.archive).mockResolvedValueOnce(undefined as never);
		await userEvent.click(screen.getByRole('button', { name: /archive/i }));
		expect(vi.mocked(toast).mock.calls[0][1]).toEqual({ action: undefined });
	});

	it('is not offered once the run is archived', () => {
		setup({ state: 'archived' });
		expect(screen.queryByRole('button', { name: /archive/i })).not.toBeInTheDocument();
	});
});

// With no current stage, a retry names the stage the log says failed.
describe('RunControls: retry stage', () => {
	it('reads the failed stage off the log', () => {
		const failed = { type: 'stage_failed', payload: { stage_id: 'build' } } as unknown as RunEvent;
		const rs = aRunStore(aRun({ state: 'failed' }), { events: [failed] });
		const actions = new HeaderActions();
		const act: HeaderAct = (which, fn) => actions.run(which, () => rs.act(fn));
		render(RunControls, { rs, actions, act, onrerun: vi.fn(), onretried: vi.fn() });
		expect(screen.getByRole('button', { name: /retry/i })).toHaveTextContent(/build/);
	});
});
