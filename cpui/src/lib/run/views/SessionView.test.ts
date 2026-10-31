import { fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import type { Run, RunHold } from '$lib/domain';
import { applyDelta, emptyLive } from '$lib/run/transcriptItems';
import { aRun } from '$test/fixtures';
import { AT, aLiveRun, aRaise, anAnswer, anEvent, aViewStore } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import SessionView from './SessionView.svelte';

vi.mock('$lib/api', () => ({
	api: {
		runs: { sendMessage: vi.fn(), takeOver: vi.fn(), release: vi.fn(), get: vi.fn() },
		pings: { answer: vi.fn() },
		files: { upload: vi.fn() }
	}
}));

const RUN = '01TESTRUN0000000000000ABCD';
const HOLD: RunHold = { stageId: 'build', since: AT, turns: 0, messages: 0, costUsd: 0 };
const agentStage = anEvent(1, 'stage_started', { stage_id: 'build', iteration: 1, agent: 'dev' });
const said = (seq: number, text: string) =>
	anEvent(seq, 'agent_message', { text, segment: `s${seq}` });
const phaseDone = (seq: number) => anEvent(seq, 'phase_completed', { phase: 'main' });
const takeOverBtn = () => screen.queryByRole('button', { name: 'Take over' });
const composerBox = () => screen.getByRole('textbox', { name: 'Message the agent' });
const followBtn = () => screen.queryByRole('button', { name: /Follow/ });
const NUDGE = 'The run is waiting on what its watchers watch.';

function show(
	over: Parameters<typeof aViewStore>[1] = {},
	run: Run | null = aLiveRun(),
	props: { visible?: boolean } = {}
) {
	const onlog = vi.fn();
	const onphases = vi.fn();
	const rs = aViewStore(run, over);
	const view = render(SessionView, { rs, visible: true, onlog, onphases, ...props });
	return { ...view, rs, onlog, onphases };
}

describe('SessionView', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		vi.spyOn(fleet, 'refresh').mockResolvedValue();
		vi.mocked(api.runs.get).mockImplementation(async () => ({ run: aLiveRun(), events: [] }));
	});

	describe('the conversation', () => {
		it('says none has started on a live run', () => {
			show();
			expect(screen.getByText(/^No conversation yet\./)).toBeInTheDocument();
		});

		it('says none was recorded on a finished one', () => {
			show({}, aRun({ state: 'completed' }));
			expect(screen.getByText('No conversation was recorded for this run.')).toBeInTheDocument();
		});

		it('shows what the agent said, and drops the placeholder', () => {
			show({ events: [agentStage, said(2, 'Reading the code')] });
			expect(screen.getByText('Reading the code')).toBeInTheDocument();
			expect(screen.queryByText(/^No conversation yet\./)).not.toBeInTheDocument();
		});

		it('streams what the agent is writing', async () => {
			const { rs } = show({ events: [agentStage] });

			rs.live = applyDelta(emptyLive(), {
				kind: 'message',
				segment: 's',
				text: 'Writing'
			} as never);

			expect(await screen.findByText('Writing')).toBeInTheDocument();
		});

		it('opens a failed stage’s log', async () => {
			const { onlog } = show({
				events: [agentStage, anEvent(2, 'stage_failed', { stage_id: 'build', reason: 'exit' })]
			});
			await userEvent.click(screen.getByRole('button', { name: 'View log' }));
			expect(onlog).toHaveBeenCalledExactlyOnceWith('build');
		});

		it('withdraws an unanswered question once the run is over', () => {
			show(
				{ events: [aRaise(1, 'q', { type: 'clarification', answer_kind: 'form' })] },
				aRun({ state: 'failed' })
			);
			expect(screen.queryByRole('button', { name: /Waiting for your answer/ })).toBeNull();
		});
	});

	describe('the ping dock', () => {
		it('docks the pending ping', () => {
			show({ events: [aRaise(1, 'a')] });
			expect(screen.getByText('Question a?')).toBeInTheDocument();
			expect(screen.getByRole('button', { name: 'Approve' })).toBeInTheDocument();
		});

		it('answers it through the store', async () => {
			vi.mocked(api.pings.answer).mockResolvedValue(undefined as never);
			show({ events: [aRaise(1, 'a')] });

			await userEvent.click(screen.getByRole('button', { name: 'Approve' }));

			expect(api.pings.answer).toHaveBeenCalledExactlyOnceWith(
				'a',
				expect.objectContaining({ answer: 'approve' })
			);
		});

		it('leads with the newest and says what queues behind it', () => {
			show({ events: [aRaise(1, 'a'), aRaise(2, 'b'), aRaise(3, 'c')] });

			expect(screen.getByText('Question c?')).toBeInTheDocument();
			expect(screen.queryByText('Question b?')).not.toBeInTheDocument();
			expect(screen.getByText('Answer this first — 2 earlier pings queued')).toBeInTheDocument();
		});

		it('moves on to the next once one is answered', async () => {
			const { rs } = show({ events: [aRaise(1, 'a'), aRaise(2, 'b')] });

			rs.events = [...rs.events, anAnswer(3, 'b')];

			expect(await screen.findByText('Question a?')).toBeInTheDocument();
			expect(screen.queryByText(/earlier ping/)).not.toBeInTheDocument();
		});

		it('holds the card while an answer is on its way', () => {
			show({ events: [aRaise(1, 'a')], answering: true });
			expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();
		});

		it('docks nothing on a finished run', () => {
			show({ events: [aRaise(1, 'a')] }, aRun({ state: 'abandoned' }));
			expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument();
		});
	});

	describe('the composer', () => {
		it('offers a take-over while an agent stage is in flight', () => {
			show({ events: [agentStage] });
			expect(takeOverBtn()).toBeEnabled();
		});

		it('counts a harness as an agent', () => {
			show({ events: [anEvent(1, 'stage_started', { stage_id: 'build', harness: 'acme' })] });
			expect(takeOverBtn()).toBeInTheDocument();
		});

		it('offers nothing while a command stage runs', () => {
			show({ events: [anEvent(1, 'stage_started', { stage_id: 'test' })] });
			expect(takeOverBtn()).not.toBeInTheDocument();
		});

		it('offers nothing with no stage in flight', () => {
			show({
				events: [agentStage, anEvent(2, 'stage_completed', { stage_id: 'build', duration_ms: 1 })]
			});
			expect(takeOverBtn()).not.toBeInTheDocument();
		});

		it('offers nothing on a run that is not running', () => {
			show({ events: [agentStage] }, aLiveRun({ state: 'paused' }));
			expect(takeOverBtn()).not.toBeInTheDocument();
		});

		it('takes over through the store', async () => {
			vi.mocked(api.runs.takeOver).mockResolvedValue(undefined as never);
			show({ events: [agentStage] });

			await userEvent.click(takeOverBtn()!);

			expect(api.runs.takeOver).toHaveBeenCalledExactlyOnceWith(RUN);
		});

		it('keeps the composer on a held run with no stage in flight', () => {
			show({}, aLiveRun({ state: 'paused', hold: HOLD }));
			expect(composerBox()).toBeInTheDocument();
		});

		it('hands back through the store', async () => {
			vi.mocked(api.runs.release).mockResolvedValue(undefined as never);
			show({}, aLiveRun({ hold: HOLD }));

			await userEvent.click(screen.getByRole('button', { name: 'Release' }));

			expect(api.runs.release).toHaveBeenCalledExactlyOnceWith(RUN);
		});

		it('says it is handing back while the engine records it', () => {
			show({ releasing: true }, aLiveRun({ hold: HOLD }));
			expect(screen.getByRole('button', { name: 'Handing back…' })).toBeDisabled();
		});
	});

	describe('a sent message', () => {
		const held = () => aLiveRun({ hold: HOLD });

		it('shows as sending at once', async () => {
			vi.mocked(api.runs.sendMessage).mockReturnValue(deferred<string>().promise);
			show({}, held());

			await userEvent.type(composerBox(), 'Use the other API{Enter}');

			expect(api.runs.sendMessage).toHaveBeenCalledExactlyOnceWith(RUN, 'Use the other API');
			expect(screen.getByText('Use the other API')).toBeInTheDocument();
			expect(screen.getByText('sending…')).toBeInTheDocument();
		});

		it('gives way to the run’s own echo, once', async () => {
			vi.mocked(api.runs.sendMessage).mockResolvedValue('m1');
			const { rs } = show({}, held());
			await userEvent.type(composerBox(), 'Use the other API{Enter}');

			rs.events = [anEvent(1, 'message_queued', { message_id: 'm1', text: 'Use the other API' })];
			await tick();

			expect(screen.getAllByText('Use the other API')).toHaveLength(1);
			expect(screen.queryByText('sending…')).not.toBeInTheDocument();
			expect(screen.getByText('queued — read when its turn ends')).toBeInTheDocument();
		});

		it('shows a message sent a second time as sending too', async () => {
			vi.mocked(api.runs.sendMessage).mockReturnValue(deferred<string>().promise);
			show({ events: [anEvent(1, 'message_queued', { message_id: 'm1', text: 'Go on' })] }, held());

			await userEvent.type(composerBox(), 'Go on{Enter}');

			expect(screen.getByText('sending…')).toBeInTheDocument();
		});

		it('goes when the engine refuses it, and the words come back', async () => {
			vi.mocked(api.runs.sendMessage).mockRejectedValue(new Error('no hold'));
			show({}, held());

			await userEvent.type(composerBox(), 'Use the other API{Enter}');

			expect(await screen.findByText('no hold')).toBeInTheDocument();
			expect(screen.queryByText('sending…')).not.toBeInTheDocument();
			expect(composerBox()).toHaveValue('Use the other API');
		});
	});

	describe('the nudge between phases', () => {
		it('points at the watchers while the run is idle', async () => {
			const { onphases } = show({}, aLiveRun({ idle: true }));

			await userEvent.click(screen.getByRole('button', { name: /See the watchers/ }));

			expect(screen.getByText(NUDGE)).toBeInTheDocument();
			expect(onphases).toHaveBeenCalledOnce();
		});

		it('stays away while a chain has the run', () => {
			show({}, aLiveRun({ idle: false }));
			expect(screen.queryByText(NUDGE)).not.toBeInTheDocument();
		});

		it('stays away on a finished run', () => {
			show({}, aLiveRun({ idle: true, state: 'completed' }));
			expect(screen.queryByText(NUDGE)).not.toBeInTheDocument();
		});

		it('goes when dismissed, remembering how many phases had ended', async () => {
			const { rs } = show({ events: [phaseDone(1)] }, aLiveRun({ idle: true }));

			await userEvent.click(screen.getByRole('button', { name: /^Dismiss/ }));

			expect(rs.phasesNudgeDismissedAt).toBe(1);
			await vi.waitFor(() => expect(screen.queryByText(NUDGE)).not.toBeInTheDocument());
		});

		it('stays dismissed until another phase ends', () => {
			show({ events: [phaseDone(1)], phasesNudgeDismissedAt: 1 }, aLiveRun({ idle: true }));
			expect(screen.queryByText(NUDGE)).not.toBeInTheDocument();
		});

		it('comes back after the next chain', () => {
			show(
				{ events: [phaseDone(1), phaseDone(2)], phasesNudgeDismissedAt: 1 },
				aLiveRun({ idle: true })
			);
			expect(screen.getByText(NUDGE)).toBeInTheDocument();
		});
	});

	describe('the close', () => {
		it('says the run is finished and its workspace stays for now', () => {
			show({}, aLiveRun({ state: 'completed' }));
			expect(screen.getByText('The bottle is corked')).toBeInTheDocument();
			expect(screen.getByText(/workspace stays up until the reaper/)).toBeInTheDocument();
		});

		it('says only that it is finished when the workspace is gone', () => {
			show({}, aRun({ state: 'failed' }));
			expect(screen.getByText('The run is finished.')).toBeInTheDocument();
		});

		it('has no composer on a finished run, even one left held', () => {
			show({ events: [agentStage] }, aLiveRun({ state: 'failed', hold: HOLD }));
			expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
			expect(takeOverBtn()).not.toBeInTheDocument();
		});

		it('shows neither close nor composer before the run has loaded', () => {
			show({}, null);
			expect(screen.queryByText('The bottle is corked')).not.toBeInTheDocument();
			expect(takeOverBtn()).not.toBeInTheDocument();
		});
	});

	describe('following the bottom', () => {
		const flush = async () => {
			await tick();
			await tick();
		};

		/** The transcript's scroller, 1000px of content in a 200px window. */
		function scrollable(container: HTMLElement) {
			const el = container.querySelector<HTMLElement>('.scroll')!;
			Object.defineProperty(el, 'scrollHeight', { value: 1000, configurable: true });
			Object.defineProperty(el, 'clientHeight', { value: 200, configurable: true });
			return el;
		}
		const scrollTo = async (el: HTMLElement, top: number) => {
			el.scrollTop = top;
			await fireEvent.scroll(el);
		};
		/** Reads from the bottom, then up. */
		async function readUp(el: HTMLElement) {
			await scrollTo(el, 800);
			await scrollTo(el, 300);
		}

		let moved: MockInstance;
		beforeEach(() => {
			moved = vi.spyOn(Element.prototype, 'scrollTo');
		});
		afterEach(() => {
			vi.useRealTimers();
		});

		it('offers no Follow while at the bottom', async () => {
			const { container } = show();
			await scrollTo(scrollable(container), 800);
			expect(followBtn()).not.toBeInTheDocument();
		});

		it('hands over to a reader who scrolls up', async () => {
			const { container } = show();
			await readUp(scrollable(container));
			expect(followBtn()).toBeInTheDocument();
		});

		it('ignores the jitter of a smooth follow', async () => {
			const { container } = show();
			const el = scrollable(container);
			await scrollTo(el, 500);
			await scrollTo(el, 499);
			expect(followBtn()).not.toBeInTheDocument();
		});

		it('takes back over when the reader reaches the bottom', async () => {
			const { container } = show();
			const el = scrollable(container);
			await readUp(el);
			await scrollTo(el, 750);
			await vi.waitFor(() => expect(followBtn()).not.toBeInTheDocument());
		});

		it('jumps back down on Follow', async () => {
			const { container } = show();
			const el = scrollable(container);
			await readUp(el);

			await userEvent.click(followBtn()!);
			await flush();

			expect(moved).toHaveBeenLastCalledWith(expect.objectContaining({ top: 1000 }));
			await vi.waitFor(() => expect(followBtn()).not.toBeInTheDocument());
		});

		it('follows what arrives while at the bottom', async () => {
			const { container, rs } = show({ events: [said(1, 'one')] });
			scrollable(container);
			await flush();
			moved.mockClear();

			rs.events = [...rs.events, said(2, 'two')];
			await flush();

			expect(moved).toHaveBeenCalledWith(expect.objectContaining({ top: 1000 }));
		});

		it('leaves a reader alone when more arrives', async () => {
			const { container, rs } = show({ events: [said(1, 'one')] });
			await readUp(scrollable(container));
			await flush();
			moved.mockClear();

			rs.events = [...rs.events, said(2, 'two')];
			await flush();

			expect(moved).not.toHaveBeenCalled();
		});

		it('follows as the live text grows', async () => {
			const delta = (text: string) => ({ kind: 'message', segment: 's', text }) as never;
			const { container, rs } = show({ events: [agentStage] });
			scrollable(container);
			rs.live = applyDelta(emptyLive(), delta('Hi'));
			await flush();
			moved.mockClear();

			rs.live = applyDelta(rs.live, delta('Hi there'));
			await flush();

			expect(moved).toHaveBeenCalled();
		});

		it('re-pins when the view comes back on screen', async () => {
			const { container, rerender } = show({}, aLiveRun(), { visible: false });
			scrollable(container);
			await flush();
			moved.mockClear();

			await rerender({ visible: true });
			await flush();

			expect(moved).toHaveBeenCalledWith({ top: 1000, behavior: 'instant' });
		});

		it('leaves a reader alone when the view comes back', async () => {
			const { container, rerender } = show();
			await readUp(scrollable(container));
			await rerender({ visible: false });
			moved.mockClear();

			await rerender({ visible: true });
			await flush();

			expect(moved).not.toHaveBeenCalled();
		});

		it('jumps through the history, then glides once it went quiet', async () => {
			vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
			const { container, rs } = show({ events: [said(1, 'one')] });
			scrollable(container);
			rs.events = [...rs.events, said(2, 'two')];
			await flush();
			expect(moved).toHaveBeenLastCalledWith({ top: 1000, behavior: 'instant' });

			vi.advanceTimersByTime(700);
			rs.events = [...rs.events, said(3, 'three')];
			await flush();

			expect(moved).toHaveBeenLastCalledWith({ top: 1000, behavior: 'smooth' });
		});

		it('keeps jumping while the replay is still landing', async () => {
			vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
			const { container, rs } = show({ events: [said(1, 'one')] });
			scrollable(container);

			vi.advanceTimersByTime(600);
			rs.events = [...rs.events, said(2, 'two')];
			await flush();
			vi.advanceTimersByTime(600);
			rs.events = [...rs.events, said(3, 'three')];
			await flush();

			expect(moved).toHaveBeenLastCalledWith({ top: 1000, behavior: 'instant' });
		});

		it('never glides under reduced motion', async () => {
			vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
			vi.spyOn(globalThis, 'matchMedia').mockReturnValue({ matches: true } as MediaQueryList);
			const { container, rs } = show({ events: [said(1, 'one')] });
			scrollable(container);

			vi.advanceTimersByTime(700);
			rs.events = [...rs.events, said(2, 'two')];
			await flush();

			expect(moved).toHaveBeenLastCalledWith({ top: 1000, behavior: 'instant' });
		});

		it('jumps to a sent message at once, even after the list went quiet', async () => {
			vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
			vi.mocked(api.runs.sendMessage).mockReturnValue(deferred<string>().promise);
			const { container } = show({}, aLiveRun({ hold: HOLD }));
			scrollable(container);
			vi.advanceTimersByTime(700);

			await fireEvent.input(composerBox(), { target: { value: 'Stop' } });
			await fireEvent.keyDown(composerBox(), { key: 'Enter' });
			await flush();

			expect(moved).toHaveBeenLastCalledWith({ top: 1000, behavior: 'instant' });
		});

		it('comes back down when a reader sends one', async () => {
			vi.mocked(api.runs.sendMessage).mockReturnValue(deferred<string>().promise);
			const { container } = show({}, aLiveRun({ hold: HOLD }));
			await readUp(scrollable(container));
			moved.mockClear();

			await userEvent.type(composerBox(), 'Stop{Enter}');
			await flush();

			expect(moved).toHaveBeenCalledWith(expect.objectContaining({ top: 1000 }));
			await vi.waitFor(() => expect(followBtn()).not.toBeInTheDocument());
		});

		it('goes to the question still waiting on an answer', async () => {
			const { container } = show({
				events: [aRaise(1, 'q', { type: 'clarification', answer_kind: 'form' })]
			});
			const el = scrollable(container);
			await readUp(el);
			moved.mockClear();

			await userEvent.click(screen.getByRole('button', { name: /Waiting for your answer/ }));
			await flush();

			expect(moved).toHaveBeenCalled();
		});
	});
});
