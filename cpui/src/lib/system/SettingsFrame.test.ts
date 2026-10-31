import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { createRawSnippet } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Settings } from '$lib/domain';
import { toasts } from '$lib/ui/toast.svelte';
import { text } from '$test/helpers';
import { settingsDraft } from './settings.svelte';
import SettingsFrame from './SettingsFrame.svelte';

vi.mock('$lib/api', () => ({
	api: { system: { settings: vi.fn(), saveSettings: vi.fn() } }
}));

interface Hours {
	reaperHours: number;
}
const settings = { reaperHours: 48 } as unknown as Settings;
const save = () => screen.getByRole('button', { name: 'Save' });
const discard = () => screen.getByRole('button', { name: 'Discard' });
const edit = (reaperHours: number) => {
	settingsDraft.draft = { ...settingsDraft.draft!, reaperHours } as unknown as Settings;
};

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.system.settings).mockResolvedValue(settings);
	settingsDraft.saved = null;
	settingsDraft.draft = null;
	settingsDraft.loading = false;
	settingsDraft.busy = false;
	settingsDraft.error = '';
	settingsDraft.savedAt = null;
	toasts.list = [];
});

describe('SettingsFrame', () => {
	it('loads and hands the draft to its section', async () => {
		const children = createRawSnippet<[Settings]>((draft) => ({
			render: () => `<span>hours: ${(draft() as unknown as Hours).reaperHours}</span>`
		}));
		render(SettingsFrame, { children });
		expect(await screen.findByText('hours: 48')).toBeInTheDocument();
		expect(save()).toBeDisabled();
	});

	it('shows no section and no save until it has loaded', () => {
		vi.mocked(api.system.settings).mockReturnValue(new Promise(() => {}));
		render(SettingsFrame, { children: text('body') as never });
		expect(screen.queryByText('body')).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument();
	});

	it('offers a retry when loading fails', async () => {
		vi.mocked(api.system.settings).mockRejectedValueOnce(new Error('down'));
		render(SettingsFrame, { children: text('body') as never });

		expect(await screen.findByText('Couldn’t load the engine settings')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
		expect(await screen.findByText('body')).toBeInTheDocument();
	});

	it('saves the draft and confirms with a toast', async () => {
		vi.mocked(api.system.saveSettings).mockImplementation(async (s) => s);
		render(SettingsFrame, { children: text('body') as never });
		await screen.findByText('body');

		edit(1);
		await waitFor(() => expect(save()).toBeEnabled());
		await userEvent.click(save());

		expect(api.system.saveSettings).toHaveBeenCalledWith({ reaperHours: 1 });
		await waitFor(() => expect(toasts.list.at(-1)?.text).toBe('Settings saved · applied now'));
	});

	it('shows a refusal in place, keeps the edits, and raises no toast', async () => {
		vi.mocked(api.system.saveSettings).mockRejectedValue(new Error('too low'));
		render(SettingsFrame, { children: text('body') as never });
		await screen.findByText('body');

		edit(1);
		await waitFor(() => expect(save()).toBeEnabled());
		await userEvent.click(save());

		expect(await screen.findByText('The engine refused these settings')).toBeInTheDocument();
		expect(screen.getByText('too low')).toBeInTheDocument();
		expect(toasts.list).toEqual([]);
		expect(settingsDraft.dirty).toBe(true);
	});

	it('discards the edits', async () => {
		render(SettingsFrame, { children: text('body') as never });
		await screen.findByText('body');
		edit(1);
		await waitFor(() => expect(discard()).toBeEnabled());

		await userEvent.click(discard());

		expect(settingsDraft.dirty).toBe(false);
		await waitFor(() => expect(discard()).toBeDisabled());
	});
});
