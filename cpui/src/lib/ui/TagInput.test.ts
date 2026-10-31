import { fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import TagInput from './TagInput.svelte';

const input = () => screen.getByRole('textbox');
const chips = (root: HTMLElement) =>
	[...root.querySelectorAll('.pill')].map((c) => c.firstChild?.textContent);
const paste = (text: string) =>
	fireEvent.paste(input(), { clipboardData: { getData: () => text } });

describe('TagInput', () => {
	it('shows a chip per value and the catalogue placeholder', () => {
		const { container } = render(TagInput, { values: ['a.test', 'b.test'] });
		expect(chips(container)).toEqual(['a.test', 'b.test']);
		expect(input()).toHaveAttribute('placeholder', 'Add…');
	});

	it.each(['{Enter}', ','])('commits the draft on "%s" and clears it', async (key) => {
		const { container } = render(TagInput, { values: [] });

		await userEvent.type(input(), `a.test${key}`);

		expect(chips(container)).toEqual(['a.test']);
		expect(input()).toHaveValue('');
	});

	it('commits the draft on blur', async () => {
		const { container } = render(TagInput, { values: [] });

		await userEvent.type(input(), 'a.test');
		await userEvent.tab();

		expect(chips(container)).toEqual(['a.test']);
		expect(input()).toHaveValue('');
	});

	it('does not add a blank draft', async () => {
		const { container } = render(TagInput, { values: ['a.test'] });
		await userEvent.type(input(), '   {Enter}');
		expect(chips(container)).toEqual(['a.test']);
		expect(input()).toHaveValue('');
	});

	it('does not add a value twice', async () => {
		const { container } = render(TagInput, { values: ['a.test'] });
		await userEvent.type(input(), 'a.test{Enter}');
		expect(chips(container)).toEqual(['a.test']);
	});

	it('splits a paste on whitespace and commas, dropping repeats', async () => {
		const { container } = render(TagInput, { values: ['a.test'] });

		await paste('a.test, b.test\nc.test  d.test,,b.test');

		expect(chips(container)).toEqual(['a.test', 'b.test', 'c.test', 'd.test']);
		expect(input()).toHaveValue('');
	});

	it('leaves a single pasted word in the draft to finish typing', async () => {
		const { container } = render(TagInput, { values: [] });
		const notPrevented = await paste('*.test');
		expect(notPrevented).toBe(true);
		expect(chips(container)).toEqual([]);
	});

	it('pops the last chip on Backspace in an empty draft', async () => {
		const { container } = render(TagInput, { values: ['a.test', 'b.test'] });
		await userEvent.type(input(), '{Backspace}');
		expect(chips(container)).toEqual(['a.test']);
	});

	it('edits the draft, not the chips, on Backspace with text in it', async () => {
		const { container } = render(TagInput, { values: ['a.test'] });
		await userEvent.type(input(), 'bc{Backspace}');
		expect(chips(container)).toEqual(['a.test']);
		expect(input()).toHaveValue('b');
	});

	it('removes one chip with its ×', async () => {
		const { container } = render(TagInput, { values: ['a.test', 'b.test'] });
		await userEvent.click(screen.getByRole('button', { name: 'Remove a.test' }));
		expect(chips(container)).toEqual(['b.test']);
	});

	it('refuses what validate refuses and says why', async () => {
		const validate = (v: string) => (v.includes('.') ? null : 'not a domain');
		const { container } = render(TagInput, { values: [], validate });

		await userEvent.type(input(), 'nope{Enter}');

		expect(chips(container)).toEqual([]);
		expect(screen.getByText('nope: not a domain')).toBeInTheDocument();
	});

	it('keeps the good values of a paste with a bad one in it', async () => {
		const validate = (v: string) => (v.includes('.') ? null : 'not a domain');
		const { container } = render(TagInput, { values: [], validate });

		await paste('a.test nope b.test');

		expect(chips(container)).toEqual(['a.test', 'b.test']);
		expect(screen.getByText('nope: not a domain')).toBeInTheDocument();
	});

	it('clears the error on the next good value', async () => {
		const validate = (v: string) => (v.includes('.') ? null : 'not a domain');
		render(TagInput, { values: [], validate });

		await userEvent.type(input(), 'nope{Enter}');
		await userEvent.type(input(), 'a.test{Enter}');

		expect(screen.queryByText(/not a domain/)).toBeNull();
	});

	it('does not let Enter submit the form around it', async () => {
		const form = document.body.appendChild(document.createElement('form'));
		const submitted = vi.fn((e: Event) => e.preventDefault());
		form.addEventListener('submit', submitted);
		render(TagInput, { target: form, props: { values: [] } });

		await userEvent.type(input(), 'a.test{Enter}');

		expect(submitted).not.toHaveBeenCalled();
		form.remove();
	});

	it('sets chips in plain text when mono is off', () => {
		render(TagInput, { values: ['a.test'], mono: false });
		expect(screen.getByText('a.test')).not.toHaveClass('mono');
	});
});
