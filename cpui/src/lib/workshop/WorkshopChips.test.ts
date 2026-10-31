import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { aWorkshop, anIntegration } from '$test/fixtures/app.svelte';
import WorkshopChips from './WorkshopChips.svelte';

const project = aWorkshop({ repoAccountId: 'r', provisionerId: 'p', harnessIds: ['h1', 'h2'] });
const integrations = [
	anIntegration({ id: 'r', name: 'Gitlab', kind: 'gitlab' }),
	anIntegration({ id: 'p', name: 'Docker', kind: 'docker' }),
	anIntegration({ id: 'h1', name: 'Claude', kind: 'claude' }),
	anIntegration({ id: 'h2', name: 'Codex', kind: 'codex' })
];

describe('WorkshopChips', () => {
	it('shows one chip per integration, harnesses each their own', () => {
		render(WorkshopChips, { project, integrations });
		expect(screen.getByText('Gitlab')).toHaveAttribute('title', expect.stringContaining('gitlab'));
		expect(screen.getByText('Docker')).toBeInTheDocument();
		expect(screen.getByText('Claude')).toBeInTheDocument();
		expect(screen.getByText('Codex')).toBeInTheDocument();
	});

	it('shows dots while integrations load, marking nothing missing', () => {
		const { container } = render(WorkshopChips, { project, integrations: null });
		expect(screen.getAllByText('…')).toHaveLength(4);
		expect(container.querySelector('.tint-danger')).toBeNull();
	});

	it('calls out an integration that no longer exists', () => {
		const { container } = render(WorkshopChips, { project, integrations: integrations.slice(1) });
		expect(screen.getByText('missing')).toHaveClass('tint-danger');
		expect(container.querySelectorAll('.tint-danger')).toHaveLength(1);
	});

	it('warns about a workshop with no harness', () => {
		render(WorkshopChips, { project: { ...project, harnessIds: [] }, integrations });
		expect(screen.getByText('none')).toHaveClass('tint-warning');
	});
});
