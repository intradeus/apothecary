import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { session } from '$lib/app/session.svelte';
import type { KindDescriptor, Me } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { anIntegration } from '$test/fixtures/app.svelte';
import ProvisionerCluster from './ProvisionerCluster.svelte';

vi.mock('$lib/api', () => ({
	isNotFound: (e: unknown) => (e as { status?: number } | null)?.status === 404,
	api: {
		system: {
			provisioner: vi.fn(),
			kinds: vi.fn(),
			saveProvisioner: vi.fn(),
			testProvisioner: vi.fn()
		},
		integrations: { test: vi.fn(), update: vi.fn() },
		credentials: { list: vi.fn(), providers: vi.fn() }
	}
}));

const k8s: KindDescriptor = {
	port: 'provisioner',
	kind: 'kubernetes',
	label: 'Kubernetes',
	fields: [{ name: 'namespace', label: 'Namespace', secret: false, required: false }]
};
const prov = anIntegration({
	id: 'p',
	name: 'Kubernetes',
	port: 'provisioner',
	kind: 'kubernetes',
	config: { namespace: 'ws' }
});
const signIn = (admin: boolean) =>
	(session.me = { user: { id: 'u1', handle: 'lu', name: 'Lu', admin } } as Me);

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.system.provisioner).mockResolvedValue(prov);
	vi.mocked(api.system.kinds).mockResolvedValue([k8s]);
	toasts.list = [];
	localStorage.clear();
	signIn(true);
});

describe('ProvisionerCluster', () => {
	it('shows the instance’s provisioner, built on by every workshop, with no Delete', async () => {
		render(ProvisionerCluster);
		expect(await screen.findByText('Kubernetes', { selector: '*:not(.pill)' })).toBeInTheDocument();
		expect(screen.getByText(/every workshop builds on it/)).toBeInTheDocument();
		expect(screen.getByText('ws')).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: /Delete/ })).not.toBeInTheDocument();
	});

	it('lets an admin test it through the instance’s route', async () => {
		vi.mocked(api.system.testProvisioner).mockResolvedValue({ ok: true, warnings: [] });
		render(ProvisionerCluster);
		await userEvent.click(await screen.findByRole('button', { name: 'Test' }));
		await waitFor(() => expect(api.system.testProvisioner).toHaveBeenCalledOnce());
		expect(api.integrations.test).not.toHaveBeenCalled();
	});

	it('lets an admin edit it, saving through the instance’s route', async () => {
		vi.mocked(api.system.saveProvisioner).mockResolvedValue(prov);
		render(ProvisionerCluster);
		await userEvent.click(await screen.findByRole('button', { name: 'Edit' }));
		const ns = screen.getByLabelText('Namespace');
		await userEvent.clear(ns);
		await userEvent.type(ns, 'apps');
		await userEvent.click(screen.getByRole('button', { name: 'Save changes' }));

		await waitFor(() =>
			expect(api.system.saveProvisioner).toHaveBeenCalledWith(
				expect.objectContaining({ config: { namespace: 'apps' } })
			)
		);
		expect(api.integrations.update).not.toHaveBeenCalled();
		expect(api.system.provisioner).toHaveBeenCalledTimes(2);
	});

	it('only shows it to everyone else', async () => {
		signIn(false);
		render(ProvisionerCluster);
		expect(await screen.findByText('Only an admin changes or tests it.')).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Edit' })).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Test' })).not.toBeInTheDocument();
	});

	it('says when the deployment names no cluster', async () => {
		vi.mocked(api.system.provisioner).mockRejectedValue(
			Object.assign(new Error('not found'), { status: 404 })
		);
		render(ProvisionerCluster);
		expect(await screen.findByText('No provisioner')).toBeInTheDocument();
	});

	it('shows a failed load, and retries it', async () => {
		vi.mocked(api.system.provisioner).mockRejectedValueOnce(new Error('engine down'));
		render(ProvisionerCluster);
		expect(await screen.findByText('engine down')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('ws')).toBeInTheDocument();
	});
});
