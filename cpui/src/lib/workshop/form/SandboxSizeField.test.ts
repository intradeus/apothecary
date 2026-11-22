import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { bindable } from '$test/fixtures/app.svelte';
import { deferred } from '$test/helpers';
import type { SandboxRunUsage } from '$lib/domain';
import SandboxSizeField from './SandboxSizeField.svelte';

vi.mock('$lib/api', () => ({ api: { projects: { sandboxUsage: vi.fn() } } }));

const unset = { cpus: null, memoryGiB: null };
const GiB = 2 ** 30;
const run = (peakMem: number, oomKills = 0): SandboxRunUsage => ({
	runId: 'r',
	peakMem,
	memLimit: 8 * GiB,
	oomKills,
	at: '2026-11-22T10:00:00Z'
});

beforeEach(() => {
	vi.resetAllMocks();
	fleet.limits = {
		maxConcurrentRuns: 3,
		maxEnvironments: 6,
		sandbox: {
			default: { cpus: 2, memoryGiB: null },
			ceiling: { cpus: 4, memoryGiB: 8 },
			idleMinutes: 0
		}
	};
});

describe('SandboxSizeField', () => {
	it('binds the size, an unset field showing what applies instead', async () => {
		const props = bindable({ size: { ...unset } });
		render(SandboxSizeField, props);
		expect(screen.getByLabelText('CPUs')).toHaveAttribute('placeholder', 'default 2 cores');
		// No default: the ceiling limits it, it does not size it.
		expect(screen.getByLabelText('Memory')).toHaveAttribute('placeholder', 'up to 8 GiB');
		await userEvent.type(screen.getByLabelText('Memory'), '6');
		expect(props.size).toEqual({ cpus: null, memoryGiB: 6 });
		expect(api.projects.sandboxUsage).not.toHaveBeenCalled();
	});

	it('says a size changed above the ceiling will be refused', async () => {
		render(SandboxSizeField, bindable({ size: { cpus: 2, memoryGiB: null } }));
		await userEvent.type(screen.getByLabelText('Memory'), '16');
		expect(screen.getByLabelText('Memory')).toHaveAttribute('aria-invalid', 'true');
		expect(screen.getByText(/Above this instance’s ceiling \(4 CPUs, 8 GiB/)).toBeInTheDocument();
		expect(screen.getByText(/the save will be refused/)).toBeInTheDocument();
	});

	it('says a size kept above a ceiling lowered since is capped, not refused', () => {
		render(SandboxSizeField, { size: { cpus: 2, memoryGiB: 16 } });
		expect(screen.getByLabelText('Memory')).not.toHaveAttribute('aria-invalid');
		expect(screen.queryByText(/the save will be refused/)).not.toBeInTheDocument();
		expect(screen.getByText(/lowered since it was set/)).toHaveClass('hue-warning');
	});

	it('marks a changed field out of its bounds', async () => {
		render(SandboxSizeField, bindable({ size: { ...unset } }));
		await userEvent.type(screen.getByLabelText('CPUs'), '0.01');
		expect(screen.getByLabelText('CPUs')).toHaveAttribute('aria-invalid', 'true');
		expect(screen.getByText(/A size is 0.1 to 1000 CPUs/)).toBeInTheDocument();
	});

	it('works on an instance whose limits are not loaded yet', () => {
		fleet.limits = null;
		render(SandboxSizeField, { size: { cpus: 64, memoryGiB: null } });
		expect(screen.getByLabelText('CPUs')).not.toHaveAttribute('aria-invalid');
		expect(screen.getByLabelText('Memory')).toHaveAttribute('placeholder', 'no limit');
	});

	it('shows what the workshop’s recent runs reached', async () => {
		vi.mocked(api.projects.sandboxUsage).mockResolvedValue([run(3 * GiB), run(5 * GiB)]);
		render(SandboxSizeField, { size: { ...unset }, projectId: 'p' });
		expect(await screen.findByText(/Its last 2 runs peaked at 5\.00 GB/)).toBeInTheDocument();
		expect(api.projects.sandboxUsage).toHaveBeenCalledWith('p');
	});

	it('flags processes killed at the memory limit', async () => {
		vi.mocked(api.projects.sandboxUsage).mockResolvedValue([run(8 * GiB, 1)]);
		render(SandboxSizeField, { size: { ...unset }, projectId: 'p' });
		const line = await screen.findByText(/1 process was killed at the memory limit/);
		expect(line).toHaveClass('hue-danger');
		expect(line).toHaveTextContent('Its last run peaked at 8.00 GB');
	});

	it('says when no run was measured, and says nothing until it knows', async () => {
		const d = deferred<SandboxRunUsage[]>();
		vi.mocked(api.projects.sandboxUsage).mockReturnValue(d.promise);
		render(SandboxSizeField, { size: { ...unset }, projectId: 'p' });
		expect(screen.queryByText(/measured/)).not.toBeInTheDocument();
		d.resolve([]);
		expect(
			await screen.findByText(/No run of this workshop has been measured/)
		).toBeInTheDocument();
	});

	it('treats usage it cannot read as none', async () => {
		vi.mocked(api.projects.sandboxUsage).mockRejectedValue(new Error('down'));
		render(SandboxSizeField, { size: { ...unset }, projectId: 'p' });
		expect(
			await screen.findByText(/No run of this workshop has been measured/)
		).toBeInTheDocument();
	});
});
