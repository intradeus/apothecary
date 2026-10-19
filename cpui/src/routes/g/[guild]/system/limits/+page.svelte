<script lang="ts">
	// Limits (DESIGN 7.10): concurrency and environment caps.
	import { fleet } from '$lib/fleet.svelte';
	import SettingsFrame from '$lib/system/SettingsFrame.svelte';
	import SettingRow from '$lib/ui/SettingRow.svelte';
	import Stepper from '$lib/ui/Stepper.svelte';
	import { t } from '$lib/i18n/index.svelte';
</script>

<SettingsFrame>
	{#snippet children(s)}
		<section class="c pane">
			<div class="pane-head">
				<h2>{t('page.limits.head')}</h2>
				<span class="mut small">{t('page.limits.headSub')}</span>
			</div>
			<SettingRow
				label={t('page.limits.concLabel')}
				forId="s-conc"
				help={t('page.limits.concHelp')}
			>
				{#snippet note()}
					{t('page.limits.nowRunning', { count: fleet.slots.length })}{#if fleet.queued.length}
						· {t('page.limits.queued', { count: fleet.queued.length })}{/if}
				{/snippet}
				<Stepper id="s-conc" label={t('page.limits.concLabel')} bind:value={s.maxConcurrentRuns} />
			</SettingRow>
			<SettingRow
				label={t('page.limits.envsLabel')}
				forId="s-envs"
				help={t('page.limits.envsHelp')}
			>
				{#snippet note()}{t('page.limits.envsNow', { count: fleet.envs.length })}{/snippet}
				<Stepper id="s-envs" label={t('page.limits.envsLabel')} bind:value={s.maxEnvironments} />
			</SettingRow>
		</section>
	{/snippet}
</SettingsFrame>
