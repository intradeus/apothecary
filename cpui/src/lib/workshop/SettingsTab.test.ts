import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { goto } from '$app/navigation';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { confirmState } from '$lib/ui/confirm.svelte';
import { toasts } from '$lib/ui/toast.svelte';
import { aRun } from '$test/fixtures';
import { aFlow, anIntegration, aWorkshop, confirmed } from '$test/fixtures/app.svelte';
import SettingsTab from './SettingsTab.svelte';

vi.mock('$lib/api', () => ({
	api: {
		projects: {
			update: vi.fn(),
			remove: vi.fn(),
			startEnvCheck: vi.fn(),
			browser: vi.fn(),
			secrets: vi.fn()
		},
		integrations: { repos: vi.fn() },
		egress: { defaults: vi.fn() }
	}
}));

const project = aWorkshop({
	id: 'p1',
	name: 'billing',
	repoAccountId: 'repo-1',
	provisionerId: 'prov-1',
	harnessIds: ['h-a'],
	defaultFlow: 'mine'
});
const integrations = [
	anIntegration({ id: 'repo-1', name: 'Gitlab', port: 'repository', kind: 'gitlab' }),
	anIntegration({ id: 'prov-1', name: 'K8s', port: 'provisioner', kind: 'kubernetes' }),
	anIntegration({ id: 'h-a', name: 'Claude', port: 'harness', kind: 'claude' })
];
const flows = [aFlow({ name: 'mine' })];

function setup(over: Record<string, unknown> = {}) {
	const onsaved = vi.fn();
	render(SettingsTab, { project, integrations, flows, runs: [], onsaved, ...over });
	return { onsaved };
}
const del = () => screen.getByRole('button', { name: 'Delete workshop' });
const nameField = () => screen.getByLabelText('Workshop name');

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.projects.update).mockImplementation(async (id, d) =>
		aWorkshop({ ...project, id, name: d.name })
	);
	vi.mocked(api.projects.browser).mockResolvedValue({
		enabled: false,
		available: true,
		state: 'absent',
		sessions: 0
	});
	vi.mocked(api.projects.secrets).mockResolvedValue([]);
	vi.spyOn(fleet, 'refresh').mockResolvedValue();
	confirmState.current = null;
	toasts.list = [];
});

describe('SettingsTab', () => {
	it('waits for the integrations and flows the form snapshots', () => {
		setup({ integrations: null });
		expect(screen.queryByRole('form')).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Save setup' })).not.toBeInTheDocument();
	});

	it('shows the setup form with the delete section at its foot', () => {
		setup();
		expect(nameField()).toHaveValue('billing');
		expect(screen.getByRole('heading', { name: 'Delete this workshop' })).toBeInTheDocument();
		expect(del()).toBeEnabled();
	});

	it('saves, hands the workshop up and starts the form again from it', async () => {
		const { onsaved } = setup();
		await userEvent.clear(nameField());
		await userEvent.type(nameField(), 'ledger');
		await userEvent.click(screen.getByRole('button', { name: 'Save setup' }));

		await waitFor(() =>
			expect(onsaved).toHaveBeenCalledWith(expect.objectContaining({ name: 'ledger' }))
		);
		expect(fleet.refresh).toHaveBeenCalled();
		expect(goto).not.toHaveBeenCalled();
		// A fresh form: nothing left to save.
		await waitFor(() => expect(screen.getByRole('button', { name: 'Save setup' })).toBeDisabled());
	});

	it('opens the Health tab when an envcheck was started after saving', async () => {
		vi.mocked(api.projects.startEnvCheck).mockResolvedValue(undefined as never);
		setup();
		await userEvent.click(screen.getByRole('switch', { name: 'Run envcheck after saving' }));
		await userEvent.click(screen.getByRole('button', { name: 'Save setup' }));

		await waitFor(() => expect(goto).toHaveBeenCalledWith('/w/p1?tab=health'));
	});

	it('discards the edits on Cancel', async () => {
		setup();
		await userEvent.clear(nameField());
		await userEvent.type(nameField(), 'scratch');
		await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));

		expect(nameField()).toHaveValue('billing');
	});

	it('refuses to delete while runs are not archived, and points at them', () => {
		setup({
			runs: [
				aRun({ projectId: 'p1' }),
				aRun({ projectId: 'p1', archivedAt: '2026-11-01T00:00:00Z' })
			]
		});
		expect(del()).toBeDisabled();
		expect(screen.getByText(/1 run still belongs to this workshop/)).toBeInTheDocument();
		expect(screen.getByRole('link', { name: /Show runs/ })).toHaveAttribute(
			'href',
			'/w/p1?tab=runs'
		);
	});

	it('deletes once confirmed, then leaves for the workshops', async () => {
		vi.mocked(api.projects.remove).mockResolvedValue(undefined as never);
		setup({ runs: [aRun({ projectId: 'p1', archivedAt: '2026-11-01T00:00:00Z' })] });
		await userEvent.click(del());
		await confirmed(true);

		await waitFor(() => expect(goto).toHaveBeenCalledWith('/w'));
		expect(api.projects.remove).toHaveBeenCalledWith('p1');
		expect(toasts.list.at(-1)?.text).toBe('billing deleted');
	});

	it('keeps it when called off, and shows a refused delete', async () => {
		setup();
		await userEvent.click(del());
		await confirmed(false);
		expect(api.projects.remove).not.toHaveBeenCalled();

		vi.mocked(api.projects.remove).mockRejectedValue(new Error('runs remain'));
		await userEvent.click(del());
		await confirmed(true);
		expect(await screen.findByText('runs remain')).toBeInTheDocument();
		expect(goto).not.toHaveBeenCalled();
	});
});
