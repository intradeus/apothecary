import { render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import SecretsSection from './SecretsSection.svelte';

vi.mock('$lib/api', () => ({ api: { projects: { secrets: vi.fn() } } }));

describe('SecretsSection', () => {
	it("is step 7 and edits the workshop's secrets", async () => {
		vi.mocked(api.projects.secrets).mockResolvedValue([]);
		const { container } = render(SecretsSection, { projectId: 'p1' });
		expect(container.querySelector('section#secrets')).toBeInTheDocument();
		expect(await screen.findByText('No secrets yet.')).toBeInTheDocument();
		expect(api.projects.secrets).toHaveBeenCalledWith('p1');
	});
});
