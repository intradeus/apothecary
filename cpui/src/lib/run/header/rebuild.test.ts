import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { toasts } from '$lib/ui/toast.svelte';
import { aRun, aRunStore } from '$test/fixtures';
import { settled } from '$test/fixtures/run-components';
import { deferred } from '$test/helpers';
import { HeaderActions, type HeaderAct } from './actions.svelte';
import { rebuild } from './rebuild';

vi.mock('$lib/api', () => ({ api: { runs: { rebuild: vi.fn() } } }));

function setup() {
	const run = aRun({ state: 'paused', envId: 'env-1' });
	const rs = aRunStore(run);
	const actions = new HeaderActions();
	const act: HeaderAct = (which, fn) => actions.run(which, () => rs.act(fn));
	const desk = { reveal: vi.fn() };
	return { run, rs, actions, act, desk };
}

const toastTexts = () => toasts.list.map((t) => t.text);

describe('rebuild', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		toasts.list = [];
		vi.mocked(api.runs.rebuild).mockResolvedValue(undefined as never);
	});

	it.each([false, true])('asks the engine for a rebuild (noCache %s) through act', async (nc) => {
		const { run, rs, act, desk } = setup();
		expect(await rebuild(run.id, nc, { act, desk })).toBe(true);
		expect(api.runs.rebuild).toHaveBeenCalledExactlyOnceWith(run.id, nc);
		expect(rs.act).toHaveBeenCalledOnce();
	});

	it('holds the rebuild action for the whole build', async () => {
		const job = deferred<never>();
		vi.mocked(api.runs.rebuild).mockReturnValue(job.promise);
		const { run, actions, act, desk } = setup();

		const done = rebuild(run.id, false, { act, desk });
		expect(actions.has('rebuild')).toBe(true);
		job.resolve(undefined as never);
		await done;
		expect(actions.has('rebuild')).toBe(false);
	});

	it.each([
		['Rebuild started', false],
		['Rebuild started, without cache', true]
	])('announces "%s" before the build ends', async (text, nc) => {
		vi.mocked(api.runs.rebuild).mockReturnValue(deferred<never>().promise);
		const { run, act, desk } = setup();
		void rebuild(run.id, nc, { act, desk });
		await settled();
		expect(toastTexts()).toEqual([text]);
		expect(toasts.list[0].tone).toBe('info');
	});

	it('points the toast at the logs', async () => {
		const { run, act, desk } = setup();
		await rebuild(run.id, false, { act, desk });
		const [started] = toasts.list;
		expect(started.action?.label).toBe('Watch logs');
		started.action?.run();
		expect(desk.reveal).toHaveBeenCalledWith('logs');
	});

	it('says so when the build is done', async () => {
		const { run, act, desk } = setup();
		await rebuild(run.id, false, { act, desk });
		expect(toastTexts()).toEqual(['Rebuild started', 'Workspace rebuilt']);
	});

	it('claims nothing when the build fails, and leaves the failure to the store', async () => {
		vi.mocked(api.runs.rebuild).mockRejectedValue(new Error('no image'));
		const { run, rs, actions, act, desk } = setup();
		expect(await rebuild(run.id, false, { act, desk })).toBe(false);
		expect(toastTexts()).toEqual(['Rebuild started']);
		expect(rs.act).toHaveBeenCalledOnce();
		expect(actions.has('rebuild')).toBe(false);
	});
});
