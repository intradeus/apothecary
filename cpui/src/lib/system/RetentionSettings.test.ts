import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Settings } from '$lib/domain';
import { settingsDraft } from './settings.svelte';
import RetentionSettings from './RetentionSettings.svelte';

vi.mock('$lib/api', () => ({
	api: { system: { settings: vi.fn(), saveSettings: vi.fn() } }
}));

const stored: Settings = {
	destroyCompletedAfter: '48h0m0s',
	destroyAbandoned: 'never',
	orphanUploadsAfter: '24h0m0s',
	snapshotsAfter: 'never'
};

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.system.settings).mockResolvedValue(stored);
	vi.mocked(api.system.saveSettings).mockImplementation(async (s) => s);
	settingsDraft.saved = null;
	settingsDraft.draft = null;
	settingsDraft.error = '';
	settingsDraft.savedAt = null;
});

describe('RetentionSettings', () => {
	it('shows the reaper and retention, one draft for both', async () => {
		render(RetentionSettings);
		expect(await screen.findByText('Workspaces of finished runs')).toBeInTheDocument();
		expect(screen.getByText('Retention')).toBeInTheDocument();
		expect(screen.getAllByRole('button', { name: 'Save' })).toHaveLength(1);
		expect(
			screen.getByRole('spinbutton', { name: 'Destroy completed environments after' })
		).toHaveValue(2);
	});

	it('saves an edit to either through the one bar', async () => {
		render(RetentionSettings);
		const sweep = await screen.findByRole('spinbutton', { name: 'Sweep unclaimed uploads after' });
		await userEvent.clear(sweep);
		await userEvent.type(sweep, '3');
		await userEvent.click(screen.getByRole('button', { name: 'Save' }));

		await waitFor(() => expect(api.system.saveSettings).toHaveBeenCalledOnce());
		expect(vi.mocked(api.system.saveSettings).mock.calls[0][0].orphanUploadsAfter).not.toBe(
			stored.orphanUploadsAfter
		);
	});
});
