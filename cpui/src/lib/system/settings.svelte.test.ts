import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import type { Settings } from '$lib/domain';
import { deferred } from '$test/helpers';
import { settingsDraft } from './settings.svelte';

vi.mock('$lib/api', () => ({
	api: { system: { settings: vi.fn(), saveSettings: vi.fn() } }
}));

const settings = (over: Record<string, unknown> = {}) =>
	({ reaperHours: 48, keepLogs: true, ...over }) as unknown as Settings;

beforeEach(() => {
	vi.resetAllMocks();
	settingsDraft.saved = null;
	settingsDraft.draft = null;
	settingsDraft.loading = false;
	settingsDraft.busy = false;
	settingsDraft.error = '';
	settingsDraft.savedAt = null;
});

const edit = (over: Record<string, unknown>) => {
	settingsDraft.draft = { ...settingsDraft.draft!, ...over };
};

describe('settingsDraft.load', () => {
	it('fills the draft from the engine, clean', async () => {
		vi.mocked(api.system.settings).mockResolvedValue(settings());
		await settingsDraft.load();
		expect(settingsDraft.draft).toEqual(settings());
		expect(settingsDraft.dirty).toBe(false);
		expect(settingsDraft.loading).toBe(false);
	});

	it('refreshes while nothing is unsaved', async () => {
		vi.mocked(api.system.settings).mockResolvedValueOnce(settings());
		await settingsDraft.load();
		vi.mocked(api.system.settings).mockResolvedValueOnce(settings({ reaperHours: 12 }));
		await settingsDraft.load();
		expect(settingsDraft.saved).toEqual(settings({ reaperHours: 12 }));
	});

	it('leaves unsaved edits alone', async () => {
		vi.mocked(api.system.settings).mockResolvedValue(settings());
		await settingsDraft.load();
		edit({ reaperHours: 1 });

		await settingsDraft.load();

		expect(api.system.settings).toHaveBeenCalledOnce();
		expect(settingsDraft.draft).toEqual(settings({ reaperHours: 1 }));
	});

	it('does not stack a second load on one in flight', async () => {
		const load = deferred<Settings>();
		vi.mocked(api.system.settings).mockReturnValue(load.promise);

		const first = settingsDraft.load();
		await settingsDraft.load();
		load.resolve(settings());
		await first;

		expect(api.system.settings).toHaveBeenCalledOnce();
	});

	it('reports a failure', async () => {
		vi.mocked(api.system.settings).mockRejectedValue(new Error('engine down'));
		await settingsDraft.load();
		expect(settingsDraft.error).toBe('engine down');
		expect(settingsDraft.loading).toBe(false);
		expect(settingsDraft.draft).toBeNull();
	});
});

describe('settingsDraft.dirty', () => {
	it('is false before anything loaded', () => {
		expect(settingsDraft.dirty).toBe(false);
	});

	it('follows the edit, there and back', async () => {
		vi.mocked(api.system.settings).mockResolvedValue(settings());
		await settingsDraft.load();

		edit({ reaperHours: 1 });
		expect(settingsDraft.dirty).toBe(true);
		edit({ reaperHours: 48 });
		expect(settingsDraft.dirty).toBe(false);
	});
});

describe('settingsDraft.save', () => {
	beforeEach(async () => {
		vi.mocked(api.system.settings).mockResolvedValue(settings());
		await settingsDraft.load();
	});

	it('does nothing with nothing loaded', async () => {
		settingsDraft.draft = null;
		await settingsDraft.save();
		expect(api.system.saveSettings).not.toHaveBeenCalled();
	});

	it('writes the draft and takes what the engine kept', async () => {
		vi.useFakeTimers({ now: 5000 });
		edit({ reaperHours: 1 });
		vi.mocked(api.system.saveSettings).mockResolvedValue(settings({ reaperHours: 2 }));

		await settingsDraft.save();
		vi.useRealTimers();

		expect(api.system.saveSettings).toHaveBeenCalledWith(settings({ reaperHours: 1 }));
		expect(settingsDraft.draft).toEqual(settings({ reaperHours: 2 }));
		expect(settingsDraft.dirty).toBe(false);
		expect(settingsDraft.savedAt).toBe(5000);
	});

	it('is busy while it saves', async () => {
		const save = deferred<Settings>();
		vi.mocked(api.system.saveSettings).mockReturnValue(save.promise);

		const saving = settingsDraft.save();
		expect(settingsDraft.busy).toBe(true);

		save.resolve(settings());
		await saving;
		expect(settingsDraft.busy).toBe(false);
	});

	it('keeps the edits and says why when the save fails', async () => {
		edit({ reaperHours: 1 });
		vi.mocked(api.system.saveSettings).mockRejectedValue(new Error('refused'));

		await settingsDraft.save();

		expect(settingsDraft.error).toBe('refused');
		expect(settingsDraft.dirty).toBe(true);
		expect(settingsDraft.savedAt).toBeNull();
		expect(settingsDraft.busy).toBe(false);
	});
});

describe('settingsDraft.discard', () => {
	it('goes back to what is saved and clears the error', async () => {
		vi.mocked(api.system.settings).mockResolvedValue(settings());
		await settingsDraft.load();
		edit({ reaperHours: 1 });
		settingsDraft.error = 'refused';

		settingsDraft.discard();

		expect(settingsDraft.draft).toEqual(settings());
		expect(settingsDraft.dirty).toBe(false);
		expect(settingsDraft.error).toBe('');
	});
});
