import { render, screen } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fleet } from '$lib/app/fleet.svelte';
import { aProject } from '$test/fixtures';
import WorkshopName from './WorkshopName.svelte';

vi.mock('$lib/api', () => ({ api: {} }));

beforeEach(() => {
	fleet.projects = [aProject({ id: 'p1', name: 'billing' })];
});

describe('WorkshopName', () => {
	it('names the workshop, as plain text by default', () => {
		render(WorkshopName, { projectId: 'p1' });
		expect(screen.getByText('billing')).toBeInTheDocument();
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});

	it('opens the workshop with link', () => {
		render(WorkshopName, { projectId: 'p1', link: true });
		expect(screen.getByRole('link', { name: 'billing' })).toHaveAttribute('href', '/w/p1');
	});

	it('shows a short id for one it does not know yet', () => {
		render(WorkshopName, { projectId: '01UNKNOWNPROJ00ABCDEF' });
		expect(screen.getByText('ABCDEF')).toBeInTheDocument();
	});

	it('shows a dash with no workshop at all', () => {
		render(WorkshopName, { projectId: undefined, link: true });
		expect(screen.getByText('—')).toBeInTheDocument();
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});
});
