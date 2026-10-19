// The engine settings, shared by the Limits, Reaper and Storage sections of
// the System hub. One draft and one Save across all three: moving between
// sections keeps unsaved edits, and saving writes them together.

import { api } from '$lib/api';
import { fleet } from '$lib/fleet.svelte';
import type { Settings } from '$lib/domain';

class SettingsDraft {
	saved = $state<Settings | null>(null);
	draft = $state<Settings | null>(null);
	loading = $state(false);
	busy = $state(false);
	error = $state('');
	/** ms epoch of the last successful save in this tab. */
	savedAt = $state<number | null>(null);

	readonly dirty = $derived(
		!!this.saved && !!this.draft && JSON.stringify(this.saved) !== JSON.stringify(this.draft)
	);

	/** Load once; a later call refreshes only while nothing is unsaved. */
	async load() {
		if (this.dirty || this.loading) return;
		this.loading = true;
		this.error = '';
		try {
			const s = await api.system.settings();
			this.saved = s;
			this.draft = { ...s };
		} catch (e) {
			this.error = e instanceof Error ? e.message : String(e);
		} finally {
			this.loading = false;
		}
	}

	async save() {
		if (!this.draft) return;
		this.busy = true;
		this.error = '';
		try {
			const s = await api.system.saveSettings({
				...this.draft,
				maxConcurrentRuns: Number(this.draft.maxConcurrentRuns),
				maxEnvironments: Number(this.draft.maxEnvironments)
			});
			this.saved = s;
			this.draft = { ...s };
			this.savedAt = Date.now();
			fleet.invalidateSettings();
		} catch (e) {
			this.error = e instanceof Error ? e.message : String(e);
		} finally {
			this.busy = false;
		}
	}

	discard() {
		if (this.saved) this.draft = { ...this.saved };
		this.error = '';
	}
}

export const settingsDraft = new SettingsDraft();
