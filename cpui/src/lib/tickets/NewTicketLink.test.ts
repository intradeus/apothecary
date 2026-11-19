import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { anOperatorProject, aProject, aViewerProject } from '$test/fixtures';
import type { Project, TicketSource } from '$lib/domain';
import NewTicketLink from './NewTicketLink.svelte';

const internal: TicketSource = { kind: 'internal', name: 'Internal', home: '' };
const jira: TicketSource = { kind: 'jira', name: 'Jira', home: 'https://acme.atlassian.net' };
const setup = (p: Project) =>
	render(NewTicketLink, { project: p, label: 'New ticket', cls: 'btn-sm' });

describe('NewTicketLink', () => {
	it('writes a ticket here on the internal tracker', () => {
		setup(aProject({ id: 'p1', ticketSource: internal }));
		const a = screen.getByRole('link', { name: /New ticket/ });
		expect(a).toHaveAttribute('href', '/tickets/new?project=p1');
		expect(a).toHaveClass('btn', 'btn-sm');
	});

	it("writes one on a shared workshop's internal tracker for an operator", () => {
		setup(anOperatorProject({ id: 'p1', ticketSource: internal }));
		expect(screen.getByRole('link', { name: /New ticket/ })).toHaveAttribute(
			'href',
			'/tickets/new?project=p1'
		);
	});

	it('offers nothing to a role that may not write tickets (ticket:write)', () => {
		setup(aViewerProject({ id: 'p1', ticketSource: internal }));
		expect(screen.queryByRole('link', { name: /New ticket/ })).not.toBeInTheDocument();
	});

	it("opens a shared workshop's remote tracker for an operator", () => {
		setup(anOperatorProject({ ticketSource: jira }));
		expect(screen.getByRole('link', { name: /Open in Jira/ })).toHaveAttribute(
			'href',
			'https://acme.atlassian.net'
		);
		expect(screen.queryByRole('link', { name: /New ticket/ })).not.toBeInTheDocument();
	});

	it('offers nothing when the source is gone', () => {
		setup(aProject({ ticketSource: undefined }));
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});

	it('offers nothing for a remote tracker it cannot place', () => {
		setup(aProject({ ticketSource: { kind: 'custom', name: 'Custom', home: '' } }));
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});
});
