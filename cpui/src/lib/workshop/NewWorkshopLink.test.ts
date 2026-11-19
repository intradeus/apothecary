import { render, screen } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { session } from '$lib/app/session.svelte';
import type { Me, UserRole } from '$lib/domain';
import NewWorkshopLink from './NewWorkshopLink.svelte';

vi.mock('$lib/api', () => ({ api: {} }));

const signIn = (role: UserRole) =>
	(session.me = { user: { id: 'u1', handle: 'lu', name: 'Lu', role } } as Me);

afterEach(() => {
	session.me = null;
});

describe('NewWorkshopLink', () => {
	it.each(['admin', 'member'] as const)('leads a %s to the new-workshop page', (role) => {
		signIn(role);
		render(NewWorkshopLink, { label: '+ New workshop', class: 'btn btn-lg' });
		const link = screen.getByRole('link', { name: '+ New workshop' });
		expect(link).toHaveAttribute('href', '/w/new');
		expect(link).toHaveClass('btn', 'btn-lg');
	});

	it('offers a guest nothing: it creates no workshop', () => {
		signIn('guest');
		render(NewWorkshopLink, { label: '+ New workshop' });
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});
});
