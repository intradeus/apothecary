<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// "updated 2s ago" caption for polled data (DESIGN 4.17), or "paused"
	// when the tab was hidden and nothing has landed since.
	import { onMount } from 'svelte';
	import StableText from './StableText.svelte';

	let { at, paused = false }: { at: number | null; paused?: boolean } = $props();

	let now = $state(Date.now());
	onMount(() => {
		const h = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(h);
	});
	const secs = $derived(at ? Math.max(0, Math.round((now - at) / 1000)) : null);
	// Polls land every few seconds, so the caption cycles "just now" → "3s ago"
	// → "just now"; reserve the widest wording so the header doesn't twitch.
	const reserve = $derived([
		t('ui.updated.now'),
		t('ui.updated.ago', { secs: 88 }),
		t('ui.updated.paused'),
		t('ui.updated.connecting')
	]);
</script>

<span class="up" title={at ? new Date(at).toLocaleTimeString(undefined, { hour12: false }) : ''}>
	{#if paused}
		<span class="d paused"></span><StableText text={t('ui.updated.paused')} {reserve} />
	{:else if secs === null}
		<span class="d pulse"></span><StableText text={t('ui.updated.connecting')} {reserve} />
	{:else}
		<span class="d"></span><StableText
			text={secs < 2 ? t('ui.updated.now') : t('ui.updated.ago', { secs })}
			{reserve}
		/>
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
