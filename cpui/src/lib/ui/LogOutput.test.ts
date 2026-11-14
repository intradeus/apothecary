import { render, screen } from '@testing-library/svelte';
import userEvent, { type UserEvent } from '@testing-library/user-event';
import { createRawSnippet, tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { I } from '$lib/design/icons';
import { toasts } from './toast.svelte';
import LogOutput from './LogOutput.svelte';

type Props = Parameters<typeof render<typeof LogOutput>>[1];

const box = (name = 'Log output') => screen.getByRole('region', { name });
const btn = (name: string | RegExp) => screen.getByRole('button', { name });
const field = () => screen.getByRole('searchbox', { name: 'Find in the log' });
const marks = (c: HTMLElement) => [...c.querySelectorAll('mark')].map((m) => m.textContent);
const current = (c: HTMLElement) => c.querySelector('mark.current');
const raw = (html: string) => createRawSnippet(() => ({ render: () => html }));

/** A keydown on `el`; true when nothing cancelled it. */
function press(el: Element, init: KeyboardEventInit) {
	return el.dispatchEvent(
		new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init })
	);
}
/** jsdom has no layout: give the box a height to scroll in. */
function scrollable(el: HTMLElement, height = 1000) {
	Object.defineProperty(el, 'scrollHeight', { value: height, configurable: true });
	Object.defineProperty(el, 'clientHeight', { value: 200, configurable: true });
}
function scrollTo(el: HTMLElement, top: number) {
	el.scrollTop = top;
	el.dispatchEvent(new Event('scroll'));
}

function show(props: Props = { text: '' }) {
	return render(LogOutput, props as never);
}

describe('LogOutput', () => {
	beforeEach(() => {
		toasts.list = [];
		window.getSelection()?.removeAllRanges();
	});
	afterEach(() => {
		vi.restoreAllMocks();
		vi.useRealTimers();
	});

	describe('the lines', () => {
		it('shows one row per line, without a trailing blank', () => {
			show({ text: 'one\ntwo\n' });
			expect(screen.getByText('one').parentElement?.children).toHaveLength(2);
			expect(screen.getByText('two')).toBeInTheDocument();
		});

		it('keeps a blank line as a row of its own', () => {
			show({ text: 'one\n\nthree' });
			expect(screen.getByText('one').parentElement?.children).toHaveLength(3);
		});

		it('gives each line the class it is asked to', () => {
			show({
				text: 'E bad\nok',
				lineClass: (l: string) => (l.startsWith('E') ? 'hue-danger' : '')
			});
			expect(screen.getByText('E bad')).toHaveClass('hue-danger');
			expect(screen.getByText('ok').className).not.toMatch(/hue-/);
		});

		it('opens a URL on a click, and lets a drag across it select', () => {
			show({ text: 'see https://a.test/x now' });
			const link = screen.getByRole('link', { name: 'https://a.test/x' });
			expect(link).toHaveAttribute('href', 'https://a.test/x');
			expect(link).toHaveAttribute('target', '_blank');
			expect(link).toHaveAttribute('rel', 'noreferrer');
			expect(link).toHaveAttribute('draggable', 'false');
			expect(link.parentElement).toHaveTextContent('see https://a.test/x now');
		});

		it('links nothing but http(s)', () => {
			show({ text: 'javascript:alert(1)' });
			expect(screen.queryByRole('link')).not.toBeInTheDocument();
		});

		it.each([null, ''])('shows its placeholder for %j', (text) => {
			show({ text, placeholder: raw('<p>Nothing yet.</p>') });
			expect(screen.getByText('Nothing yet.')).toBeInTheDocument();
		});

		it('puts the parent’s controls in its toolbar, and its head above the lines', () => {
			show({ text: 'line', bar: raw('<button>Reload</button>'), head: raw('<p>Earlier</p>') });
			expect(btn('Reload').parentElement).toBe(btn('Find in the log').parentElement?.parentElement);
			expect(screen.getByText('Earlier').nextElementSibling).toBe(
				screen.getByText('line').parentElement
			);
		});

		it('can be scrolled and searched from the keyboard', () => {
			show({ text: 'x', label: 'Workspace log' });
			expect(box('Workspace log')).toHaveAttribute('tabindex', '0');
		});
	});

	describe('copying', () => {
		it('copies the whole log, and ticks', async () => {
			const user = userEvent.setup();
			const write = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
			show({ text: 'one\ntwo\n' });

			await user.click(btn('Copy the whole log'));

			expect(write).toHaveBeenCalledExactlyOnceWith('one\ntwo\n');
			expect(btn('Copied')).toHaveTextContent(I.ok);
			await vi.waitFor(() => expect(btn('Copy the whole log')).toHaveTextContent(I.copy), {
				timeout: 3000
			});
		});

		it('says in a toast when the browser refuses the clipboard', async () => {
			const user = userEvent.setup();
			vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'));
			show({ text: 'one' });

			await user.click(btn('Copy the whole log'));

			expect(toasts.list.map((x) => [x.text, x.tone])).toEqual([
				['Could not copy — the browser refused clipboard access.', 'bad']
			]);
		});

		it('has nothing to copy without text', () => {
			show({ text: null });
			expect(btn('Copy the whole log')).toBeDisabled();
		});
	});

	describe('following the bottom', () => {
		async function grown(text = 'first') {
			const view = show({ text });
			scrollable(box());
			return view;
		}

		it('stays at the bottom as the text grows', async () => {
			const { rerender } = await grown();
			await rerender({ text: 'first\nsecond' });
			expect(box().scrollTop).toBe(1000);
		});

		it('leaves a reader who scrolled up where they are', async () => {
			const { rerender } = await grown();
			scrollTo(box(), 100);
			await rerender({ text: 'first\nsecond' });
			expect(box().scrollTop).toBe(100);
		});

		it('follows again once the reader returns to the bottom', async () => {
			const { rerender } = await grown();
			scrollTo(box(), 100);
			scrollTo(box(), 790);
			await rerender({ text: 'first\nsecond' });
			expect(box().scrollTop).toBe(1000);
		});

		it('holds still while the reader has text selected in it, and follows once that ends', async () => {
			const { rerender } = await grown();
			scrollTo(box(), 800);
			const range = document.createRange();
			range.selectNodeContents(screen.getByText('first'));
			window.getSelection()!.addRange(range);

			await rerender({ text: 'first\nsecond' });
			expect(box().scrollTop).toBe(800);

			window.getSelection()!.removeAllRanges();
			await rerender({ text: 'first\nsecond\nthird' });
			expect(box().scrollTop).toBe(1000);
		});

		it('pays no heed to a selection elsewhere on the page', async () => {
			const { rerender } = await grown();
			const elsewhere = document.body.appendChild(document.createElement('p'));
			elsewhere.textContent = 'not the log';
			const range = document.createRange();
			range.selectNodeContents(elsewhere);
			window.getSelection()!.addRange(range);

			await rerender({ text: 'first\nsecond' });

			expect(box().scrollTop).toBe(1000);
			elsewhere.remove();
		});

		it('jumps to the top, and stops following', async () => {
			const user = userEvent.setup();
			const { rerender } = await grown();
			scrollTo(box(), 800);

			await user.click(btn('Scroll to the top'));
			expect(box().scrollTop).toBe(0);
			await rerender({ text: 'first\nsecond' });
			expect(box().scrollTop).toBe(0);
		});

		it('jumps to the bottom, and follows again', async () => {
			const user = userEvent.setup();
			const { rerender } = await grown();
			scrollTo(box(), 100);

			await user.click(btn('Scroll to the bottom and follow'));
			expect(box().scrollTop).toBe(1000);
			scrollable(box(), 1500);
			await rerender({ text: 'first\nsecond' });
			expect(box().scrollTop).toBe(1500);
		});

		it('follows again when its parent asks', async () => {
			const { component, rerender } = await grown();
			scrollTo(box(), 100);

			component.follow();
			expect(box().scrollTop).toBe(1000);
			scrollable(box(), 1500);
			await rerender({ text: 'first\nsecond' });
			expect(box().scrollTop).toBe(1500);
		});
	});

	describe('find', () => {
		const LOG = 'err one\nok\nerr two https://a.test/err\nERR three';

		async function finding(text = LOG, query = 'err') {
			const user = userEvent.setup();
			const view = show({ text });
			await user.click(btn('Find in the log'));
			if (query) await typed(user, query);
			return { ...view, user };
		}
		/** Typed into the find field, and searched once typing paused. */
		async function typed(user: UserEvent, query: string) {
			await user.type(field(), query);
			await vi.waitFor(() => expect(document.querySelector('.count')).not.toHaveTextContent(''));
		}

		it('searches once typing pauses, or at once on Enter', async () => {
			vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
			const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
			const { container } = show({ text: LOG });
			await user.click(btn('Find in the log'));

			await user.type(field(), 'er');
			expect(marks(container)).toEqual([]);
			await vi.advanceTimersByTimeAsync(200);
			expect(screen.getByText('1 of 4')).toBeInTheDocument();

			// "err t": in "err two" and "ERR three". Enter searches first, then steps.
			await user.type(field(), 'r t');
			await user.keyboard('{Enter}');
			expect(screen.getByText('1 of 2')).toBeInTheDocument();
			await user.keyboard('{Enter}');
			expect(screen.getByText('2 of 2')).toBeInTheDocument();
		});

		it('opens from its button, on the field', async () => {
			const user = userEvent.setup();
			show({ text: LOG });

			await user.click(btn('Find in the log'));

			expect(field()).toHaveFocus();
			expect(btn('Find in the log')).toHaveAttribute('aria-pressed', 'true');
		});

		it.each([
			['Ctrl+F', { ctrlKey: true }],
			['Cmd+F', { metaKey: true }]
		])('opens on %s from inside it, instead of the browser’s find', async (_, mods) => {
			show({ text: LOG });
			box().focus();

			const delivered = press(box(), { key: 'f', ...mods });

			expect(delivered).toBe(false);
			await vi.waitFor(() => expect(field()).toHaveFocus());
		});

		it('leaves Ctrl+Shift+F alone', () => {
			show({ text: LOG });
			expect(press(box(), { key: 'F', ctrlKey: true, shiftKey: true })).toBe(true);
			expect(screen.queryByRole('searchbox')).not.toBeInTheDocument();
		});

		it('highlights every match whatever its case, the first one current and in view', async () => {
			const into = vi.spyOn(Element.prototype, 'scrollIntoView');
			const { container } = await finding();

			expect(marks(container)).toEqual(['err', 'err', 'err', 'ERR']);
			expect(current(container)).toHaveTextContent('err');
			expect(current(container)?.parentElement).toHaveTextContent('err one');
			expect(screen.getByText('1 of 4')).toBeInTheDocument();
			expect(into.mock.contexts.at(-1)).toBe(current(container));
		});

		it('highlights a match inside a link, and keeps the link', async () => {
			const { container } = await finding(LOG, 'a.test');
			const link = screen.getByRole('link', { name: 'https://a.test/err' });
			expect(link).toContainElement(current(container) as HTMLElement);
		});

		it('steps through them with Enter and Shift+Enter, wrapping at both ends', async () => {
			const { user, container } = await finding();

			await user.keyboard('{Enter}');
			expect(screen.getByText('2 of 4')).toBeInTheDocument();
			expect(current(container)?.parentElement).toHaveTextContent('err two');

			await user.keyboard('{Shift>}{Enter}{/Shift}');
			await user.keyboard('{Shift>}{Enter}{/Shift}');
			expect(screen.getByText('4 of 4')).toBeInTheDocument();
			expect(current(container)).toHaveTextContent('ERR');

			await user.keyboard('{Enter}');
			expect(screen.getByText('1 of 4')).toBeInTheDocument();
		});

		it('steps with its buttons too', async () => {
			const { user } = await finding();

			await user.click(btn('Next match'));
			expect(screen.getByText('2 of 4')).toBeInTheDocument();
			await user.click(btn('Previous match'));
			await user.click(btn('Previous match'));
			expect(screen.getByText('4 of 4')).toBeInTheDocument();
		});

		it('does not step when Enter presses one of its buttons', async () => {
			await finding();
			press(btn('Previous match'), { key: 'Enter' });
			await tick();
			expect(screen.getByText('1 of 4')).toBeInTheDocument();
		});

		it('says when nothing matches, and has nowhere to step', async () => {
			const { user } = await finding(LOG, 'nope');

			expect(screen.getByText('No matches')).toBeInTheDocument();
			expect(btn('Next match')).toBeDisabled();
			expect(btn('Previous match')).toBeDisabled();
			await user.keyboard('{Enter}');
			expect(screen.getByText('No matches')).toBeInTheDocument();
		});

		it('says nothing before anything is typed', async () => {
			const { container } = await finding(LOG, '');
			expect(container.querySelector('.count')).toHaveTextContent('');
			expect(marks(container)).toEqual([]);
		});

		it('starts again from the first match of a new query', async () => {
			const { user } = await finding();
			await user.keyboard('{Enter}');
			await user.type(field(), ' t');
			expect(await screen.findByText('1 of 2')).toBeInTheDocument();
		});

		it('closes on Esc, clears the highlights, and keeps the desk from taking the Esc', async () => {
			const { container } = await finding();

			const delivered = press(field(), { key: 'Escape' });
			await tick();

			expect(delivered).toBe(false);
			expect(screen.queryByRole('searchbox')).not.toBeInTheDocument();
			expect(marks(container)).toEqual([]);
			expect(box()).toHaveFocus();
			expect(btn('Find in the log')).toHaveAttribute('aria-pressed', 'false');
		});

		it('closes from its close button and from its toggle', async () => {
			const { user, container } = await finding();
			await user.click(btn('Close find'));
			expect(marks(container)).toEqual([]);

			await user.click(btn('Find in the log'));
			expect(field()).toHaveValue('');
			await user.click(btn('Find in the log'));
			expect(screen.queryByRole('searchbox')).not.toBeInTheDocument();
		});

		it('keeps the same match current as the tail grows, and does not scroll away from it', async () => {
			const { user, rerender, container } = await finding('a err\nb err');
			await user.keyboard('{Enter}');
			scrollable(box());
			scrollTo(box(), 800);

			await rerender({ text: 'a err\nb err\nc err' });

			expect(screen.getByText('2 of 3')).toBeInTheDocument();
			expect(current(container)?.parentElement).toHaveTextContent('b err');
			expect(box().scrollTop).toBe(800);
		});

		it('makes the first match current when the tail brings one', async () => {
			const { user, rerender, container } = await finding('a\nb', '');
			await typed(user, 'err');
			expect(screen.getByText('No matches')).toBeInTheDocument();

			await rerender({ text: 'a\nb\nc err' });

			expect(screen.getByText('1 of 1')).toBeInTheDocument();
			expect(current(container)?.parentElement).toHaveTextContent('c err');
		});

		it('counts every match, and draws those within 300 lines of the current one', async () => {
			const text = Array.from({ length: 700 }, (_, i) => `x${i}`).join('\n');
			const { user, container } = await finding(text, 'x');
			const markedLines = () =>
				[...container.querySelectorAll('mark')].map((m) => m.parentElement!.textContent);

			expect(screen.getByText('1 of 700')).toBeInTheDocument();
			expect(markedLines()).toHaveLength(301);
			expect(markedLines().at(-1)).toBe('x300');

			await user.keyboard('{Shift>}{Enter}{/Shift}');

			expect(screen.getByText('700 of 700')).toBeInTheDocument();
			expect(markedLines()).toHaveLength(301);
			expect(markedLines()[0]).toBe('x399');
			expect(current(container)?.parentElement).toHaveTextContent('x699');
		});
	});
});
