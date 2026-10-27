<script lang="ts">
	// Reaper (DESIGN 7.10): when finished runs give their workspaces back.
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
				<h2>{t('page.reaper.head')}</h2>
				<span class="mut small">{t('page.reaper.headSub')}</span>
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
	{/snippet}
</SettingsFrame>
