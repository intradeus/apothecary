import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import SearchInput from './SearchInput.svelte';

describe('SearchInput', () => {
	it('falls back on the catalogue for its placeholder and name', () => {
		render(SearchInput, { value: '' });
		const input = screen.getByRole('searchbox', { name: 'Search…' });
		expect(input).toHaveAttribute('placeholder', 'Search…');
	});

	it('is named by its placeholder when that is all it has', () => {
		render(SearchInput, { value: '', placeholder: 'Find a run' });
		expect(screen.getByRole('searchbox', { name: 'Find a run' })).toBeInTheDocument();
	});

	it('prefers its label over the placeholder for the name', () => {
		render(SearchInput, { value: '', placeholder: 'Find a run', label: 'Filter runs' });
		expect(screen.getByRole('searchbox', { name: 'Filter runs' })).toHaveAttribute(
			'placeholder',
			'Find a run'
		);
	});

	it('shows the value it is given and what is typed', async () => {
		render(SearchInput, { value: 'pay' });
		const input = screen.getByRole('searchbox');
		expect(input).toHaveValue('pay');
		await userEvent.type(input, 'ment');
		expect(input).toHaveValue('payment');
	});

	it('hands the text to onenter on Enter', async () => {
		const onenter = vi.fn();
		render(SearchInput, { value: '', onenter });

		await userEvent.type(screen.getByRole('searchbox'), 'webhook{Enter}');

		expect(onenter).toHaveBeenCalledExactlyOnceWith('webhook');
	});

	it('never submits the form around it', async () => {
		const form = document.body.appendChild(document.createElement('form'));
		const submitted = vi.fn((e: Event) => e.preventDefault());
		form.addEventListener('submit', submitted);
		render(SearchInput, { target: form, props: { value: '' } });

		await userEvent.type(screen.getByRole('searchbox'), 'x{Enter}');

		expect(submitted).not.toHaveBeenCalled();
		form.remove();
	});

	it('caps its width as asked', () => {
		const { container } = render(SearchInput, { value: '', width: 120 });
		expect(container.querySelector<HTMLElement>('.si')?.style.getPropertyValue('--w')).toBe(
			'120px'
		);
	});
});
