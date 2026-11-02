import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { goto } from '$app/navigation';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { DeskState } from '$lib/desk/deskState.svelte';
import type { Run } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { aProject, aRun } from '$test/fixtures';
import { aFlow, settled } from '$test/fixtures/run-components';
import { aLiveRun, aViewStore } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import RunHeader from './RunHeader.svelte';

vi.mock('$lib/api', () => ({
	api: {
		runs: {
			get: vi.fn(),
			pause: vi.fn(),
			play: vi.fn(),
			retry: vi.fn(),
			abort: vi.fn(),
			archive: vi.fn(),
			rebuild: vi.fn(),
			openEditor: vi.fn(),
			ssh: vi.fn(),
			ports: vi.fn(),
			services: vi.fn(),
			mrStatus: vi.fn(),
			rerun: vi.fn()
		},
		files: { upload: vi.fn(), limits: vi.fn() },
		flows: { list: vi.fn(), get: vi.fn(), binding: vi.fn() },
		projects: { get: vi.fn(), mergeRequests: vi.fn() },
		integrations: { list: vi.fn(), models: vi.fn() },
		skillsets: { list: vi.fn(), versions: vi.fn() }
	}
}));

function setup(run: Run = aLiveRun({ state: 'paused' })) {
	vi.mocked(api.runs.get).mockResolvedValue({ run, events: [] });
	const rs = aViewStore(run);
	const desk = new DeskState();
	const onretried = vi.fn();
	render(RunHeader, { rs, desk, onretried });
	return { rs, desk, onretried, run };
}

const btn = (name: string | RegExp) => screen.getByRole('button', { name });
const env = () => btn(/^env/i);
const rebuild = () => btn(/^rebuild\W*$/i);

describe('RunHeader', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		toasts.list = [];
		vi.spyOn(fleet, 'refresh').mockResolvedValue();
		vi.mocked(api.runs.ssh).mockResolvedValue({ enabled: false, reason: 'Off here' } as never);
		vi.mocked(api.runs.ports).mockResolvedValue({ exposed: [], observed: [] } as never);
		vi.mocked(api.runs.services).mockResolvedValue({ workspace: true, services: [] });
		vi.mocked(api.runs.mrStatus).mockResolvedValue([]);
		vi.mocked(api.files.limits).mockResolvedValue(null as never);
		vi.mocked(api.flows.list).mockResolvedValue([aFlow({ name: 'builder' })]);
		vi.mocked(api.flows.get).mockResolvedValue(aFlow());
		vi.mocked(api.projects.get).mockResolvedValue(aProject());
		vi.mocked(api.integrations.list).mockResolvedValue([]);
		vi.mocked(api.skillsets.list).mockResolvedValue([]);
		vi.mocked(api.runs.rebuild).mockResolvedValue(undefined as never);
	});

	it("keeps the run's controls apart from the workspace's", () => {
		setup();
		expect(screen.getByRole('heading', { level: 1 })).toBeInTheDocument();
		expect(btn(/^play/i)).toBeEnabled();
		expect(env()).toBeEnabled();
		expect(btn(/^editor/i)).toBeEnabled();
	});

	it('shows the merge requests when the run has some', () => {
		const mrs = [{ repo: 'repo', iid: 20, url: 'https://git.test/20', branch: 'b' }];
		setup(aLiveRun({ mrs } as Partial<Run>));
		expect(screen.getByRole('link', { name: /^!20/ })).toBeInTheDocument();
	});

	describe('the Env menu', () => {
		it('stays shut until asked', () => {
			setup();
			expect(screen.queryByText('Environment')).not.toBeInTheDocument();
			expect(env()).toHaveAttribute('aria-expanded', 'false');
		});

		it('opens from its button, beside it', async () => {
			const { run } = setup();
			await userEvent.click(env());
			expect(screen.getByText('Environment')).toBeInTheDocument();
			expect(env()).toHaveAttribute('aria-expanded', 'true');
			expect(api.runs.ssh).toHaveBeenCalledWith(run.id);
			expect(await screen.findByText('Off here')).toBeInTheDocument();
		});

		it('cannot open with no workspace', () => {
			setup(aRun());
			expect(env()).toBeDisabled();
		});
	});

	describe('the rebuild menu', () => {
		it('opens from its button', async () => {
			setup();
			expect(rebuild()).toHaveAttribute('aria-expanded', 'false');
			await userEvent.click(rebuild());
			expect(screen.getByRole('button', { name: /^rebuild without cache/i })).toBeInTheDocument();
			expect(rebuild()).toHaveAttribute('aria-expanded', 'true');
		});

		it('holds the run and the workspace while a build runs', async () => {
			const job = deferred<never>();
			vi.mocked(api.runs.rebuild).mockReturnValue(job.promise);
			const { run } = setup();

			await userEvent.click(rebuild());
			await userEvent.click(btn(/^rebuild workspace/i));

			expect(api.runs.rebuild).toHaveBeenCalledWith(run.id, false);
			await waitFor(() => expect(btn(/^play/i)).toBeDisabled());
			expect(rebuild()).toHaveAttribute('aria-busy', 'true');

			job.resolve(undefined as never);
			await waitFor(() => expect(btn(/^play/i)).toBeEnabled());
		});

		it('goes through the store, so a failed build lands on the page', async () => {
			vi.mocked(api.runs.rebuild).mockRejectedValue(new Error('no image'));
			const { rs } = setup();
			await userEvent.click(rebuild());
			await userEvent.click(btn(/^rebuild workspace/i));
			await waitFor(() => expect(rs.error).toBe('no image'));
		});

		it('reaches the desk it was given', async () => {
			const { desk } = setup();
			const reveal = vi.spyOn(desk, 'reveal');
			await userEvent.click(rebuild());
			await userEvent.click(btn(/^rebuild workspace/i));
			toasts.list[0].action?.run();
			expect(reveal).toHaveBeenCalledWith('logs');
		});
	});

	it('tells the page a retry went through', async () => {
		vi.mocked(api.runs.retry).mockResolvedValue(undefined as never);
		const { onretried } = setup(aLiveRun({ state: 'failed' }));
		await userEvent.click(btn(/retry/i));
		await waitFor(() => expect(onretried).toHaveBeenCalledOnce());
	});

	describe('a new run from this one', () => {
		const failed = () => aLiveRun({ state: 'failed', projectId: 'p1' });
		const start = async () => {
			await userEvent.click(btn('New run from this…'));
			await settled();
			await userEvent.click(btn('Start new run'));
		};

		beforeEach(() => {
			vi.mocked(api.runs.rerun).mockResolvedValue('01NEWRUN00000000000000WXYZ');
		});

		it('opens the modal on this run', async () => {
			setup(failed());
			expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
			await userEvent.click(btn('New run from this…'));
			expect(screen.getByRole('dialog', { name: 'New run from r_abcd' })).toBeInTheDocument();
		});

		it('goes to the new run', async () => {
			setup(failed());
			await start();
			await waitFor(() => expect(goto).toHaveBeenCalledWith('/runs/01NEWRUN00000000000000WXYZ'));
		});

		it('says so, with a way back to this one', async () => {
			const { run } = setup(failed());
			await start();
			await waitFor(() => expect(toasts.list.length).toBeGreaterThan(0));

			const [started] = toasts.list;
			expect(started.text).toBe('New run r_wxyz started');
			expect(started.action?.label).toBe('Back to r_abcd');

			vi.mocked(goto).mockClear();
			started.action?.run();
			expect(goto).toHaveBeenCalledExactlyOnceWith(`/runs/${run.id}`);
		});
	});
});
