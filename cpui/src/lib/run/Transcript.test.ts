import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fmtClock, fmtTime } from '$lib/core/format';
import { aToolCall } from '$test/fixtures/run-components';
import type { ChatItem } from './transcriptItems';
import Transcript from './Transcript.svelte';

const AT = '2026-10-31T12:00:00Z';

type Of<K extends ChatItem['kind']> = Omit<Extract<ChatItem, { kind: K }>, 'kind' | 'key'>;
const item = <K extends ChatItem['kind']>(kind: K, over: Of<K>, key = kind as string) =>
	({ kind, key, ...over }) as unknown as ChatItem;

const stage = (over: Partial<Of<'stage'>> = {}) =>
	item('stage', { stageId: 'build', iteration: 2, agentic: true, ts: AT, ...over });
const message = (text: string, over: Partial<Of<'message'>> = {}, key = text) =>
	item('message', { text, thought: false, live: false, ...over }, key);
const user = (text: string, over: Partial<Of<'user'>> = {}, key = text) =>
	item('user', { id: 'u1', text, delivered: true, ts: AT, ...over }, key);
const question = (over: Partial<Of<'question'>> = {}) =>
	item('question', { pingId: 'p1', type: 'debug', question: 'Which DB?', options: [], ...over });

function show(items: ChatItem[], props: { sending?: { text: string }[]; over?: boolean } = {}) {
	const onlog = vi.fn();
	const onanswer = vi.fn();
	const view = render(Transcript, { items, onlog, onanswer, ...props });
	return { onlog, onanswer, ...view };
}

describe('Transcript', () => {
	it('shows nothing for nothing', () => {
		const { container } = show([]);
		expect(container).toHaveTextContent('');
	});

	it('keeps the items in order', () => {
		const { container } = show([
			item('notice', { text: 'one' }, 'a'),
			item('notice', { text: 'two' }, 'b'),
			item('notice', { text: 'three' }, 'c')
		]);
		expect(container).toHaveTextContent(/^one\s*two\s*three$/);
	});

	describe('a stage divider', () => {
		it('names the stage, the attempt, the agent, the model and the time', () => {
			const { container } = show([stage({ agent: 'builder', model: 'acme/m1' })]);

			expect(screen.getByText('build')).toBeInTheDocument();
			expect(container).toHaveTextContent(`attempt 2 · builder · acme/m1 · ${fmtTime(AT)}`);
		});

		it('says "stage" for one with no agent', () => {
			const { container } = show([stage({ agentic: false })]);
			expect(container).toHaveTextContent(`attempt 2 · stage · ${fmtTime(AT)}`);
		});
	});

	describe('a status line', () => {
		it.each([
			['ok', 'hue-success'],
			['bad', 'hue-danger'],
			['warn', 'hue-attention'],
			['info', 'hue-running']
		] as const)('takes its hue from the tone %s', (tone, hue) => {
			show([item('status', { glyph: '✓', text: 'build completed', tone })]);
			expect(screen.getByText('build completed').closest('.status')).toHaveClass(hue);
		});

		it('adds its meta after the text', () => {
			show([item('status', { glyph: '↩', text: 'looped', meta: '3 turns', tone: 'info' })]);

			expect(screen.getByText('↩')).toBeInTheDocument();
			expect(screen.getByText('· 3 turns')).toBeInTheDocument();
		});
	});

	describe('a failure', () => {
		it('shows the cause and the detail, and opens the stage log', async () => {
			const { onlog } = show([
				item('failure', { stageId: 'build', title: 'build failed', error: 'exit 1' })
			]);

			const card = screen.getByRole('alert');
			expect(within(card).getByText('build failed')).toBeInTheDocument();
			expect(within(card).getByText('exit 1')).toBeInTheDocument();

			await userEvent.click(within(card).getByRole('button', { name: 'View log' }));
			expect(onlog).toHaveBeenCalledExactlyOnceWith('build');
		});
	});

	describe('a prompt', () => {
		const prompt = (over: Partial<Of<'prompt'>> = {}) =>
			item('prompt', { promptKind: 'stage', text: 'Do the thing', truncated: false, ...over });

		it.each([
			['stage', 'Stage prompt'],
			['answer', 'Your answers, delivered'],
			['restart', 'Engine restarted — continue'],
			['messages', 'Your messages'],
			['handback', 'Handed back — finish the task'],
			['custom', 'custom']
		])('labels the kind %s', (promptKind, label) => {
			show([prompt({ promptKind })]);
			expect(screen.getByText(label)).toBeInTheDocument();
		});

		it('previews the first line that has words and folds the rest', () => {
			const { container } = show([prompt({ text: '\n\n  \nFirst words\nmore below' })]);

			expect(screen.getByText('First words')).toBeInTheDocument();
			expect(container.querySelector('details')).not.toHaveAttribute('open');
		});

		it('carries the whole text, marked when cut', () => {
			const { container, unmount } = show([prompt({ text: 'one\ntwo', truncated: true })]);
			expect(container.querySelector('pre')?.textContent).toBe('one\ntwo\n… [truncated]');
			unmount();

			const whole = show([prompt({ text: 'one\ntwo' })]);
			expect(whole.container.querySelector('pre')?.textContent).toBe('one\ntwo');
		});
	});

	describe('an agent message', () => {
		it('renders as markdown', () => {
			show([message('Use **the** middleware')]);
			expect(screen.getByText('the').tagName).toBe('STRONG');
		});

		it('wears a caret while it streams, and only then', () => {
			const { container, unmount } = show([message('Writing', { live: true })]);
			expect(container.querySelector('.caret')).toBeInTheDocument();
			unmount();

			const done = show([message('Written')]);
			expect(done.container.querySelector('.caret')).not.toBeInTheDocument();
		});
	});

	describe('thinking', () => {
		it('stays open while live', () => {
			const { container } = show([message('Hmm', { thought: true, live: true })]);

			expect(screen.getByText(/Thinking/)).toBeInTheDocument();
			expect(container.querySelector('details')).toHaveAttribute('open');
			expect(container.querySelector('.live-dot')).toBeInTheDocument();
		});

		it('folds once finished', () => {
			const { container } = show([message('Hmm', { thought: true })]);

			expect(container.querySelector('details')).not.toHaveAttribute('open');
			expect(container.querySelector('.live-dot')).not.toBeInTheDocument();
		});

		it('is plain text, not markdown', () => {
			show([message('**raw**', { thought: true })]);
			expect(screen.getByText('**raw**')).toBeInTheDocument();
		});
	});

	describe('a tool group', () => {
		const four = Array.from({ length: 4 }, (_, i) =>
			aToolCall({ toolCallId: `c${i}`, title: `Call ${i}` })
		);

		it('shows its calls', () => {
			show([item('tools', { calls: [aToolCall({ title: 'grep foo' })], live: false })]);
			expect(screen.getByText('grep foo')).toBeInTheDocument();
		});

		it('hands it whether it is live', () => {
			const { unmount } = show([item('tools', { calls: four, live: true })]);
			expect(screen.getByText('Call 3')).toBeInTheDocument();
			unmount();

			show([item('tools', { calls: four, live: false })]);
			expect(screen.queryByText('Call 3')).not.toBeInTheDocument();
		});
	});

	describe('the plan', () => {
		const entries = [
			{ content: 'Read the router', status: 'completed' },
			{ content: 'Add the key', status: 'in_progress' },
			{ content: 'Test it', status: 'pending' }
		];

		it('counts what is done', () => {
			show([item('plan', { entries })]);

			expect(screen.getByText('Checklist')).toBeInTheDocument();
			expect(screen.getByText('1 of 3')).toBeInTheDocument();
		});

		it('strikes what is done and marks each entry', () => {
			show([item('plan', { entries })]);

			expect(screen.getByText('Read the router').tagName).toBe('S');
			expect(screen.getByText('Add the key').tagName).toBe('SPAN');
			expect(screen.getByRole('img', { name: 'ok' })).toBeInTheDocument();
			expect(screen.getByRole('img', { name: 'live' })).toBeInTheDocument();
			expect(screen.getByRole('img', { name: 'pending' })).toBeInTheDocument();
		});
	});

	it('shows a notice', () => {
		show([item('notice', { text: 'Model switched' })]);
		expect(screen.getByText('Model switched')).toBeInTheDocument();
	});

	describe('your message', () => {
		it('says when it was delivered', () => {
			show([user('go on', { deliveredAt: '2026-10-31T13:21:00Z' })]);

			expect(screen.getByText('go on')).not.toHaveClass('queued');
			expect(
				screen.getByText(`you · delivered ${fmtClock('2026-10-31T13:21:00Z')}`)
			).toBeInTheDocument();
		});

		it('falls back to when it was sent', () => {
			show([user('go on')]);
			expect(screen.getByText(`you · delivered ${fmtClock(AT)}`)).toBeInTheDocument();
		});

		it('says it is queued until then', () => {
			show([user('go on', { delivered: false })]);

			expect(screen.getByText('go on')).toHaveClass('queued');
			expect(screen.getByText('queued — read when its turn ends')).toBeInTheDocument();
		});
	});

	describe('a message on its way', () => {
		it('shows as sending at once, after everything else', () => {
			const { container } = show([message('earlier')], { sending: [{ text: 'go on' }] });

			expect(screen.getByText('go on')).toHaveClass('queued');
			expect(screen.getByText('sending…')).toBeInTheDocument();
			expect(container.textContent?.indexOf('earlier')).toBeLessThan(
				container.textContent!.indexOf('go on')
			);
		});

		it('goes when the run echoes it back', () => {
			show([user('go on')], { sending: [{ text: 'go on' }] });

			expect(screen.getAllByText('go on')).toHaveLength(1);
			expect(screen.queryByText('sending…')).not.toBeInTheDocument();
		});

		it('keeps the ones not echoed yet', () => {
			show([user('first')], { sending: [{ text: 'first' }, { text: 'second' }] });

			expect(screen.getAllByText('first')).toHaveLength(1);
			expect(screen.getByText('second')).toBeInTheDocument();
			expect(screen.getAllByText('sending…')).toHaveLength(1);
		});
	});

	describe('a question', () => {
		it('shows the type and the question, as markdown', () => {
			show([question({ question: 'Which **DB**?' })]);

			expect(screen.getByText('The agent asked')).toBeInTheDocument();
			expect(screen.getByText('DB').tagName).toBe('STRONG');
		});

		it('lists its options until answered', () => {
			const { unmount } = show([question({ options: ['pg', 'sqlite'] })]);
			expect(screen.getByText('options: pg · sqlite')).toBeInTheDocument();
			unmount();

			show([question({ options: ['pg', 'sqlite'], answer: 'pg' })]);
			expect(screen.queryByText(/^options:/)).not.toBeInTheDocument();
		});

		it('lists no options when it has none', () => {
			show([question()]);
			expect(screen.queryByText(/^options:/)).not.toBeInTheDocument();
		});

		it('waits for you and jumps to where it is answered', async () => {
			const { onanswer } = show([question()]);

			await userEvent.click(screen.getByRole('button', { name: /Waiting for your answer/ }));

			expect(onanswer).toHaveBeenCalledOnce();
		});

		it('shows your answer in place of the wait', () => {
			show([question({ answer: 'pg, please' })]);

			expect(screen.getByText('You:')).toBeInTheDocument();
			expect(screen.getByText('You:').parentElement).toHaveTextContent('You: pg, please');
			expect(screen.queryByRole('button')).not.toBeInTheDocument();
		});

		it('counts an empty answer as answered', () => {
			show([question({ answer: '' })]);
			expect(screen.queryByRole('button')).not.toBeInTheDocument();
		});

		it('is withdrawn when the run ends unanswered', () => {
			show([question()], { over: true });

			expect(screen.getByText('Withdrawn — the run ended before an answer.')).toBeInTheDocument();
			expect(screen.queryByRole('button')).not.toBeInTheDocument();
		});

		it('keeps an answer given before the run ended', () => {
			show([question({ answer: 'pg' })], { over: true });

			expect(screen.getByText('You:')).toBeInTheDocument();
			expect(screen.queryByText(/^Withdrawn/)).not.toBeInTheDocument();
		});

		it('draws the eye only while open', () => {
			const { container, unmount } = show([question()]);
			expect(container.querySelector('.q')).toHaveClass('open');
			unmount();

			const answered = show([question({ answer: 'pg' })]);
			expect(answered.container.querySelector('.q')).not.toHaveClass('open');
			answered.unmount();

			const over = show([question()], { over: true });
			expect(over.container.querySelector('.q')).not.toHaveClass('open');
		});
	});

	describe('arrival motion', () => {
		let animate: ReturnType<typeof vi.spyOn>;

		beforeEach(() => {
			vi.useFakeTimers();
			animate = vi.spyOn(Element.prototype, 'animate');
		});
		afterEach(() => {
			vi.useRealTimers();
		});

		const notice = (n: number) => item('notice', { text: `n${n}` }, `n${n}`);

		it('leaves the history alone', () => {
			show([notice(1), notice(2)]);
			vi.advanceTimersByTime(1000);
			expect(animate).not.toHaveBeenCalled();
		});

		it('lets the replay burst behind the history land quietly', async () => {
			const { rerender } = show([notice(1)]);

			vi.advanceTimersByTime(600);
			await rerender({ items: [notice(1), notice(2)] });
			vi.advanceTimersByTime(600);
			await rerender({ items: [notice(1), notice(2), notice(3)] });

			expect(animate).not.toHaveBeenCalled();
		});

		it('moves what arrives once the list has settled', async () => {
			const { rerender } = show([notice(1)]);
			vi.advanceTimersByTime(700);

			await rerender({ items: [notice(1), notice(2)] });

			expect(animate).toHaveBeenCalledTimes(2); // the fade, the rise
		});

		it('does not move a persisted message over its live twin', async () => {
			const { rerender } = show([notice(1)]);
			vi.advanceTimersByTime(700);

			await rerender({ items: [notice(1), message('done')] });

			expect(animate).not.toHaveBeenCalled();
		});

		it('moves a message while it is live', async () => {
			const { rerender } = show([notice(1)]);
			vi.advanceTimersByTime(700);

			await rerender({ items: [notice(1), message('going', { live: true })] });

			expect(animate).toHaveBeenCalled();
		});

		it('does not move your message echoing its bubble', async () => {
			const sending = [{ text: 'go on' }];
			const { rerender } = show([notice(1)], { sending });
			vi.advanceTimersByTime(700);

			await rerender({ items: [notice(1), user('go on')] });

			expect(animate).not.toHaveBeenCalled();
		});

		it('moves a bubble you just sent', async () => {
			const { rerender } = show([notice(1)]);
			vi.advanceTimersByTime(700);

			await rerender({ sending: [{ text: 'go on' }] });

			expect(animate).toHaveBeenCalled();
		});

		it('stays out of the way of a plan moving to its latest spot', async () => {
			const { rerender } = show([notice(1)]);
			vi.advanceTimersByTime(700);

			await rerender({ items: [notice(1), item('plan', { entries: [] })] });

			expect(animate).not.toHaveBeenCalled();
		});
	});
});
