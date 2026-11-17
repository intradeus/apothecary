<script lang="ts">
	// Reaper and storage (System → Provisioner): when finished runs give their
	// workspaces back, and how long uploads and snapshot content are kept.
	// One's own settings, one draft, one save bar.
	import { t } from '$lib/i18n/index.svelte';
	import SettingRow from '$lib/ui/SettingRow.svelte';
	import DurationInput from '$lib/ui/DurationInput.svelte';
	import { durWords } from '$lib/ui/duration';
	import SettingsFrame from './SettingsFrame.svelte';
</script>

<SettingsFrame>
	{#snippet children(s)}
		<section class="glass pane">
			<div class="pane-head">
				<h2>{t('page.reaper.head')}</h2>
				<span class="muted small">{t('page.reaper.headSub')}</span>
			</div>
			<SettingRow
				label={t('page.reaper.afterLabel')}
				forId="s-after"
				help={t('page.reaper.afterHelp')}
			>
				{#snippet note()}{t('page.reaper.currently', {
						dur: durWords(s.destroyCompletedAfter)
					})}{/snippet}
				<DurationInput
					id="s-after"
					label={t('page.reaper.afterLabel')}
					bind:value={s.destroyCompletedAfter}
				/>
			</SettingRow>
			<SettingRow
				label={t('page.reaper.abandonedLabel')}
				forId="s-abandoned"
				help={t('page.reaper.abandonedHelp')}
			>
				{#snippet note()}{t('page.reaper.abandonedNote')}{/snippet}
				<DurationInput
					id="s-abandoned"
					label={t('page.reaper.abandonedLabel')}
					bind:value={s.destroyAbandoned}
					allowNever
					disabled
				/>
			</SettingRow>
		</section>
		<section class="glass pane">
			<div class="pane-head">
				<h2>{t('page.storage.head')}</h2>
				<span class="muted small">{t('page.storage.headSub')}</span>
			</div>
			<SettingRow
				label={t('page.storage.orphansLabel')}
				forId="s-orphans"
				help={t('page.storage.orphansHelp')}
			>
				{#snippet note()}{t('page.reaper.currently', {
						dur: durWords(s.orphanUploadsAfter)
					})}{/snippet}
				<DurationInput
					id="s-orphans"
					label={t('page.storage.orphansLabel')}
					bind:value={s.orphanUploadsAfter}
				/>
			</SettingRow>
			<SettingRow
				label={t('page.storage.snapsLabel')}
				forId="s-snaps"
				help={t('page.storage.snapsHelp')}
			>
				{#snippet note()}{t('page.reaper.currently', { dur: durWords(s.snapshotsAfter) })}{/snippet}
				<DurationInput
					id="s-snaps"
					label={t('page.storage.snapsLabel')}
					bind:value={s.snapshotsAfter}
					allowNever
				/>
			</SettingRow>
		</section>
	{/snippet}
</SettingsFrame>
