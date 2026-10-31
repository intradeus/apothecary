import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { html } from '$test/fixtures/designsystem.svelte';
import EmptyState from './EmptyState.svelte';

describe('EmptyState', () => {
	it('gives the title, the line and the one action', () => {
		render(EmptyState, {
			title: 'An empty bench',
			text: 'Nothing is brewing.',
			children: html('<button>New run</button>')
		});
		expect(screen.getByText('An empty bench')).toBeInTheDocument();
		expect(screen.getByText('Nothing is brewing.')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'New run' })).toBeInTheDocument();
	});

	it('stands on its title alone', () => {
		const { container } = render(EmptyState, { title: 'An empty bench' });
		expect(container.firstElementChild!.children).toHaveLength(1);
	});

	it.each(['compact', 'inline'] as const)('has a %s form', (form) => {
		render(EmptyState, { title: 'Quiet', [form]: true });
		expect(screen.getByText('Quiet').parentElement).toHaveClass(form);
	});
});
