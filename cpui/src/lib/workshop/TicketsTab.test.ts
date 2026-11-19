import { render, screen } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Project, TicketSource } from '$lib/domain';
import { fleet } from '$lib/app/fleet.svelte';
import { anOperatorProject, aTicketRow, aViewerProject } from '$test/fixtures';
import { aWorkshop } from '$test/fixtures/app.svelte';
import WithHeader from '$test/fixtures/WithHeader.svelte';
import TicketsTab from './TicketsTab.svelte';

vi.mock('$lib/api', () => ({ api: { projects: { tickets: vi.fn() } } }));

const own: TicketSource = { kind: 'internal', name: 'Mine', home: '' };
const jira: TicketSource = { kind: 'jira', name: 'Jira', home: 'https://acme.atlassian.net' };
const setup = (p: Project = aWorkshop({ id: 'p1', ticketSource: own })) =>
	render(WithHeader, { props: { page: TicketsTab, props: { project: p } } });

beforeEach(() => {
	vi.resetAllMocks();
	fleet.reset();
	vi.mocked(api.projects.tickets).mockResolvedValue({ tickets: [], next: '' });
});

describe('TicketsTab', () => {
	it('reads the workshop’s live list', async () => {
		vi.mocked(api.projects.tickets).mockResolvedValue({
			tickets: [aTicketRow({ key: 'P-1', title: 'Add keys' })],
			next: ''
		});
		setup();
		expect(await screen.findByRole('link', { name: 'Add keys' })).toHaveAttribute(
			'href',
			'/w/p1/tickets/P-1'
		);
		expect(api.projects.tickets).toHaveBeenCalledWith('p1', expect.anything());
	});

	it('offers no New ticket to a role without ticket:write', async () => {
		setup(aViewerProject({ id: 'p1', ticketSource: own }));
		await screen.findByText('No open tickets');
		expect(screen.queryByRole('link', { name: /New ticket/ })).not.toBeInTheDocument();
	});

	it('offers New ticket on the internal tracker, in the header and the empty list', async () => {
		const { container } = setup();
		expect(container.querySelector('header a')).toHaveAttribute('href', '/tickets/new?project=p1');
		await screen.findByText('No open tickets');
		expect(screen.getAllByRole('link', { name: /New ticket/ })).toHaveLength(2);
	});

	it("offers New ticket to an operator on a shared workshop's internal tracker", async () => {
		const { container } = setup(anOperatorProject({ id: 'p1', ticketSource: own }));
		expect(container.querySelector('header a')).toHaveAttribute('href', '/tickets/new?project=p1');
	});

	it('links a remote source to its tracker instead', async () => {
		const { container } = setup(aWorkshop({ id: 'p1', ticketSource: jira }));
		const link = container.querySelector('header a');
		expect(link).toHaveTextContent('Open in Jira');
		expect(link).toHaveAttribute('href', 'https://acme.atlassian.net');
		expect(screen.queryByRole('link', { name: /New ticket/ })).not.toBeInTheDocument();
	});

	it('offers neither while the source is gone or has no address', () => {
		const { container } = setup(aWorkshop({ id: 'p1', ticketSource: undefined }));
		expect(container.querySelector('header a')).toBeNull();
		setup(aWorkshop({ id: 'p1', ticketSource: { ...jira, home: '' } }));
		expect(screen.queryByRole('link', { name: /Open in/ })).not.toBeInTheDocument();
	});
});
