import { render, screen } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { settingsDraft } from './settings.svelte';
import ProvisionerPage from './ProvisionerPage.svelte';

vi.mock('$lib/api', () => ({
	isNotFound: () => false,
	api: {
		system: {
			provisioner: vi.fn(),
			kinds: vi.fn(),
			settings: vi.fn(),
			saveSettings: vi.fn(),
			setLimits: vi.fn()
		}
	}
}));

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.system.provisioner).mockReturnValue(new Promise(() => {}));
	vi.mocked(api.system.kinds).mockResolvedValue([]);
	vi.mocked(api.system.settings).mockReturnValue(new Promise(() => {}));
	settingsDraft.draft = null;
	fleet.limits = { maxConcurrentRuns: 2, maxEnvironments: 4 };
});

describe('ProvisionerPage', () => {
	it('holds the cluster, then the limits, then the reaper and retention', () => {
		render(ProvisionerPage);
		expect(screen.getByRole('heading', { name: 'Cluster' })).toBeInTheDocument();
		expect(screen.getByRole('heading', { name: 'Capacity' })).toBeInTheDocument();
		expect(document.getElementById('limits')).not.toBeNull();
		expect(api.system.settings).toHaveBeenCalled();
	});
});
