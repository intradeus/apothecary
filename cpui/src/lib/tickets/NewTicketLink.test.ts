import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { anIntegration } from '$test/fixtures/app.svelte';
import NewTicketLink from './NewTicketLink.svelte';

const project = { id: 'p1', ticketSourceId: 'src' };
const setup = (integrations: Parameters<typeof anIntegration>[0][] | null) =>
	render(NewTicketLink, {
		project,
		integrations: integrations && integrations.map(anIntegration),
		label: 'New ticket',
		cls: 'btn-sm'
	});

describe('NewTicketLink', () => {
	it('writes a ticket here on the internal tracker', () => {
		setup([{ id: 'src', kind: 'internal', port: 'ticketsource' }]);
		const a = screen.getByRole('link', { name: /New ticket/ });
		expect(a).toHaveAttribute('href', '/tickets/new?project=p1');
		expect(a).toHaveClass('btn', 'btn-sm');
	});

	it('opens a remote tracker instead', () => {
		setup([
			{
				id: 'src',
				kind: 'jira',
				name: 'Jira',
				port: 'ticketsource',
				config: { base_url: 'https://acme.atlassian.net' }
			}
		]);
		expect(screen.getByRole('link', { name: /Open in Jira/ })).toHaveAttribute(
			'href',
			'https://acme.atlassian.net'
		);
		expect(screen.queryByRole('link', { name: /New ticket/ })).not.toBeInTheDocument();
	});

	it('offers nothing while loading or with no tracker address', () => {
		setup(null);
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});

	it('offers nothing for a remote tracker it cannot place', () => {
		setup([{ id: 'src', kind: 'custom', port: 'ticketsource' }]);
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});
});
