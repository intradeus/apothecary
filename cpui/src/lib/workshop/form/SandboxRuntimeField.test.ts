import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { bindable } from '$test/fixtures/app.svelte';
import type { RuntimeOffer, SandboxRuntime } from '$lib/domain';
import SandboxRuntimeField from './SandboxRuntimeField.svelte';

vi.mock('$lib/api', () => ({ api: { system: { runtimeOffer: vi.fn() } } }));

const sysbox: SandboxRuntime = {
	name: 'sysbox',
	capabilities: {
		nestedContainers: true,
		nestedKubernetes: true,
		userNamespaces: false,
		contained: true
	}
};
const privileged: SandboxRuntime = {
	name: 'privileged',
	capabilities: {
		nestedContainers: true,
		nestedKubernetes: true,
		userNamespaces: true,
		contained: false
	}
};
const plain: SandboxRuntime = {
	name: 'plain',
	capabilities: {
		nestedContainers: false,
		nestedKubernetes: false,
		userNamespaces: false,
		contained: true
	}
};
const offer = (o: Partial<RuntimeOffer> = {}): RuntimeOffer => ({
	offered: true,
	default: 'sysbox',
	approved: [sysbox, privileged],
	...o
});

beforeEach(() => vi.resetAllMocks());

describe('SandboxRuntimeField', () => {
	it('offers the default and each approved runtime, with what it can run, and binds the pick', async () => {
		vi.mocked(api.system.runtimeOffer).mockResolvedValue(offer());
		const props = bindable({ runtime: '', docker: false });
		render(SandboxRuntimeField, props);
		const def = await screen.findByRole('radio', { name: /The default \(sysbox\)/ });
		expect(def).toBeChecked();
		expect(screen.getByText(/Pick this if you need docker, compose/)).toBeInTheDocument();
		expect(screen.getByText(/Pick this only for trusted, internal use/)).toBeInTheDocument();
		await userEvent.click(screen.getByRole('radio', { name: /privileged/ }));
		expect(props.runtime).toBe('privileged');
		expect(screen.queryByText(/save will be refused/)).not.toBeInTheDocument();
	});

	it('says when to pick userns, and more where the cluster gives it user namespaces', async () => {
		const userns: SandboxRuntime = { name: 'userns', capabilities: sysbox.capabilities };
		vi.mocked(api.system.runtimeOffer).mockResolvedValue(offer({ approved: [sysbox, userns] }));
		const { unmount } = render(SandboxRuntimeField, { runtime: '', docker: false });
		expect(
			await screen.findByText(/Pick this if you need docker.*not code you do not trust/)
		).toBeInTheDocument();
		unmount();
		const wide: SandboxRuntime = {
			name: 'userns',
			capabilities: { ...sysbox.capabilities, userNamespaces: true }
		};
		vi.mocked(api.system.runtimeOffer).mockResolvedValue(offer({ approved: [sysbox, wide] }));
		render(SandboxRuntimeField, { runtime: '', docker: false });
		expect(
			await screen.findByText(/Pick this if you also need user-namespaced pods.*without kata’s VM/)
		).toBeInTheDocument();
	});

	it('says when to pick each known runtime, and what an unknown one can run', async () => {
		const kata: SandboxRuntime = {
			name: 'kata',
			capabilities: { ...sysbox.capabilities, userNamespaces: true }
		};
		const other: SandboxRuntime = { name: 'gvisor', capabilities: plain.capabilities };
		vi.mocked(api.system.runtimeOffer).mockResolvedValue(
			offer({ approved: [sysbox, kata, privileged, other] })
		);
		render(SandboxRuntimeField, { runtime: '', docker: false });
		expect(
			await screen.findByText(/Pick this if you need docker.*the lightest on memory/)
		).toBeInTheDocument();
		expect(
			screen.getByText(/Pick this only if you also need user-namespaced pods.*small VM/)
		).toBeInTheDocument();
		expect(screen.getByText(/no isolation, root inside is root on its node/)).toBeInTheDocument();
		// No capability lists for the ones the form knows.
		expect(screen.queryByText(/docker inside/)).not.toBeInTheDocument();
		// A runtime the form does not know keeps its capabilities.
		expect(screen.getByText('— contained from its node')).toBeInTheDocument();
	});

	it('shows nothing when there is no choice', async () => {
		vi.mocked(api.system.runtimeOffer).mockResolvedValue(offer({ approved: [sysbox] }));
		render(SandboxRuntimeField, { runtime: '', docker: false });
		await Promise.resolve();
		await Promise.resolve();
		expect(screen.queryByRole('radiogroup')).not.toBeInTheDocument();
		expect(screen.queryByText('Sandbox runtime')).not.toBeInTheDocument();
	});

	it('shows nothing for a provisioner without a choice, or an offer it cannot read', async () => {
		vi.mocked(api.system.runtimeOffer).mockResolvedValue(offer({ offered: false, approved: [] }));
		const { unmount } = render(SandboxRuntimeField, { runtime: '', docker: false });
		await Promise.resolve();
		expect(screen.queryByText('Sandbox runtime')).not.toBeInTheDocument();
		unmount();
		vi.mocked(api.system.runtimeOffer).mockRejectedValue(new Error('down'));
		render(SandboxRuntimeField, { runtime: '', docker: false });
		await Promise.resolve();
		expect(screen.queryByText('Sandbox runtime')).not.toBeInTheDocument();
	});

	it('says when the provisioner’s offer is broken', async () => {
		vi.mocked(api.system.runtimeOffer).mockResolvedValue(
			offer({ offered: false, approved: [], error: 'kubernetes: runtimes: unknown' })
		);
		render(SandboxRuntimeField, { runtime: '', docker: false });
		expect(await screen.findByText('kubernetes: runtimes: unknown')).toBeInTheDocument();
	});

	it('keeps a withdrawn pick visible and says the save will be refused', async () => {
		vi.mocked(api.system.runtimeOffer).mockResolvedValue(offer({ approved: [sysbox] }));
		render(SandboxRuntimeField, { runtime: 'privileged', docker: false });
		expect(await screen.findByRole('radio', { name: /privileged/ })).toBeChecked();
		expect(screen.getByText(/no longer approved/)).toBeInTheDocument();
		expect(screen.getByText(/privileged is not approved on this instance/)).toBeInTheDocument();
	});

	it('says a runtime without docker cannot serve Docker in workspaces', async () => {
		vi.mocked(api.system.runtimeOffer).mockResolvedValue(
			offer({ default: 'plain', approved: [plain, sysbox] })
		);
		render(SandboxRuntimeField, { runtime: '', docker: true });
		expect(
			await screen.findByText(/plain cannot run docker inside the sandbox/)
		).toBeInTheDocument();
	});

	it('labels a default the offer does not describe', async () => {
		vi.mocked(api.system.runtimeOffer).mockResolvedValue(offer({ default: 'kata' }));
		render(SandboxRuntimeField, { runtime: '', docker: false });
		expect(await screen.findByRole('radio', { name: /The default \(kata\)/ })).toBeInTheDocument();
	});
});
