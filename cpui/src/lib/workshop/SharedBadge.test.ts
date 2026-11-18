import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { aProject, aViewerProject } from '$test/fixtures';
import SharedBadge from './SharedBadge.svelte';

describe('SharedBadge', () => {
	it('names the owner and the caller’s role on a shared workshop', () => {
		render(SharedBadge, { project: aViewerProject() });
		expect(screen.getByText('shared by @ada · Viewer')).toBeInTheDocument();
	});

	it('names an operator role', () => {
		const p = aViewerProject();
		render(SharedBadge, { project: { ...p, access: { ...p.access, role: 'operator' } } });
		expect(screen.getByText('shared by @ada · Operator')).toBeInTheDocument();
	});

	it('shows nothing on one’s own workshop', () => {
		render(SharedBadge, { project: aProject() });
		expect(screen.queryByText(/shared by/)).toBeNull();
	});
});
