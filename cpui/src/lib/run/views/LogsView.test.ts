import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { PROVISION_STAGE } from '$lib/run/derive';
import type { LogRange } from '$lib/domain';
import { aRun } from '$test/fixtures';
import { aLiveRun, anEvent, aViewStore } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import LogsView from './LogsView.svelte';

vi.mock('$lib/api', () => ({ api: { runs: { workspaceLog: vi.fn(), stageLog: vi.fn() } } }));

const RUN = '01TESTRUN0000000000000ABCD';
/** A range of the log: `text`, starting at byte `start` of a log that ends with it. */
const logOf = (text: string, start = 0, size = start + text.length): LogRange => ({
	text,
	start,
	end: start + text.length,
	size
});
const provisioned = anEvent(1, 'env_requested');
const stageEvents = [
	anEvent(2, 'stage_completed', { stage_id: 'plan' }),
	anEvent(3, 'stage_failed', { stage_id: 'build' })
];
const live = (over: Parameters<typeof aViewStore>[1] = {}) => aViewStore(aLiveRun(), over);
const source = (name: string | RegExp) => screen.getByRole('radio', { name });
const reload = () => screen.queryByRole('button', { name: 'Reload' });

async function open(rs = live()) {
	const view = render(LogsView, { rs, visible: true });
	// The first read, and the scroll to the bottom it ends with, settle.
	for (let i = 0; i < 4; i++) await tick();
	return { ...view, rs };
}

describe('LogsView', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf(''));
		vi.mocked(api.runs.stageLog).mockResolvedValue('');
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	describe('states', () => {
		it('loads nothing while hidden', () => {
			render(LogsView, { rs: live(), visible: false });
			expect(api.runs.workspaceLog).not.toHaveBeenCalled();
			expect(screen.getByText('Nothing yet.')).toBeInTheDocument();
		});

		it('says it is connecting until the log lands', async () => {
			const log = deferred<LogRange | null>();
			vi.mocked(api.runs.workspaceLog).mockReturnValue(log.promise);
			render(LogsView, { rs: live(), visible: true });

			expect(screen.getByRole('status')).toHaveTextContent('Connecting…');
			log.resolve(logOf('booted'));
			expect(await screen.findByText('booted')).toBeInTheDocument();
			expect(screen.queryByRole('status')).not.toBeInTheDocument();
		});

		it('tells apart a provisioner that streams nothing', async () => {
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(null);
			await open();
			expect(
				screen.getByText('No live output here — this provisioner does not stream it.')
			).toBeInTheDocument();
		});

		it('says the workspace has written nothing yet', async () => {
			await open();
			expect(screen.getByText(/^No live output yet/)).toBeInTheDocument();
		});

		it('says a stage log is empty', async () => {
			await open(live({ logSource: 'plan', events: stageEvents }));
			expect(screen.getByText('The log is empty.')).toBeInTheDocument();
		});

		it('says why a log could not be read', async () => {
			vi.mocked(api.runs.stageLog).mockRejectedValue(new Error('no such stage'));
			await open(live({ logSource: 'plan', events: stageEvents }));
			expect(await screen.findByText('No log: no such stage')).toBeInTheDocument();
		});

		it('recovers from a failed read on reload', async () => {
			vi.mocked(api.runs.stageLog).mockRejectedValueOnce(new Error('no such stage'));
			await open(live({ logSource: 'plan', events: stageEvents }));
			await screen.findByText('No log: no such stage');

			vi.mocked(api.runs.stageLog).mockResolvedValue('plan output');
			await userEvent.click(reload()!);

			expect(await screen.findByText('plan output')).toBeInTheDocument();
			expect(screen.queryByText('No log: no such stage')).not.toBeInTheDocument();
		});

		it('says so when a reload asked for fails', async () => {
			vi.mocked(api.runs.stageLog).mockResolvedValueOnce('plan output');
			await open(live({ logSource: 'plan', events: stageEvents }));
			await screen.findByText('plan output');

			vi.mocked(api.runs.stageLog).mockRejectedValue(new Error('no such stage'));
			await userEvent.click(reload()!);

			expect(await screen.findByText('No log: no such stage')).toBeInTheDocument();
		});
	});

	describe('the lines', () => {
		it('shows one row per line, without a trailing blank', async () => {
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf('one\ntwo\n'));
			await open();
			const first = screen.getByText('one');
			expect(first.parentElement?.children).toHaveLength(2);
		});

		it.each([
			['ERROR: disk full', 'hue-danger'],
			['request denied', 'hue-danger'],
			['build FAILED', 'hue-danger'],
			['tunnel up', 'hue-accent-2'],
			['harness started', 'hue-running'],
			['shim ready', 'hue-accent']
		])('tints %s by its source', async (line, hue) => {
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf(line));
			await open();
			expect(screen.getByText(line)).toHaveClass(hue);
		});

		it('leaves plain output untinted, and only matches whole words', async () => {
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf('terrorised shimmer'));
			await open();
			expect(screen.getByText('terrorised shimmer').className).not.toMatch(/hue-/);
		});

		it('lets a failure outrank its source', async () => {
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf('tunnel failed'));
			await open();
			expect(screen.getByText('tunnel failed')).toHaveClass('hue-danger');
		});
	});

	describe('sources', () => {
		it('offers only the workspace before anything else exists', async () => {
			await open();
			expect(screen.getAllByRole('radio').map((r) => r.textContent)).toEqual(['Workspace · live']);
		});

		it('offers provisioning once it was asked for, and stages once one ended', async () => {
			await open(live({ events: [provisioned, ...stageEvents] }));
			expect(screen.getAllByRole('radio').map((r) => r.textContent)).toEqual([
				'Workspace · live',
				'Provisioning',
				'Stage'
			]);
		});

		it('drops "live" on a finished run', async () => {
			await open(aViewStore(aRun({ state: 'completed' })));
			expect(source('Workspace')).toBeInTheDocument();
		});

		it('reads the workspace log by default', async () => {
			await open();
			expect(api.runs.workspaceLog).toHaveBeenCalledExactlyOnceWith(RUN, { n: 2 << 20 });
			expect(api.runs.stageLog).not.toHaveBeenCalled();
			expect(source('Workspace · live')).toBeChecked();
		});

		it('reads the provisioning log under its pseudo-stage', async () => {
			const { rs } = await open(live({ events: [provisioned] }));
			await userEvent.click(source('Provisioning'));

			expect(rs.logSource).toBe(PROVISION_STAGE);
			expect(api.runs.stageLog).toHaveBeenCalledExactlyOnceWith(RUN, PROVISION_STAGE);
		});

		it('opens the latest stage first', async () => {
			await open(live({ events: stageEvents }));
			await userEvent.click(source('Stage'));
			expect(api.runs.stageLog).toHaveBeenCalledExactlyOnceWith(RUN, 'build');
		});

		it('lists each stage once, however many attempts it took', async () => {
			const again = anEvent(4, 'stage_completed', { stage_id: 'plan' });
			await open(live({ logSource: 'plan', events: [...stageEvents, again] }));

			await userEvent.click(screen.getByRole('combobox', { name: 'Stage' }));

			expect(within(screen.getByRole('listbox')).getAllByRole('option')).toHaveLength(2);
		});

		it('switches stage from the picker', async () => {
			const { rs } = await open(live({ logSource: 'plan', events: stageEvents }));

			await userEvent.click(screen.getByRole('combobox', { name: 'Stage' }));
			await userEvent.click(screen.getByRole('option', { name: 'build' }));

			expect(rs.logSource).toBe('build');
			expect(api.runs.stageLog).toHaveBeenLastCalledWith(RUN, 'build');
		});

		it('shows no stage picker on the workspace', async () => {
			await open(live({ events: stageEvents }));
			expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
		});

		it('follows a stage opened from elsewhere', async () => {
			const { rs } = await open(live({ events: stageEvents }));
			rs.logSource = 'plan';
			await tick();

			expect(source('Stage')).toBeChecked();
			expect(api.runs.stageLog).toHaveBeenCalledWith(RUN, 'plan');
		});

		it('drops the answer of a source left behind', async () => {
			const old = deferred<string>();
			vi.mocked(api.runs.stageLog).mockReturnValueOnce(old.promise);
			const { rs } = await open(live({ logSource: 'plan', events: stageEvents }));

			vi.mocked(api.runs.stageLog).mockResolvedValue('build output');
			rs.logSource = 'build';
			await screen.findByText('build output');
			old.resolve('plan output');
			await old.promise;
			await tick();

			expect(screen.getByText('build output')).toBeInTheDocument();
			expect(screen.queryByText('plan output')).not.toBeInTheDocument();
		});

		it('drops the failure of a source left behind', async () => {
			const old = deferred<string>();
			vi.mocked(api.runs.stageLog).mockReturnValueOnce(old.promise);
			const { rs } = await open(live({ logSource: 'plan', events: stageEvents }));

			vi.mocked(api.runs.stageLog).mockResolvedValue('build output');
			rs.logSource = 'build';
			await screen.findByText('build output');
			old.reject(new Error('gone'));
			await old.promise.catch(() => {});
			await tick();

			expect(screen.getByText('build output')).toBeInTheDocument();
		});
	});

	describe('reload and tail', () => {
		beforeEach(() => {
			vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
		});

		it('tails a live workspace instead of offering a reload', async () => {
			await open();
			expect(screen.getByText('tail -f')).toBeInTheDocument();
			expect(reload()).not.toBeInTheDocument();
		});

		it('reads the live tail every 2.5s', async () => {
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf('first'));
			await open();
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf('\nsecond', 5));

			await vi.advanceTimersByTimeAsync(2500);

			// Only what was written since: the read starts where the last ended.
			expect(api.runs.workspaceLog).toHaveBeenLastCalledWith(RUN, { from: 5, n: 1 << 20 });
			expect(screen.getByText('first')).toBeInTheDocument();
			expect(screen.getByText('second')).toBeInTheDocument();
		});

		it('reads the tail again when the log was replaced', async () => {
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf('old workspace'));
			await open();
			vi.mocked(api.runs.workspaceLog)
				.mockResolvedValueOnce(logOf('', 3, 3))
				.mockResolvedValueOnce(logOf('new'));

			await vi.advanceTimersByTimeAsync(2500);

			expect(api.runs.workspaceLog).toHaveBeenLastCalledWith(RUN, { n: 2 << 20 });
			expect(screen.getByText('new')).toBeInTheDocument();
			expect(screen.queryByText('old workspace')).not.toBeInTheDocument();
		});

		it('does not read twice while a slow read is out', async () => {
			await open();
			vi.mocked(api.runs.workspaceLog).mockReturnValue(deferred<LogRange>().promise);
			await vi.advanceTimersByTimeAsync(7500);
			expect(api.runs.workspaceLog).toHaveBeenCalledTimes(2);
		});

		it('says so when the stream goes away', async () => {
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf('first'));
			await open();
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(null);
			await vi.advanceTimersByTimeAsync(2500);
			expect(
				screen.getByText('No live output here — this provisioner does not stream it.')
			).toBeInTheDocument();
		});

		it('keeps the old lines on screen between reads', async () => {
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf('first'));
			await open();
			vi.mocked(api.runs.workspaceLog).mockReturnValue(deferred<LogRange>().promise);

			await vi.advanceTimersByTimeAsync(2500);

			expect(screen.getByText('first')).toBeInTheDocument();
		});

		it('skips while the tab is in the background', async () => {
			await open();
			vi.spyOn(document, 'hidden', 'get').mockReturnValue(true);
			await vi.advanceTimersByTimeAsync(2500);
			expect(api.runs.workspaceLog).toHaveBeenCalledOnce();
		});

		it('stops when the window hides, and reads again when it returns', async () => {
			const { rerender } = await open();
			await rerender({ visible: false });
			await vi.advanceTimersByTimeAsync(5000);
			expect(api.runs.workspaceLog).toHaveBeenCalledOnce();

			await rerender({ visible: true });
			expect(api.runs.workspaceLog).toHaveBeenCalledTimes(2);
		});

		it('stops tailing on another source', async () => {
			const { rs } = await open(live({ events: stageEvents }));
			rs.logSource = 'plan';
			await tick();
			await vi.advanceTimersByTimeAsync(5000);
			expect(api.runs.workspaceLog).toHaveBeenCalledOnce();
		});

		it('offers a reload on a stage log, which does not tail', async () => {
			vi.mocked(api.runs.stageLog).mockResolvedValue('plan output');
			await open(live({ logSource: 'plan', events: stageEvents }));
			await vi.advanceTimersByTimeAsync(5000);
			expect(api.runs.stageLog).toHaveBeenCalledOnce();

			await userEvent.click(reload()!);
			expect(api.runs.stageLog).toHaveBeenCalledTimes(2);
		});

		it('offers a reload on a finished run, and does not tail it', async () => {
			await open(aViewStore(aLiveRun({ state: 'failed' })));
			await vi.advanceTimersByTimeAsync(5000);

			expect(api.runs.workspaceLog).toHaveBeenCalledOnce();
			expect(reload()).toBeInTheDocument();
			expect(screen.queryByText('tail -f')).not.toBeInTheDocument();
		});

		// The clone and the build stream into the same log before the
		// workspace exists.
		it('tails a provisioning run', async () => {
			await open(aViewStore(aRun({ state: 'provisioning' })));
			await vi.advanceTimersByTimeAsync(5000);
			expect(api.runs.workspaceLog).toHaveBeenCalledTimes(3);
			expect(screen.getByText('tail -f')).toBeInTheDocument();
		});

		it('does not tail before provisioning starts', async () => {
			await open(aViewStore(aRun({ state: 'pending' })));
			await vi.advanceTimersByTimeAsync(5000);
			expect(api.runs.workspaceLog).toHaveBeenCalledOnce();
		});

		it('starts tailing when the workspace appears', async () => {
			const { rs } = await open(aViewStore(aRun({ state: 'pending' })));
			rs.run = aLiveRun();
			await tick();
			await vi.advanceTimersByTimeAsync(2500);
			expect(api.runs.workspaceLog).toHaveBeenCalledTimes(3);
		});

		it('does not claim to tail when there is no workspace to tail', async () => {
			await open(aViewStore(aRun({ state: 'paused' })));
			expect(screen.queryByText('tail -f')).not.toBeInTheDocument();
		});

		it('keeps the lines when one read of the tail fails', async () => {
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf('first'));
			await open();
			vi.mocked(api.runs.workspaceLog).mockRejectedValue(new Error('blip'));

			await vi.advanceTimersByTimeAsync(2500);

			expect(screen.getByText('first')).toBeInTheDocument();
		});
	});

	describe('going back', () => {
		const earlier = () => screen.queryByRole('button', { name: /^Load earlier output/ });

		it('offers what came before the tail, and puts it above', async () => {
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf('tail line', 3 << 20));
			await open();
			expect(earlier()).toHaveTextContent('Load earlier output · 3.0 MB before this');

			vi.mocked(api.runs.workspaceLog).mockResolvedValue({
				text: 'older ',
				start: 1 << 20,
				end: 3 << 20,
				size: 4 << 20
			});
			await userEvent.click(earlier()!);

			expect(api.runs.workspaceLog).toHaveBeenLastCalledWith(RUN, { from: 1 << 20, n: 2 << 20 });
			expect(screen.getByText('older tail line')).toBeInTheDocument();
			expect(earlier()).toHaveTextContent('1.0 MB before this');
		});

		it('offers nothing once the log is on screen from its first byte', async () => {
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf('all of it'));
			await open();
			expect(earlier()).not.toBeInTheDocument();
		});

		it('starts over when the log moved under it', async () => {
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf('tail', 10));
			await open();
			vi.mocked(api.runs.workspaceLog)
				.mockResolvedValueOnce(logOf('other', 0, 5))
				.mockResolvedValueOnce(logOf('fresh'));
			await userEvent.click(earlier()!);

			expect(await screen.findByText('fresh')).toBeInTheDocument();
			expect(earlier()).not.toBeInTheDocument();
		});

		it('says why it could not go back, and keeps what is on screen', async () => {
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf('tail', 10));
			await open();
			vi.mocked(api.runs.workspaceLog).mockRejectedValue(new Error('gone'));
			await userEvent.click(earlier()!);

			expect(screen.getByText('tail')).toBeInTheDocument();
			expect(earlier()).toBeEnabled();
		});

		it('keeps nothing it read for a source no longer shown', async () => {
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf('tail', 10));
			const { rs } = await open(live({ events: stageEvents }));
			const out = deferred<LogRange | null>();
			vi.mocked(api.runs.workspaceLog).mockReturnValue(out.promise);
			vi.mocked(api.runs.stageLog).mockResolvedValue('plan output');
			await userEvent.click(earlier()!);
			rs.logSource = 'plan';
			await tick();
			out.resolve(logOf('older', 5, 14));
			expect(await screen.findByText('plan output')).toBeInTheDocument();
			expect(screen.queryByText(/older/)).not.toBeInTheDocument();
		});
	});

	describe('following the bottom', () => {
		function scrollable(el: HTMLElement) {
			Object.defineProperty(el, 'scrollHeight', { value: 1000, configurable: true });
			Object.defineProperty(el, 'clientHeight', { value: 200, configurable: true });
		}

		beforeEach(() => {
			vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
			vi.mocked(api.runs.workspaceLog).mockResolvedValue(logOf('first'));
		});

		it('stays at the bottom as the tail grows', async () => {
			await open();
			const log = screen.getByText('first').parentElement!;
			scrollable(log);

			await vi.advanceTimersByTimeAsync(2500);
			await tick();

			expect(log.scrollTop).toBe(1000);
		});

		it('leaves a reader who scrolled up where they are', async () => {
			await open();
			const log = screen.getByText('first').parentElement!;
			scrollable(log);
			log.scrollTop = 100;
			log.dispatchEvent(new Event('scroll'));

			await vi.advanceTimersByTimeAsync(2500);
			await tick();

			expect(log.scrollTop).toBe(100);
		});

		it('follows again once the reader returns to the bottom', async () => {
			await open();
			const log = screen.getByText('first').parentElement!;
			scrollable(log);
			log.scrollTop = 100;
			log.dispatchEvent(new Event('scroll'));
			log.scrollTop = 790;
			log.dispatchEvent(new Event('scroll'));

			await vi.advanceTimersByTimeAsync(2500);
			await tick();

			expect(log.scrollTop).toBe(1000);
		});
	});
});
