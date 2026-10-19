<script lang="ts">
	// Storage (DESIGN 7.10): upload and snapshot retention.
	import { t } from '$lib/i18n/index.svelte';
	import SettingsFrame from '$lib/system/SettingsFrame.svelte';
	import SettingRow from '$lib/ui/SettingRow.svelte';
	import DurationInput from '$lib/ui/DurationInput.svelte';
	import { durWords } from '$lib/ui/duration';
</script>

<SettingsFrame>
	{#snippet children(s)}
		<section class="c pane">
			<div class="pane-head">
				<h2>{t('page.storage.head')}</h2>
				<span class="mut small">{t('page.storage.headSub')}</span>
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
