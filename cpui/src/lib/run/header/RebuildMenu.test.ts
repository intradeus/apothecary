import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { DeskState } from '$lib/desk/deskState.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import { aRun, aRunStore } from '$test/fixtures';
import { bound, settled } from '$test/fixtures/run-components';
import { deferred } from '$test/helpers';
import { HeaderActions, type HeaderAct } from './actions.svelte';
import RebuildMenu from './RebuildMenu.svelte';

vi.mock('$lib/api', () => ({ api: { runs: { rebuild: vi.fn() } } }));

function setup(over: Parameters<typeof aRun>[0] = {}, open = true) {
	const run = aRun({ state: 'paused', envId: 'env-1', ...over });
	const rs = aRunStore(run);
	const actions = new HeaderActions();
	const act: HeaderAct = (which, fn) => actions.run(which, () => rs.act(fn));
	const desk = { reveal: vi.fn() } as unknown as DeskState;
	const props = bound({ rs, desk, act, anchor: null, open });
	render(RebuildMenu, { props });
	return { run, rs, actions, desk, props };
}

const toastTexts = () => toasts.list.map((t) => t.text);
const rebuild = () => screen.getByRole('button', { name: /^rebuild workspace/i });
const noCache = () => screen.getByRole('button', { name: /^rebuild without cache/i });

describe('RebuildMenu', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		toasts.list = [];
		vi.mocked(api.runs.rebuild).mockResolvedValue(undefined as never);
	});

	it('stays out of the way while shut', () => {
		setup({}, false);
		expect(screen.queryByRole('button')).not.toBeInTheDocument();
	});

	it('says the config changed since the build', () => {
		setup({ envDirty: true });
		expect(screen.getByText('config changed since the build')).toBeInTheDocument();
	});

	it('reassures a clean workspace', () => {
		setup({ envDirty: false });
		expect(screen.getByText('the run is untouched')).toBeInTheDocument();
	});

	it.each([
		['from its cache', rebuild, false],
		['from scratch', noCache, true]
	])('rebuilds %s', async (_, button, cacheless) => {
		const { run } = setup();
		await userEvent.click(button());
		expect(api.runs.rebuild).toHaveBeenCalledExactlyOnceWith(run.id, cacheless);
	});

	it('closes as the build starts: the request blocks for the whole build', async () => {
		const job = deferred<never>();
		vi.mocked(api.runs.rebuild).mockReturnValue(job.promise);
		const { props, actions } = setup();

		await userEvent.click(rebuild());

		expect(props.open).toBe(false);
		expect(actions.has('rebuild')).toBe(true);
		job.resolve(undefined as never);
		await settled();
		expect(actions.has('rebuild')).toBe(false);
	});

	it.each([
		['Rebuild started', rebuild],
		['Rebuild started, without cache', noCache]
	])('announces "%s" before the build ends', async (text, button) => {
		vi.mocked(api.runs.rebuild).mockReturnValue(deferred<never>().promise);
		setup();
		await userEvent.click(button());
		expect(toastTexts()).toEqual([text]);
		expect(toasts.list[0].tone).toBe('info');
	});

	it('points the toast at the logs', async () => {
		const { desk } = setup();
		await userEvent.click(rebuild());
		const [started] = toasts.list;
		expect(started.action?.label).toBe('Watch logs');
		started.action?.run();
		expect(desk.reveal).toHaveBeenCalledWith('logs');
	});

	it('says so when the build is done', async () => {
		setup();
		await userEvent.click(rebuild());
		await waitFor(() => expect(toastTexts()).toContain('Workspace rebuilt'));
	});

	it('claims nothing when the build fails, and leaves the failure to the header', async () => {
		vi.mocked(api.runs.rebuild).mockRejectedValue(new Error('no image'));
		const { actions } = setup();
		await userEvent.click(rebuild());
		await settled();
		expect(toastTexts()).toEqual(['Rebuild started']);
		expect(actions.has('rebuild')).toBe(false);
	});
});
