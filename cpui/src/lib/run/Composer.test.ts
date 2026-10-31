import { fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { aHold } from '$test/fixtures/run-components';
import { deferred } from '$test/helpers';
import Composer from './Composer.svelte';

vi.mock('$lib/api', () => ({ api: { runs: { sendMessage: vi.fn() } } }));

const box = () => screen.getByRole('textbox', { name: 'Message the agent' });
const sendBtn = () => screen.getByRole('button', { name: 'Send' });

function held(over: Partial<Parameters<typeof render<typeof Composer>>[1]> = {}) {
	const onsent = vi.fn();
	const onfailed = vi.fn();
	render(Composer, { runId: 'r1', onsent, onfailed, hold: aHold(), ...over });
	return { onsent, onfailed };
}

describe('Composer', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		vi.mocked(api.runs.sendMessage).mockResolvedValue(undefined as never);
	});

	describe('before a take-over', () => {
		it('is only the Take over button', () => {
			render(Composer, { runId: 'r1', onsent: vi.fn() });

			expect(screen.getByText('Take over to talk to the agent')).toBeInTheDocument();
			expect(screen.getByRole('button', { name: 'Take over' })).toBeEnabled();
			expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
			expect(screen.queryByRole('button', { name: 'Send' })).not.toBeInTheDocument();
			expect(screen.queryByRole('button', { name: 'Release' })).not.toBeInTheDocument();
		});

		it('holds the button while the take-over is on its way', async () => {
			const taking = deferred<boolean>();
			const ontakeover = vi.fn(() => taking.promise);
			render(Composer, { runId: 'r1', onsent: vi.fn(), ontakeover });
			const btn = screen.getByRole('button', { name: 'Take over' });

			await userEvent.click(btn);
			expect(ontakeover).toHaveBeenCalledOnce();
			expect(btn).toBeDisabled();
			expect(btn).toHaveAttribute('aria-busy', 'true');

			taking.resolve(false);
			await vi.waitFor(() => expect(btn).toBeEnabled());
		});

		it('offers the button again when a take-over is refused', async () => {
			const ontakeover = vi.fn(async () => false);
			render(Composer, { runId: 'r1', onsent: vi.fn(), ontakeover });
			const btn = screen.getByRole('button', { name: 'Take over' });

			await userEvent.click(btn);
			await vi.waitFor(() => expect(btn).toBeEnabled());
			expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
		});
	});

	describe('while held', () => {
		it('names the hold, its clock and its messages', () => {
			const since = '2026-10-31T12:00:00Z';
			held({
				hold: aHold({ since, messages: 2 }),
				now: new Date(since).getTime() + 252_000
			});

			expect(screen.getByText('In your hands · 4:12')).toBeInTheDocument();
			expect(screen.getByText('· 2 messages')).toBeInTheDocument();
			expect(box()).toHaveAttribute(
				'placeholder',
				'The agent is in your hands. Say what to change…'
			);
			expect(screen.queryByRole('button', { name: 'Take over' })).not.toBeInTheDocument();
		});

		it('counts no messages until there is one, then one in the singular', async () => {
			const { rerender } = render(Composer, {
				runId: 'r1',
				onsent: vi.fn(),
				hold: aHold({ messages: 0 })
			});
			expect(screen.queryByText(/message$|messages$/)).not.toBeInTheDocument();

			await rerender({ hold: aHold({ messages: 1 }) });
			expect(screen.getByText('· 1 message')).toBeInTheDocument();
		});

		it('ticks the clock from the page', async () => {
			const since = '2026-10-31T12:00:00Z';
			const start = new Date(since).getTime();
			const { rerender } = render(Composer, {
				runId: 'r1',
				onsent: vi.fn(),
				hold: aHold({ since }),
				now: start + 9_000
			});
			expect(screen.getByText('In your hands · 0:09')).toBeInTheDocument();

			await rerender({ now: start + 3_725_000 });
			expect(screen.getByText('In your hands · 1:02:05')).toBeInTheDocument();
		});

		it('releases, and holds the button while that is on its way', async () => {
			const going = deferred<boolean>();
			const onrelease = vi.fn(() => going.promise);
			held({ onrelease });
			const btn = screen.getByRole('button', { name: 'Release' });

			await userEvent.click(btn);
			expect(onrelease).toHaveBeenCalledOnce();
			expect(btn).toBeDisabled();
			expect(btn).toHaveAttribute('aria-busy', 'true');

			going.resolve(true);
			await vi.waitFor(() => expect(btn).toBeEnabled());
		});

		it('reads "Handing back…" until the engine records the release', async () => {
			const onrelease = vi.fn(async () => true);
			held({ onrelease, releasing: true });
			const btn = screen.getByRole('button', { name: 'Handing back…' });

			expect(btn).toBeDisabled();
			expect(btn).toHaveAttribute('aria-busy', 'true');
			await userEvent.click(btn);
			expect(onrelease).not.toHaveBeenCalled();
		});

		it('still takes messages while the release is on record', () => {
			held({ releasing: true });
			expect(box()).toBeEnabled();
		});
	});

	describe('sending', () => {
		it('has nothing to send while the box is empty or blank', async () => {
			held();
			expect(sendBtn()).toBeDisabled();

			await userEvent.type(box(), '   ');
			expect(sendBtn()).toBeDisabled();

			await userEvent.type(box(), '{Enter}');
			expect(api.runs.sendMessage).not.toHaveBeenCalled();
		});

		it('sends the trimmed text on Enter and clears the box at once', async () => {
			const leaving = deferred<void>();
			vi.mocked(api.runs.sendMessage).mockReturnValue(leaving.promise as never);
			const { onsent } = held();

			await userEvent.type(box(), '  use the middleware  {Enter}');

			expect(onsent).toHaveBeenCalledExactlyOnceWith('use the middleware');
			expect(api.runs.sendMessage).toHaveBeenCalledExactlyOnceWith('r1', 'use the middleware');
			expect(box()).toHaveValue('');
			leaving.resolve();
		});

		it('sends with the button too', async () => {
			const { onsent } = held();

			await userEvent.type(box(), 'go on');
			await userEvent.click(sendBtn());

			expect(onsent).toHaveBeenCalledExactlyOnceWith('go on');
			expect(api.runs.sendMessage).toHaveBeenCalledExactlyOnceWith('r1', 'go on');
		});

		it('makes a newline of Shift+Enter', async () => {
			const { onsent } = held();

			await userEvent.type(box(), 'one{Shift>}{Enter}{/Shift}two');

			expect(box()).toHaveValue('one\ntwo');
			expect(onsent).not.toHaveBeenCalled();
		});

		// Enter confirms a candidate while an IME is composing (CJK input).
		it('leaves an Enter that confirms an IME composition alone', async () => {
			const { onsent } = held();
			await userEvent.type(box(), 'こんにちは');

			await fireEvent.keyDown(box(), { key: 'Enter', isComposing: true });

			expect(onsent).not.toHaveBeenCalled();
		});

		it('sends one message at a time', async () => {
			const leaving = deferred<void>();
			vi.mocked(api.runs.sendMessage).mockReturnValue(leaving.promise as never);
			const { onsent } = held();

			await userEvent.type(box(), 'first{Enter}');
			await userEvent.type(box(), 'second{Enter}');

			expect(onsent).toHaveBeenCalledOnce();
			expect(api.runs.sendMessage).toHaveBeenCalledOnce();
			expect(box()).toHaveValue('second');
			expect(sendBtn()).toBeDisabled();
			expect(sendBtn()).toHaveAttribute('aria-busy', 'true');

			leaving.resolve();
			await vi.waitFor(() => expect(sendBtn()).toBeEnabled());
		});

		it('gives a refused message back with the reason', async () => {
			vi.mocked(api.runs.sendMessage).mockRejectedValue(new Error('the hold has ended'));
			const { onfailed } = held();

			await userEvent.type(box(), 'go on{Enter}');

			await vi.waitFor(() => expect(box()).toHaveValue('go on'));
			expect(screen.getByText('the hold has ended')).toBeInTheDocument();
			expect(onfailed).toHaveBeenCalledExactlyOnceWith('go on');
			expect(sendBtn()).toBeEnabled();
		});

		it('keeps what was typed since when a message is refused', async () => {
			const leaving = deferred<void>();
			vi.mocked(api.runs.sendMessage).mockReturnValue(leaving.promise as never);
			const { onfailed } = held();

			await userEvent.type(box(), 'first{Enter}');
			await userEvent.type(box(), 'second');
			leaving.reject(new Error('nope'));

			await vi.waitFor(() => expect(onfailed).toHaveBeenCalledWith('first'));
			expect(box()).toHaveValue('second');
		});

		it('survives a refusal without an onfailed', async () => {
			vi.mocked(api.runs.sendMessage).mockRejectedValue(new Error('nope'));
			render(Composer, { runId: 'r1', onsent: vi.fn(), hold: aHold() });

			await userEvent.type(box(), 'go on{Enter}');

			await vi.waitFor(() => expect(box()).toHaveValue('go on'));
		});

		it('clears the reason on the next try', async () => {
			vi.mocked(api.runs.sendMessage).mockRejectedValueOnce(new Error('nope'));
			held();
			await userEvent.type(box(), 'go on{Enter}');
			await screen.findByText('nope');

			await userEvent.type(box(), '{Enter}');

			await vi.waitFor(() => expect(screen.queryByText('nope')).not.toBeInTheDocument());
			expect(api.runs.sendMessage).toHaveBeenCalledTimes(2);
		});

		it('clears the reason when the hold is released', async () => {
			vi.mocked(api.runs.sendMessage).mockRejectedValueOnce(new Error('nope'));
			held({ onrelease: vi.fn(async () => true) });
			await userEvent.type(box(), 'go on{Enter}');
			await screen.findByText('nope');

			await userEvent.click(screen.getByRole('button', { name: 'Release' }));

			await vi.waitFor(() => expect(screen.queryByText('nope')).not.toBeInTheDocument());
		});
	});

	it('grows with the text, up to its cap', async () => {
		const height = vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(90);
		held();
		await userEvent.type(box(), 'a');
		expect(box()).toHaveStyle({ height: '90px' });

		height.mockReturnValue(900);
		await userEvent.type(box(), 'b');
		expect(box()).toHaveStyle({ height: '220px' });
	});
});
