import { act, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { text } from '$test/helpers';
import SaveBar from './SaveBar.svelte';

const save = (name = 'Save') => screen.getByRole('button', { name });
const discard = () => screen.getByRole('button', { name: 'Discard' });

describe('SaveBar', () => {
	afterEach(() => {
		vi.useRealTimers();
	});

	it('has nothing to save on a clean form', () => {
		render(SaveBar, { dirty: false, ondiscard: vi.fn() });
		expect(save()).toBeDisabled();
		expect(discard()).toBeDisabled();
		expect(screen.queryByText('Unsaved changes')).toBeNull();
	});

	it('offers both once there are changes', () => {
		render(SaveBar, { dirty: true, ondiscard: vi.fn() });
		expect(save()).toBeEnabled();
		expect(discard()).toBeEnabled();
		expect(screen.getByText('Unsaved changes')).toBeInTheDocument();
	});

	it('calls back on Save and on Discard', async () => {
		const onsave = vi.fn();
		const ondiscard = vi.fn();
		render(SaveBar, { dirty: true, onsave, ondiscard });

		await userEvent.click(save());
		expect(onsave).toHaveBeenCalledOnce();
		expect(ondiscard).not.toHaveBeenCalled();

		await userEvent.click(discard());
		expect(ondiscard).toHaveBeenCalledOnce();
	});

	it('holds both buttons while a save is in flight', () => {
		render(SaveBar, { dirty: true, busy: true, ondiscard: vi.fn() });
		expect(save()).toBeDisabled();
		expect(save()).toHaveAttribute('aria-busy', 'true');
		expect(discard()).toBeDisabled();
	});

	it('has no Discard unless the page can discard', () => {
		render(SaveBar, { dirty: true });
		expect(screen.queryByRole('button', { name: 'Discard' })).toBeNull();
	});

	it('uses the label, the note and the extra controls it is given', () => {
		render(SaveBar, {
			dirty: true,
			saveLabel: 'Create workshop',
			note: 'Takes effect on the next run',
			extra: text('Run envcheck after')
		});
		expect(save('Create workshop')).toBeInTheDocument();
		expect(screen.getByText('Takes effect on the next run')).toBeInTheDocument();
		expect(screen.getByText('Run envcheck after')).toBeInTheDocument();
	});

	it('is a plain button by default, so it cannot submit a form around it', () => {
		render(SaveBar, { dirty: true });
		expect(save()).toHaveAttribute('type', 'button');
	});

	it('hands Save to the surrounding form when submit is set', async () => {
		const form = document.body.appendChild(document.createElement('form'));
		const submitted = vi.fn((e: Event) => e.preventDefault());
		form.addEventListener('submit', submitted);
		const onsave = vi.fn();
		render(SaveBar, { target: form, props: { dirty: true, submit: true, onsave } });

		await userEvent.click(save());

		expect(submitted).toHaveBeenCalledOnce();
		expect(onsave).not.toHaveBeenCalled();
		form.remove();
	});

	it('confirms a landed save for four seconds', async () => {
		vi.useFakeTimers({ now: 10_000 });
		render(SaveBar, { dirty: false, savedAt: 10_000 });
		expect(screen.getByText('Saved')).toBeInTheDocument();

		await act(() => vi.advanceTimersByTime(3_999));
		expect(screen.getByText('Saved')).toBeInTheDocument();

		await act(() => vi.advanceTimersByTime(1));
		expect(screen.queryByText('Saved')).toBeNull();
	});

	it('does not claim Saved for a save long past', () => {
		vi.useFakeTimers({ now: 60_000 });
		render(SaveBar, { dirty: false, savedAt: 10_000 });
		expect(screen.queryByText('Saved')).toBeNull();
	});

	it('shows Unsaved, not Saved, the moment the form changes again', async () => {
		vi.useFakeTimers({ now: 10_000 });
		const { rerender } = render(SaveBar, { dirty: false, savedAt: 10_000 });
		await rerender({ dirty: true });
		expect(screen.queryByText('Saved')).toBeNull();
		expect(screen.getByText('Unsaved changes')).toBeInTheDocument();
	});

	it('starts the four seconds over on the next save', async () => {
		vi.useFakeTimers({ now: 10_000 });
		const { rerender } = render(SaveBar, { dirty: false, savedAt: 10_000 });

		await act(() => vi.advanceTimersByTime(3_000));
		await rerender({ savedAt: 13_000 });
		await act(() => vi.advanceTimersByTime(3_000));
		expect(screen.getByText('Saved')).toBeInTheDocument();

		await act(() => vi.advanceTimersByTime(1_000));
		expect(screen.queryByText('Saved')).toBeNull();
	});
});
