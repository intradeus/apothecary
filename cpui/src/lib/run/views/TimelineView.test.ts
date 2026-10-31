import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fmtTime } from '$lib/core/format';
import { PROVISION_STAGE } from '$lib/run/derive';
import { aRun } from '$test/fixtures';
import { anEvent, aViewStore } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import TimelineView from './TimelineView.svelte';

vi.mock('$lib/api', () => ({
	api: {
		runs: { stageLog: vi.fn() },
		files: { snapshotUrl: (id: string) => `/snap/${id}` }
	}
}));

const RUN = '01TESTRUN0000000000000ABCD';
const failed = (seq: number, stage: string) =>
	anEvent(seq, 'stage_failed', { stage_id: stage, reason: 'exit' });
const logBtn = () => screen.getByRole('button', { name: 'log' });
const noLog = () => screen.queryByRole('button', { name: 'log' });

function show(events: ReturnType<typeof anEvent>[], run = aRun()) {
	render(TimelineView, { rs: aViewStore(run, { events }) });
}

describe('TimelineView', () => {
	beforeEach(() => {
		vi.resetAllMocks();
	});

	describe('the rows', () => {
		it('says it is connecting until there is something to show', () => {
			show([]);
			expect(screen.getByRole('status')).toHaveTextContent('Connecting…');
		});

		it('keeps the transcript out of the timeline', () => {
			show([
				anEvent(1, 'agent_message', { text: 'hello' }),
				anEvent(2, 'agent_tool', { tool: 'Read' }),
				anEvent(3, 'run_archived')
			]);
			expect(screen.getByText('archived')).toBeInTheDocument();
			expect(screen.queryByText(/agent/)).not.toBeInTheDocument();
		});

		it('states when each thing happened', () => {
			const e = anEvent(1, 'run_archived');
			show([e]);
			expect(screen.getByText(fmtTime(e.ts))).toBeInTheDocument();
		});

		it('says what happened, in order', () => {
			show([anEvent(1, 'env_destroyed'), anEvent(2, 'run_archived'), anEvent(3, 'some_new_event')]);
			const said = screen.getAllByText(/^(workspace destroyed|archived|some new event)$/);
			expect(said.map((e) => e.textContent)).toEqual([
				'workspace destroyed',
				'archived',
				'some new event'
			]);
		});

		it('follows the log as events land', async () => {
			const rs = aViewStore(aRun(), { events: [anEvent(1, 'run_archived')] });
			render(TimelineView, { rs });

			rs.events = [...rs.events, anEvent(2, 'env_destroyed')];

			expect(await screen.findByText('workspace destroyed')).toBeInTheDocument();
		});

		it('links a captured file', () => {
			show([
				anEvent(1, 'snapshot_captured', {
					snapshot_id: 'snap-1',
					kind: 'log',
					stage_id: 'build',
					iteration: 1
				})
			]);
			const link = screen.getByRole('link', { name: /open/ });
			expect(link).toHaveAttribute('href', '/snap/snap-1');
			expect(link).toHaveAttribute('target', '_blank');
		});
	});

	describe('which rows have a log', () => {
		it('offers it on a finished attempt', () => {
			show([anEvent(1, 'stage_completed', { stage_id: 'build', duration_ms: 1000 })]);
			expect(logBtn()).toBeInTheDocument();
		});

		it('offers it on a failed attempt', () => {
			show([failed(1, 'build')]);
			expect(logBtn()).toBeInTheDocument();
		});

		it('offers it on a paused attempt', () => {
			show([anEvent(1, 'stage_paused', { stage_id: 'build', reason: 'ask_human' })]);
			expect(logBtn()).toBeInTheDocument();
		});

		it('offers none on a start: the attempt has no log yet', () => {
			show([anEvent(1, 'stage_started', { stage_id: 'build', iteration: 1 })]);
			expect(noLog()).not.toBeInTheDocument();
		});

		it('offers none on a skipped stage', () => {
			show([anEvent(1, 'stage_completed', { stage_id: 'build', skipped: true })]);
			expect(noLog()).not.toBeInTheDocument();
		});

		it('offers the provisioning log once the workspace is ready', async () => {
			vi.mocked(api.runs.stageLog).mockResolvedValue('built');
			show([anEvent(1, 'env_ready', { env_id: 'env-1' })]);

			await userEvent.click(logBtn());

			expect(api.runs.stageLog).toHaveBeenCalledExactlyOnceWith(RUN, PROVISION_STAGE);
		});

		it('hangs the provisioning log on the request only if the run ended mid-build', () => {
			show([anEvent(1, 'env_requested')], aRun({ state: 'failed' }));
			expect(logBtn()).toBeInTheDocument();
		});

		it('offers none on the request while the build runs', () => {
			show([anEvent(1, 'env_requested')]);
			expect(noLog()).not.toBeInTheDocument();
		});

		it('offers none on the request when env_ready has its own', () => {
			show(
				[anEvent(1, 'env_requested'), anEvent(2, 'env_ready', { env_id: 'e' })],
				aRun({ state: 'failed' })
			);
			expect(screen.getAllByRole('button', { name: 'log' })).toHaveLength(1);
		});
	});

	describe('a log', () => {
		it('reads the stage’s log under its row', async () => {
			vi.mocked(api.runs.stageLog).mockResolvedValue('the output');
			show([failed(1, 'build')]);

			await userEvent.click(logBtn());

			expect(api.runs.stageLog).toHaveBeenCalledExactlyOnceWith(RUN, 'build');
			expect(await screen.findByText('the output')).toBeInTheDocument();
			expect(screen.getByRole('button', { name: 'hide log' })).toHaveAttribute(
				'aria-expanded',
				'true'
			);
		});

		it('says it is loading meanwhile', async () => {
			vi.mocked(api.runs.stageLog).mockReturnValue(deferred<string>().promise);
			show([failed(1, 'build')]);
			await userEvent.click(logBtn());
			expect(screen.getByText('loading…')).toBeInTheDocument();
		});

		it('says when it is empty', async () => {
			vi.mocked(api.runs.stageLog).mockResolvedValue('');
			show([failed(1, 'build')]);
			await userEvent.click(logBtn());
			expect(await screen.findByText('(empty log)')).toBeInTheDocument();
		});

		it('says why it could not be read', async () => {
			vi.mocked(api.runs.stageLog).mockRejectedValue(new Error('expired'));
			show([failed(1, 'build')]);
			await userEvent.click(logBtn());
			expect(await screen.findByText('no log: expired')).toBeInTheDocument();
		});

		it('closes on a second click, without reading again', async () => {
			vi.mocked(api.runs.stageLog).mockResolvedValue('the output');
			show([failed(1, 'build')]);
			await userEvent.click(logBtn());
			await userEvent.click(await screen.findByRole('button', { name: 'hide log' }));

			expect(screen.queryByText('the output')).not.toBeInTheDocument();
			expect(logBtn()).toHaveAttribute('aria-expanded', 'false');
			expect(api.runs.stageLog).toHaveBeenCalledOnce();
		});

		it('keeps one open at a time', async () => {
			vi.mocked(api.runs.stageLog).mockResolvedValueOnce('build output');
			vi.mocked(api.runs.stageLog).mockResolvedValueOnce('test output');
			show([failed(1, 'build'), failed(2, 'test')]);

			await userEvent.click(screen.getAllByRole('button', { name: 'log' })[0]);
			await screen.findByText('build output');
			await userEvent.click(logBtn());

			expect(await screen.findByText('test output')).toBeInTheDocument();
			expect(screen.queryByText('build output')).not.toBeInTheDocument();
		});

		it('does not let a slow log land under the row opened after it', async () => {
			const slow = deferred<string>();
			vi.mocked(api.runs.stageLog).mockReturnValueOnce(slow.promise);
			vi.mocked(api.runs.stageLog).mockResolvedValueOnce('test output');
			show([failed(1, 'build'), failed(2, 'test')]);

			await userEvent.click(screen.getAllByRole('button', { name: 'log' })[0]);
			await userEvent.click(logBtn());
			await screen.findByText('test output');

			slow.resolve('build output');
			await slow.promise;
			await tick();

			expect(screen.getByText('test output')).toBeInTheDocument();
		});
	});
});
