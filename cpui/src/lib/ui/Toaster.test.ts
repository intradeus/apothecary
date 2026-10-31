import { act, fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { I } from '$lib/design/icons';
import Toaster from './Toaster.svelte';
import { toast, toasts, type ToastTone } from './toast.svelte';

const row = () => screen.getByRole('status');
const dismiss = () => screen.getByRole('button', { name: 'Dismiss' });
const shown = () =>
	screen.queryAllByRole('status').map((s) => s.querySelector('.grow')?.textContent);

/** Past the auto-dismiss, and past the outro that takes it off the page. */
const pass = (ms: number) => act(() => vi.advanceTimersByTime(ms));

describe('Toaster', () => {
	afterEach(() => {
		toasts.list = [];
		vi.useRealTimers();
	});

	it('announces politely', () => {
		const { container } = render(Toaster);
		expect(container.querySelector('.toaster')).toHaveAttribute('aria-live', 'polite');
	});

	it('shows nothing with nothing to say', () => {
		render(Toaster);
		expect(screen.queryByRole('status')).toBeNull();
	});

	it('shows what is raised, newest last', async () => {
		render(Toaster);
		await act(() => {
			toast('First');
			toast('Second');
		});
		expect(shown()).toEqual(['First', 'Second']);
	});

	it.each<[ToastTone, string, string]>([
		['ok', 'tone-success', I.ok],
		['bad', 'tone-danger', I.bad],
		['warn', 'tone-warning', I.warn],
		['info', 'tone-attention', I.info]
	])('draws a %s toast with its tone and glyph', async (tone, cls, glyph) => {
		render(Toaster);
		await act(() => void toast('Hello', { tone }));
		expect(row()).toHaveClass(cls);
		expect(row()).toHaveTextContent(glyph);
	});

	it('takes a toast away with its ×', async () => {
		render(Toaster);
		await act(() => void toast('Hello'));

		await userEvent.click(dismiss());

		expect(toasts.list).toEqual([]);
	});

	it('has no action button unless given an action', async () => {
		render(Toaster);
		await act(() => void toast('Hello'));
		expect(screen.getAllByRole('button')).toEqual([dismiss()]);
	});

	it('runs the action and takes the toast away', async () => {
		const run = vi.fn();
		render(Toaster);
		await act(() => void toast('Rule saved', { action: { label: 'Undo', run } }));

		await userEvent.click(screen.getByRole('button', { name: 'Undo' }));

		expect(run).toHaveBeenCalledOnce();
		expect(toasts.list).toEqual([]);
	});

	it('takes a toast away when swiped right', async () => {
		vi.useFakeTimers();
		vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(300);
		render(Toaster);
		await act(() => void toast('Hello'));
		const at = (clientX: number) => ({ pointerId: 1, button: 0, clientX, clientY: 0 });

		fireEvent.pointerDown(row(), at(0));
		fireEvent.pointerMove(row(), at(200));
		fireEvent.pointerUp(row(), at(200));
		await pass(400);

		expect(toasts.list).toEqual([]);
	});

	describe('its timer', () => {
		it('leaves after its time on screen, and not before', async () => {
			vi.useFakeTimers();
			render(Toaster);
			await act(() => void toast('Hello', { ms: 1000 }));

			await pass(999);
			expect(toasts.list).toHaveLength(1);

			await pass(1);
			expect(toasts.list).toEqual([]);
		});

		it('comes off the page once gone', async () => {
			vi.useFakeTimers();
			render(Toaster);
			await act(() => void toast('Hello', { ms: 1000 }));

			await pass(1000);
			await pass(1);

			expect(screen.queryByRole('status')).toBeNull();
		});

		it('gives each toast its own time', async () => {
			vi.useFakeTimers();
			render(Toaster);
			await act(() => {
				toast('Short', { ms: 1000 });
				toast('Long', { ms: 5000 });
			});

			await pass(1000);

			expect(toasts.list.map((t) => t.text)).toEqual(['Long']);
		});

		it('waits while hovered, however long', async () => {
			vi.useFakeTimers();
			render(Toaster);
			await act(() => void toast('Hello', { ms: 1000 }));

			await pass(200);
			await fireEvent.pointerEnter(row());
			await pass(60_000);

			expect(toasts.list).toHaveLength(1);
		});

		it('finishes the time it had left after the hover', async () => {
			vi.useFakeTimers();
			render(Toaster);
			await act(() => void toast('Hello', { ms: 4000 }));

			await pass(1000);
			await fireEvent.pointerEnter(row());
			await pass(5000);
			await fireEvent.pointerLeave(row());

			await pass(2999);
			expect(toasts.list).toHaveLength(1);
			await pass(1);
			expect(toasts.list).toEqual([]);
		});

		it('leaves time to read after a hover at the very end', async () => {
			vi.useFakeTimers();
			render(Toaster);
			await act(() => void toast('Hello', { ms: 1000 }));

			await pass(950);
			await fireEvent.pointerEnter(row());
			await fireEvent.pointerLeave(row());

			await pass(799);
			expect(toasts.list).toHaveLength(1);
			await pass(1);
			expect(toasts.list).toEqual([]);
		});

		it('drops its timer when the toast goes early', async () => {
			vi.useFakeTimers();
			render(Toaster);
			let id = 0;
			await act(() => void (id = toast('Hello', { ms: 1000 })));
			const dismissed = vi.spyOn(toasts, 'dismiss');

			await act(() => toasts.dismiss(id));
			await pass(1);
			dismissed.mockClear();
			await pass(5000);

			expect(dismissed).not.toHaveBeenCalled();
		});
	});
});
