import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import ErrorCard from './ErrorCard.svelte';

describe('ErrorCard', () => {
	it('announces the cause as an alert', () => {
		render(ErrorCard, { cause: 'The engine did not answer' });
		expect(screen.getByRole('alert')).toHaveTextContent('The engine did not answer');
	});

	it('offers nothing to press without handlers', () => {
		render(ErrorCard, { cause: 'Nope', action: 'View log' });
		expect(screen.queryByRole('button')).not.toBeInTheDocument();
	});

	it('retries by default', async () => {
		const onretry = vi.fn();
		render(ErrorCard, { cause: 'Nope', onretry });
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(onretry).toHaveBeenCalledOnce();
	});

	it('puts another label on the action', async () => {
		const onretry = vi.fn();
		render(ErrorCard, { cause: 'Nope', onretry, action: 'View log' });
		await userEvent.click(screen.getByRole('button', { name: 'View log' }));
		expect(onretry).toHaveBeenCalledOnce();
		expect(screen.queryByRole('button', { name: /Retry/ })).not.toBeInTheDocument();
	});

	it('dismisses from a named icon button', async () => {
		const ondismiss = vi.fn();
		render(ErrorCard, { cause: 'Nope', ondismiss });
		await userEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
		expect(ondismiss).toHaveBeenCalledOnce();
	});

	it('folds the detail until it is asked for', async () => {
		render(ErrorCard, { cause: 'Nope', detail: 'dial tcp: refused' });
		const fold = screen.getByText('Details').closest('details')!;
		expect(fold).toContainElement(screen.getByText('dial tcp: refused'));
		expect(fold.open).toBe(false);

		await userEvent.click(screen.getByText('Details'));
		expect(fold.open).toBe(true);
	});

	it('has no fold without a detail', () => {
		render(ErrorCard, { cause: 'Nope' });
		expect(screen.queryByText('Details')).not.toBeInTheDocument();
	});
});
