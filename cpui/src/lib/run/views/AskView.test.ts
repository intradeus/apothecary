import { fireEvent, render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { AskFrame, AskState } from '$lib/domain';
import { fmtClock } from '$lib/core/format';
import { AT, anAskMessage, anAskState, aViewStore } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import AskView from './AskView.svelte';

vi.mock('$lib/api', () => ({
	api: { ask: { state: vi.fn(), stream: vi.fn(), send: vi.fn(), setModel: vi.fn() } }
}));

const box = () => screen.getByRole('textbox', { name: 'Ask' });
const askBtn = () => screen.getByRole('button', { name: /^(Ask|Answering…)$/ });

/** Opens the pane on a state and hands back the stream's two callbacks. */
async function open(state: Partial<AskState> = {}) {
	const stop = vi.fn();
	vi.mocked(api.ask.state).mockResolvedValue(anAskState(state));
	vi.mocked(api.ask.stream).mockReturnValue(stop);
	const view = render(AskView, { rs: aViewStore(), visible: true });
	await screen.findByText('Ask', { selector: '.ttl' });
	const [, onFrame, onReconnect] = vi.mocked(api.ask.stream).mock.calls[0];
	const frame = async (f: AskFrame) => {
		onFrame(f);
		await tick();
	};
	return { ...view, frame, onReconnect: onReconnect!, stop };
}

describe('AskView', () => {
	beforeEach(() => {
		vi.resetAllMocks();
	});

	describe('states', () => {
		it('says it is opening until the pane has answered', async () => {
			const state = deferred<AskState>();
			vi.mocked(api.ask.state).mockReturnValue(state.promise);
			vi.mocked(api.ask.stream).mockReturnValue(vi.fn());
			render(AskView, { rs: aViewStore(), visible: true });

			expect(screen.getByRole('status')).toHaveTextContent('Opening the conversation…');
			state.resolve(anAskState());
			expect(await screen.findByRole('textbox', { name: 'Ask' })).toBeEnabled();
			expect(screen.queryByRole('status')).not.toBeInTheDocument();
		});

		it('says why the pane could not load, and offers no composer', async () => {
			vi.mocked(api.ask.state).mockRejectedValue(new Error('engine down'));
			vi.mocked(api.ask.stream).mockReturnValue(vi.fn());
			render(AskView, { rs: aViewStore(), visible: true });

			expect(await screen.findByText('The Ask pane could not be loaded')).toBeInTheDocument();
			expect(screen.getByText('engine down')).toBeInTheDocument();
			expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
		});

		it('says why Ask is off, without a model or a composer', async () => {
			await open({ enabled: false, reason: 'No ask section in the flow', models: ['acme/one'] });

			expect(screen.getByText('Ask is off for this run')).toBeInTheDocument();
			expect(screen.getByText('No ask section in the flow')).toBeInTheDocument();
			expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
			expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
		});

		it('keeps the old conversation readable while Ask is off', async () => {
			await open({ enabled: false, messages: [anAskMessage({ text: 'An old question' })] });
			expect(screen.getByText('An old question')).toBeInTheDocument();
		});

		it('invites a first question on an empty conversation', async () => {
			await open({ harness: 'acme' });
			expect(screen.getByText(/^Ask anything about the code/)).toBeInTheDocument();
			expect(screen.getByText('acme')).toBeInTheDocument();
		});

		it('drops the invitation once an answer is on its way', async () => {
			const { frame } = await open();
			await frame({ kind: 'delta', text: '' });
			expect(screen.queryByText(/^Ask anything about the code/)).not.toBeInTheDocument();
		});

		// A conversation already on screen is not lost to a failed re-read.
		it('keeps the conversation when a refetch after a reconnect fails', async () => {
			const { onReconnect } = await open({ messages: [anAskMessage({ text: 'An old question' })] });
			vi.mocked(api.ask.state).mockRejectedValue(new Error('blip'));
			onReconnect();
			await vi.waitFor(() => expect(api.ask.state).toHaveBeenCalledTimes(2));
			await tick();
			await tick();

			expect(screen.getByText('An old question')).toBeInTheDocument();
		});
	});

	describe('following the pane', () => {
		it('neither loads nor streams while hidden', () => {
			render(AskView, { rs: aViewStore(), visible: false });
			expect(api.ask.state).not.toHaveBeenCalled();
			expect(api.ask.stream).not.toHaveBeenCalled();
		});

		it('stops the stream when the window hides, and loads again when it returns', async () => {
			const { rerender, stop } = await open();
			expect(api.ask.state).toHaveBeenCalledWith('01TESTRUN0000000000000ABCD');

			await rerender({ visible: false });
			expect(stop).toHaveBeenCalledOnce();

			await rerender({ visible: true });
			expect(api.ask.state).toHaveBeenCalledTimes(2);
			expect(api.ask.stream).toHaveBeenCalledTimes(2);
		});

		it('refetches what a dropped stream may have missed', async () => {
			const { onReconnect } = await open();
			vi.mocked(api.ask.state).mockResolvedValue(
				anAskState({ messages: [anAskMessage({ text: 'Landed while offline' })] })
			);
			onReconnect();
			expect(await screen.findByText('Landed while offline')).toBeInTheDocument();
		});

		it('adds a message once, in sequence order, however often it is replayed', async () => {
			const { frame } = await open({ messages: [anAskMessage({ seq: 3, text: 'third' })] });
			const second = anAskMessage({ seq: 2, id: 'm2', text: 'second' });
			await frame({ kind: 'message', message: second });
			await frame({ kind: 'message', message: second });

			const said = screen.getAllByText(/^(second|third)$/).map((e) => e.textContent);
			expect(said).toEqual(['second', 'third']);
		});

		it('streams the answer with what the agent is doing, then settles it', async () => {
			const { frame } = await open();

			await frame({ kind: 'delta', text: 'Looking', tool: 'Read main.go' });
			expect(screen.getByText('Looking')).toBeInTheDocument();
			expect(screen.getByText('Read main.go…')).toBeInTheDocument();

			await frame({ kind: 'delta', text: 'Looking closer' });
			expect(screen.getByText('thinking…')).toBeInTheDocument();

			const answer = anAskMessage({ seq: 2, role: 'answer', text: 'It is in retry.go' });
			await frame({ kind: 'message', message: answer });
			expect(screen.getByText('It is in retry.go')).toBeInTheDocument();
			expect(screen.queryByText('thinking…')).not.toBeInTheDocument();
			expect(screen.queryByText('Looking closer')).not.toBeInTheDocument();
		});

		it('keeps the live answer through a question landing', async () => {
			const { frame } = await open();
			await frame({ kind: 'delta', text: 'Looking' });
			await frame({ kind: 'message', message: anAskMessage({ seq: 1, text: 'Asked meanwhile' }) });
			expect(screen.getByText('Looking')).toBeInTheDocument();
		});

		it('takes the box away while answering and clears the stream when it ends', async () => {
			const { frame } = await open();

			await frame({ kind: 'state', busy: true });
			await frame({ kind: 'delta', text: 'Partial' });
			expect(box()).toBeDisabled();
			expect(askBtn()).toHaveTextContent('Answering…');

			await frame({ kind: 'state', busy: false });
			expect(box()).toBeEnabled();
			expect(screen.queryByText('Partial')).not.toBeInTheDocument();
		});

		it('follows a model change from elsewhere', async () => {
			const { frame } = await open({ model: 'acme/one', models: [] });
			await frame({ kind: 'state', busy: false, model: 'acme/two' });
			expect(screen.getByPlaceholderText('provider/model')).toHaveValue('acme/two');
		});

		it('ignores frames that arrive before the pane has loaded', async () => {
			const state = deferred<AskState>();
			vi.mocked(api.ask.state).mockReturnValue(state.promise);
			vi.mocked(api.ask.stream).mockReturnValue(vi.fn());
			render(AskView, { rs: aViewStore(), visible: true });

			const [, onFrame] = vi.mocked(api.ask.stream).mock.calls[0];
			expect(() => onFrame({ kind: 'delta', text: 'Early' })).not.toThrow();
			state.resolve(anAskState());

			expect(await screen.findByRole('textbox', { name: 'Ask' })).toBeInTheDocument();
			expect(screen.queryByText('Early')).not.toBeInTheDocument();
		});
	});

	describe('the conversation', () => {
		it('names your question and when you asked it', async () => {
			await open({ messages: [anAskMessage({ userId: 'u1' })] });
			expect(screen.getByText(`you · ${fmtClock(AT)}`)).toBeInTheDocument();
		});

		it('does not claim a question it cannot place as yours', async () => {
			await open({ messages: [anAskMessage()] });
			expect(screen.getByText(`asked · ${fmtClock(AT)}`)).toBeInTheDocument();
		});

		it('signs an answer with its model, cost and time', async () => {
			await open({
				messages: [
					anAskMessage({ role: 'answer', text: 'Here', model: 'acme/one', costUsd: 0.012 })
				]
			});
			expect(screen.getByText(`acme/one · $0.012 · ${fmtClock(AT)}`)).toBeInTheDocument();
		});

		it('leaves out what an answer did not cost', async () => {
			await open({ messages: [anAskMessage({ role: 'answer', text: 'Here', costUsd: 0 })] });
			expect(screen.getByText(fmtClock(AT))).toBeInTheDocument();
		});

		it('says why an answer failed', async () => {
			await open({ messages: [anAskMessage({ role: 'answer', text: '', error: 'out of turns' })] });
			expect(screen.getByText('No answer — out of turns')).toBeInTheDocument();
		});
	});

	describe('asking', () => {
		it('sends on Enter, trimmed, and empties the box', async () => {
			vi.mocked(api.ask.send).mockResolvedValue(anAskMessage());
			await open();

			await userEvent.type(box(), '  Where is the retry?  {Enter}');

			expect(api.ask.send).toHaveBeenCalledExactlyOnceWith(
				'01TESTRUN0000000000000ABCD',
				'Where is the retry?'
			);
			expect(box()).toHaveValue('');
		});

		it('leaves an Enter that confirms an IME composition alone', async () => {
			await open();
			await userEvent.type(box(), 'こんにちは');

			await fireEvent.keyDown(box(), { key: 'Enter', isComposing: true });

			expect(api.ask.send).not.toHaveBeenCalled();
		});

		it('keeps writing on Shift+Enter', async () => {
			await open();
			await userEvent.type(box(), 'one{Shift>}{Enter}{/Shift}two');
			expect(api.ask.send).not.toHaveBeenCalled();
			expect(box()).toHaveValue('one\ntwo');
		});

		it('sends from the button', async () => {
			vi.mocked(api.ask.send).mockResolvedValue(anAskMessage());
			await open();
			await userEvent.type(box(), 'Why?');
			await userEvent.click(askBtn());
			expect(api.ask.send).toHaveBeenCalledOnce();
		});

		it('offers nothing to send until there are words', async () => {
			await open();
			expect(askBtn()).toBeDisabled();

			await userEvent.type(box(), '   {Enter}');
			expect(askBtn()).toBeDisabled();
			expect(api.ask.send).not.toHaveBeenCalled();
		});

		it('shows the answer as on its way once the question is in', async () => {
			vi.mocked(api.ask.send).mockResolvedValue(anAskMessage());
			await open();
			await userEvent.type(box(), 'Why?{Enter}');

			expect(await screen.findByText('thinking…')).toBeInTheDocument();
			expect(box()).toBeDisabled();
			expect(askBtn()).toHaveTextContent('Answering…');
		});

		it('holds the button while the question is in flight, sending it once', async () => {
			const sent = deferred<never>();
			vi.mocked(api.ask.send).mockReturnValue(sent.promise);
			await open();

			await userEvent.type(box(), 'Why?{Enter}');
			expect(askBtn()).toHaveAttribute('aria-busy', 'true');

			await userEvent.type(box(), 'Again');
			expect(askBtn()).toBeDisabled();

			await userEvent.type(box(), '{Enter}');
			expect(api.ask.send).toHaveBeenCalledOnce();
		});

		it('gives the words back with the reason when refused', async () => {
			vi.mocked(api.ask.send).mockRejectedValue(new Error('an answer is being written'));
			await open();
			await userEvent.type(box(), 'Why?{Enter}');

			expect(await screen.findByText('an answer is being written')).toBeInTheDocument();
			expect(box()).toHaveValue('Why?');
			expect(screen.queryByText('thinking…')).not.toBeInTheDocument();
			expect(box()).toBeEnabled();
		});

		it('keeps what was typed meanwhile over the refused words', async () => {
			const sent = deferred<never>();
			vi.mocked(api.ask.send).mockReturnValue(sent.promise);
			await open();
			await userEvent.type(box(), 'First{Enter}');
			await userEvent.type(box(), 'Second');

			sent.reject(new Error('nope'));

			expect(await screen.findByText('nope')).toBeInTheDocument();
			expect(box()).toHaveValue('Second');
		});

		it('clears the last failure on the next attempt', async () => {
			vi.mocked(api.ask.send).mockRejectedValueOnce(new Error('nope'));
			await open();
			await userEvent.type(box(), 'Why?{Enter}');
			await screen.findByText('nope');

			vi.mocked(api.ask.send).mockResolvedValue(anAskMessage());
			await userEvent.type(box(), '{Enter}');
			expect(screen.queryByText('nope')).not.toBeInTheDocument();
		});
	});

	describe('the model', () => {
		const models = ['acme/one', 'acme/two'];
		const picker = () => screen.getByRole('combobox');

		async function pick(name: string) {
			await userEvent.click(picker());
			await userEvent.click(within(screen.getByRole('listbox')).getByRole('option', { name }));
		}

		it('picks from what the harness can reach', async () => {
			vi.mocked(api.ask.setModel).mockResolvedValue(undefined);
			await open({ models });
			await pick('acme/two');

			expect(api.ask.setModel).toHaveBeenCalledExactlyOnceWith(
				'01TESTRUN0000000000000ABCD',
				'acme/two'
			);
			expect(picker()).toHaveTextContent('acme/two');
		});

		it('leaves the model alone when the same one is picked', async () => {
			await open({ models });
			await pick('acme/one');
			expect(api.ask.setModel).not.toHaveBeenCalled();
		});

		it('takes a typed model when the harness lists none', async () => {
			vi.mocked(api.ask.setModel).mockResolvedValue(undefined);
			await open({ model: undefined, models: [] });

			await userEvent.type(screen.getByPlaceholderText('provider/model'), '  acme/typed  {Tab}');

			expect(api.ask.setModel).toHaveBeenCalledExactlyOnceWith(
				'01TESTRUN0000000000000ABCD',
				'acme/typed'
			);
		});

		it('leaves the model alone when the same one is typed', async () => {
			await open({ model: 'acme/one', models: [] });
			await userEvent.type(screen.getByPlaceholderText('provider/model'), ' {Tab}');
			expect(api.ask.setModel).not.toHaveBeenCalled();
		});

		it('cannot be changed while an answer is written', async () => {
			await open({ models, busy: true });
			expect(picker()).toBeDisabled();
		});

		it('cannot be typed over while an answer is written', async () => {
			await open({ models: [], busy: true });
			expect(screen.getByPlaceholderText('provider/model')).toBeDisabled();
		});

		it('says why a model was refused', async () => {
			vi.mocked(api.ask.setModel).mockRejectedValue(new Error('not available to harness'));
			await open({ models });
			await pick('acme/two');
			expect(await screen.findByText('not available to harness')).toBeInTheDocument();
		});

		it('goes back to the model in force when the pick is refused', async () => {
			vi.mocked(api.ask.setModel).mockRejectedValue(new Error('not available to harness'));
			await open({ models });
			await pick('acme/two');
			await screen.findByText('not available to harness');

			expect(picker()).toHaveTextContent('acme/one');
		});

		it('goes back to the model in force when a typed one is refused', async () => {
			vi.mocked(api.ask.setModel).mockRejectedValue(new Error('not available to harness'));
			await open({ model: 'acme/one', models: [] });
			const input = screen.getByPlaceholderText('provider/model');
			await userEvent.type(input, 'x{Tab}');
			await screen.findByText('not available to harness');

			expect(input).toHaveValue('acme/one');
		});
	});
});
