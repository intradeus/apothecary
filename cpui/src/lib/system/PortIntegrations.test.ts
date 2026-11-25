import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import type { KindDescriptor } from '$lib/domain';
import { anIntegration, aWorkshop } from '$test/fixtures/app.svelte';
import PortIntegrations from './PortIntegrations.svelte';
import WithHeader from '$test/fixtures/WithHeader.svelte';

vi.mock('$lib/api', () => ({
	api: {
		system: { kinds: vi.fn() },
		integrations: { list: vi.fn(), create: vi.fn(), update: vi.fn(), remove: vi.fn() },
		credentials: { list: vi.fn(), providers: vi.fn() }
	}
}));

const kind = (over: Partial<KindDescriptor>): KindDescriptor => ({
	port: 'repository',
	kind: 'gitlab',
	label: 'GitLab',
	fields: [],
	...over
});
const KINDS = [
	kind({}),
	kind({ kind: 'github', label: 'GitHub' }),
	kind({ port: 'harness', kind: 'claude', label: 'Claude Code' })
];

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.system.kinds).mockResolvedValue(KINDS);
	vi.mocked(api.integrations.list).mockResolvedValue([
		anIntegration({ id: 'r1', name: 'Work GitLab', port: 'repository', kind: 'gitlab' }),
		anIntegration({ id: 'h1', name: 'Claude', port: 'harness', kind: 'claude' })
	]);
	vi.mocked(api.credentials.list).mockResolvedValue([]);
	vi.mocked(api.credentials.providers).mockResolvedValue([]);
	vi.spyOn(fleet, 'refresh').mockResolvedValue();
	fleet.projects = [aWorkshop({ name: 'billing', repoAccountId: 'r1' })];
});

describe('PortIntegrations', () => {
	it('lists only the integrations of its own kind, with who uses them', async () => {
		render(WithHeader, { props: { page: PortIntegrations, props: { port: 'repository' } } });
		expect(await screen.findByText('Work GitLab')).toBeInTheDocument();
		expect(screen.queryByText('Claude')).not.toBeInTheDocument();
		expect(screen.getByRole('heading', { name: 'Repositories' })).toBeInTheDocument();
		expect(screen.getByText(/billing/)).toBeInTheDocument();
	});

	it('adds from a menu of the port’s kinds when it has several', async () => {
		render(WithHeader, { props: { page: PortIntegrations, props: { port: 'repository' } } });
		await userEvent.click(await screen.findByRole('button', { name: /Add repository/ }));
		const menu = screen.getByRole('menu');
		expect(
			within(menu)
				.getAllByRole('menuitem')
				.map((m) => m.textContent)
		).toEqual(['GitLab', 'GitHub']);

		await userEvent.click(within(menu).getByRole('menuitem', { name: 'GitHub' }));
		expect(screen.getByText('New GitHub')).toBeInTheDocument();
	});

	it('adds with one button when the port has a single kind, and closes on Cancel', async () => {
		render(WithHeader, { props: { page: PortIntegrations, props: { port: 'harness' } } });
		await userEvent.click(await screen.findByRole('button', { name: /Add harness/ }));
		expect(screen.getByText('New Claude Code')).toBeInTheDocument();

		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
		await waitFor(() => expect(screen.queryByText('New Claude Code')).not.toBeInTheDocument());
	});

	it('says what a missing one means', async () => {
		vi.mocked(api.integrations.list).mockResolvedValue([]);
		const { unmount } = render(WithHeader, {
			props: { page: PortIntegrations, props: { port: 'repository' } }
		});
		expect(await screen.findByText('A workshop needs one repository to run.')).toBeInTheDocument();
		unmount();

		vi.mocked(api.system.kinds).mockResolvedValue([
			...KINDS,
			kind({ port: 'ticketsource', kind: 'jira', label: 'Jira' })
		]);
		render(WithHeader, { props: { page: PortIntegrations, props: { port: 'ticketsource' } } });
		expect(await screen.findByText(/tickets can always be written by hand/)).toBeInTheDocument();
	});

	it('shows an engine with no kinds for the port', async () => {
		render(WithHeader, { props: { page: PortIntegrations, props: { port: 'ticketsource' } } });
		expect(await screen.findByText('No kinds registered')).toBeInTheDocument();
	});

	it('shows a failed load, and retries it', async () => {
		vi.mocked(api.integrations.list).mockRejectedValueOnce(new Error('engine down'));
		render(WithHeader, { props: { page: PortIntegrations, props: { port: 'repository' } } });
		expect(await screen.findByText('engine down')).toBeInTheDocument();

		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('Work GitLab')).toBeInTheDocument();
	});
});
