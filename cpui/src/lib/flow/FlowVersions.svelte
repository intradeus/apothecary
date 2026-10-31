<script lang="ts">
	// The Versions panel: every hash this flow has been saved under, each one
	// loadable into the editor. Sits in the page's side column.
	import { ago, fmtAbs } from '$lib/core/format';
	import { I } from '$lib/design/icons';
	import type { Flow, FlowVersion } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';

	let {
		flow,
		versions,
		onload,
		onclose
	}: {
		flow: Flow;
		versions: FlowVersion[];
		onload: (v: FlowVersion) => void;
		onclose: () => void;
	} = $props();
</script>

<div class="sh">
	<span class="display st">{t('page.flow.versions')}</span>
	<span class="spacer"></span>
	<button class="btn-icon" aria-label={t('page.flow.closeVersions')} onclick={onclose}
		>{I.close}</button
	>
</div>
<p class="help sp">
	{t('page.flow.versionsHelp')}
</p>
<div class="vers">
	{#each versions as v (v.hash)}
		<div class="vr" class:cur={v.hash === flow.hash}>
			<span class="mono">@{v.hash.slice(0, 10)}</span>
			{#if v.hash === flow.hash}<span class="pill sm tint-success">{t('page.flow.current')}</span
				>{/if}
			<span class="spacer"></span>
			<span class="faint tiny" title={fmtAbs(v.createdAt)}>{ago(v.createdAt)}</span>
			{#if v.hash !== flow.hash}
				<button class="btn-sm" onclick={() => onload(v)}>{t('page.flow.loadIntoEditor')}</button>
			{/if}
		</div>
	{:else}
		<div class="faint small">
			{flow.builtin ? t('page.flow.noVersionsBuiltin') : t('page.flow.noVersions')}
		</div>
	{/each}
</div>

<style>
	.sh {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 12px 14px 8px;
	}
	.st {
		font-size: var(--fs-section);
	}
	.sp {
		margin: 0;
		padding: 0 14px 10px;
	}
	.vers {
		display: flex;
		flex-direction: column;
		padding: 0 8px 10px;
	}
	.vr {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 7px 8px;
		border-top: 1px solid var(--line);
		font-size: var(--fs-label);
	}
	.vr.cur {
		background: var(--fill-2);
		border-radius: var(--r-row);
	}
</style>
