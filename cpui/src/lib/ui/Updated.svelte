<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// "updated 2s ago" caption for polled data (DESIGN 4.17), or "paused"
	// when the tab was hidden and nothing has landed since.
	import { onMount } from 'svelte';

	let { at, paused = false }: { at: number | null; paused?: boolean } = $props();

	let now = $state(Date.now());
	onMount(() => {
		const h = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(h);
	});
	const secs = $derived(at ? Math.max(0, Math.round((now - at) / 1000)) : null);
</script>

<span class="up" title={at ? new Date(at).toLocaleTimeString(undefined, { hour12: false }) : ''}>
	{#if paused}
		<span class="d paused"></span>{t('ui.updated.paused')}
	{:else if secs === null}
		<span class="d pulse"></span>{t('ui.updated.connecting')}
	{:else}
		<span class="d"></span>{secs < 2 ? t('ui.updated.now') : t('ui.updated.ago', { secs })}
	{/if}
</span>

<style>
	.up {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: var(--fs-meta);
		color: var(--fnt);
		white-space: nowrap;
	}
	.d {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--az);
	}
	.d.paused {
		background: transparent;
		border: 1px solid var(--fnt);
	}
</style>
