import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { aRun, aRunStore } from '$test/fixtures';
import { deferred } from '$test/helpers';
import { HeaderActions, type HeaderAct } from './actions.svelte';
import RunControls from './RunControls.svelte';

vi.mock('$lib/api', () => ({
	api: { runs: { pause: vi.fn(), play: vi.fn(), retry: vi.fn(), abort: vi.fn(), archive: vi.fn() } }
}));

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
