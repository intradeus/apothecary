import { fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { createRawSnippet } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import PromptBox from './PromptBox.svelte';

const snippet = (html: string) => createRawSnippet(() => ({ render: () => html }));

describe('PromptBox', () => {
	it('is named by its label, its placeholder the label unless given', () => {
		render(PromptBox, { label: 'Ask', onsubmit: vi.fn() });
		expect(screen.getByRole('textbox', { name: 'Ask' })).toHaveAttribute('placeholder', 'Ask');
	});

	it('submits on ↵ and keeps ⇧↵ a newline', async () => {
		const onsubmit = vi.fn();
		const oninput = vi.fn();
		render(PromptBox, { label: 'Ask', placeholder: 'Your question', onsubmit, oninput });
		const box = screen.getByRole('textbox', { name: 'Ask' });
		expect(box).toHaveAttribute('placeholder', 'Your question');
		await userEvent.type(box, 'one{Shift>}{Enter}{/Shift}two');
		expect(box).toHaveValue('one\ntwo');
		expect(onsubmit).not.toHaveBeenCalled();
		expect(oninput).toHaveBeenLastCalledWith('one\ntwo');
		await userEvent.keyboard('{Enter}');
		expect(onsubmit).toHaveBeenCalledTimes(1);
	});

	it('never submits mid-composition', async () => {
		const onsubmit = vi.fn();
		render(PromptBox, { label: 'Ask', onsubmit });
		await fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter', isComposing: true });
		expect(onsubmit).not.toHaveBeenCalled();
	});

	it('shows its keys, its trouble and what the caller puts in it', () => {
		render(PromptBox, {
			label: 'Ask',
			sendKey: 'ask',
			error: 'refused',
			onsubmit: vi.fn(),
			top: snippet('<span>chip</span>'),
			extras: snippet('<span>files</span>'),
			actions: snippet('<button>Go</button>')
		});
		expect(screen.getByText('ask')).toBeInTheDocument();
		expect(screen.getByText('newline')).toBeInTheDocument();
		expect(screen.getByText('refused')).toHaveAttribute('title', 'refused');
		expect(screen.getByText('chip')).toBeInTheDocument();
		expect(screen.getByText('files')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Go' })).toBeInTheDocument();
	});

	it('locks while disabled or readonly', () => {
		const { unmount } = render(PromptBox, { label: 'Ask', disabled: true, onsubmit: vi.fn() });
		expect(screen.getByRole('textbox')).toBeDisabled();
		unmount();
		render(PromptBox, { label: 'Ask', readonly: true, onsubmit: vi.fn() });
		expect(screen.getByRole('textbox')).toHaveAttribute('readonly');
	});
});
