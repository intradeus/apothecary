<script lang="ts">
	// Limits (DESIGN 7.10): the deployment's concurrency and environment
	// caps. Read-only here — they protect the host, so they live in the
	// boot env (APOTHECARY_MAX_*), the same for every account.
	import { fleet } from '$lib/fleet.svelte';
	import SettingRow from '$lib/ui/SettingRow.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/ui/icons';

	const cap = (n: number | null | undefined) =>
		n === null ? t('page.limits.unlimited') : n === undefined ? '…' : String(n);
</script>

<section class="c pane">
	<div class="pane-head">
		<h2>{t('page.limits.head')}</h2>
		<span class="mut small">{t('page.limits.headSub')}</span>
	</div>
	<SettingRow label={t('page.limits.concLabel')} help={t('page.limits.concHelp')}>
		{#snippet note()}
			{t('page.limits.nowRunning', { count: fleet.slots.length })}{#if fleet.queued.length}
				· {t('page.limits.queued', { count: fleet.queued.length })}{/if}
		{/snippet}
		<span class="mono val">{cap(fleet.limits?.maxConcurrentRuns)}</span>
	</SettingRow>
	<SettingRow label={t('page.limits.envsLabel')} help={t('page.limits.envsHelp')}>
		{#snippet note()}{t('page.limits.envsNow', { count: fleet.envs.length })}{/snippet}
		<span class="mono val">{cap(fleet.limits?.maxEnvironments)}</span>
	</SettingRow>
</section>

<Callout hue="brand" glyph={I.info}>{t('page.limits.envNote')}</Callout>

<style>
	.val {
		font-size: var(--fs-lead);
	}
</style>
