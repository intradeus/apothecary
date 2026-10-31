import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import { text } from '$test/helpers';
import AuthGate from './AuthGate.svelte';

const props = {
	title: 'Sign in',
	lede: 'Yours alone.',
	action: 'Go',
	onsubmit: vi.fn(),
	children: text('the fields')
};

describe('AuthGate', () => {
	it('frames the form: brand, title, lede, fields, action', () => {
		render(AuthGate, props);
		expect(screen.getByText('Apothecary')).toBeInTheDocument();
		expect(screen.getByRole('heading', { name: 'Sign in' })).toBeInTheDocument();
		expect(screen.getByText('Yours alone.')).toBeInTheDocument();
		expect(screen.getByText('the fields')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Go' })).toHaveAttribute('type', 'submit');
	});

	it('hands the submit to the page', async () => {
		const onsubmit = vi.fn((e: SubmitEvent) => e.preventDefault());
		const { container } = render(AuthGate, { ...props, onsubmit });
		await fireEvent.submit(container.querySelector('form')!);
		expect(onsubmit).toHaveBeenCalledOnce();
	});

	it('shows an error only when there is one', () => {
		const { rerender } = render(AuthGate, props);
		expect(screen.queryByText('refused')).not.toBeInTheDocument();
		void rerender({ error: 'refused' });
		expect(screen.getByText('refused')).toBeInTheDocument();
	});

	it('holds the action while busy', () => {
		render(AuthGate, { ...props, busy: true });
		const action = screen.getByRole('button', { name: 'Go' });
		expect(action).toBeDisabled();
		expect(action).toHaveAttribute('aria-busy', 'true');
	});

	it('renders the foot under the action when given one', () => {
		render(AuthGate, { ...props, foot: text('locked out?') });
		expect(screen.getByText('locked out?')).toBeInTheDocument();
	});
});
