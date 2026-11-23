import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { bindable } from '$test/fixtures/app.svelte';
import { IDLE_NEVER, type RuntimeOffer } from '$lib/domain';
import SandboxIdleField from './SandboxIdleField.svelte';

vi.mock('$lib/api', () => ({ api: { system: { runtimeOffer: vi.fn() } } }));

const offer = (o: Partial<RuntimeOffer> = {}): RuntimeOffer => ({
	offered: true,
	default: 'sysbox',
	approved: [],
	...o
});
const caps = {
	nestedContainers: true,
	nestedKubernetes: true,
	userNamespaces: true,
	contained: true
};
const limits = (idleMinutes: number) => ({
	maxConcurrentRuns: 3,
	maxEnvironments: 6,
	sandbox: {
		default: { cpus: null, memoryGiB: null },
		ceiling: { cpus: null, memoryGiB: null },
		idleMinutes
	}
});

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.system.runtimeOffer).mockResolvedValue(offer());
	fleet.limits = limits(20);
});

describe('SandboxIdleField', () => {
	it("shows the instance's default and binds never and minutes of its own", async () => {
		const props = bindable({ idleMinutes: 0 });
		render(SandboxIdleField, props);
		expect(screen.getByRole('radio', { name: /The instance's default/ })).toBeChecked();
		expect(screen.getByText(/— after 20 min$/)).toBeInTheDocument();
		await userEvent.click(screen.getByRole('radio', { name: /Never/ }));
		expect(props.idleMinutes).toBe(IDLE_NEVER);
		await userEvent.click(screen.getByRole('radio', { name: /minutes of its own/ }));
		expect(props.idleMinutes).toBe(30);
		const input = screen.getByRole('spinbutton', { name: 'Minutes unused' });
		await userEvent.clear(input);
		await userEvent.type(input, '45');
		expect(props.idleMinutes).toBe(45);
		await userEvent.clear(input);
		expect(screen.getByText(/whole number of minutes/)).toBeInTheDocument();
		await userEvent.click(screen.getByRole('radio', { name: /The instance's default/ }));
		expect(props.idleMinutes).toBe(0);
	});

	it('opens on what is saved, and reads an instance default of 0 as never', () => {
		fleet.limits = limits(0);
		render(SandboxIdleField, bindable({ idleMinutes: 90 }));
		expect(screen.getByRole('radio', { name: /minutes of its own/ })).toBeChecked();
		expect(screen.getByRole('spinbutton')).toHaveValue(90);
		expect(screen.getByText(/— never$/)).toBeInTheDocument();
	});

	it('says why sandboxes cannot be stopped here, and that minutes will be refused', async () => {
		vi.mocked(api.system.runtimeOffer).mockResolvedValue(offer({ noSleep: 'emptydir storage' }));
		render(SandboxIdleField, bindable({ idleMinutes: 15 }));
		expect(
			await screen.findByText(/never stopped when idle: emptydir storage/)
		).toBeInTheDocument();
		expect(screen.getByText(/save will be refused/)).toBeInTheDocument();
	});

	it("says why under the workshop's runtime alone: a VM's disks go with it", async () => {
		vi.mocked(api.system.runtimeOffer).mockResolvedValue(
			offer({
				approved: [
					{ name: 'sysbox', capabilities: caps },
					{
						name: 'kata',
						capabilities: caps,
						noSleep: 'a kata sandbox keeps its disks inside its VM'
					}
				]
			})
		);
		const { rerender } = render(SandboxIdleField, { idleMinutes: 15, runtime: 'kata' });
		expect(await screen.findByText(/never stopped when idle: a kata sandbox/)).toBeInTheDocument();
		await rerender({ idleMinutes: 15, runtime: '' });
		expect(screen.queryByText(/never stopped when idle/)).not.toBeInTheDocument();
	});

	it('treats an unreadable offer, or none at all, as sleeping allowed', async () => {
		vi.mocked(api.system.runtimeOffer).mockRejectedValue(new Error('down'));
		fleet.limits = null;
		render(SandboxIdleField, bindable({ idleMinutes: IDLE_NEVER }));
		expect(screen.getByRole('radio', { name: /Never/ })).toBeChecked();
		await waitFor(() => expect(api.system.runtimeOffer).toHaveBeenCalled());
		expect(screen.getByText(/A sandbox nobody uses/)).toBeInTheDocument();
		expect(screen.getByText(/— never$/)).toBeInTheDocument();
	});
});
