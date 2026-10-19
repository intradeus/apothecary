<script lang="ts">
	// "updated 2s ago" caption for polled data (DESIGN 4.17), or "paused"
	// when the tab was hidden and nothing has landed since.
	import { onMount } from 'svelte';

	let { at, paused = false }: { at: number | null; paused?: boolean } = $props();

	let now = $state(Date.now());
	onMount(() => {
		const t = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(t);
	});
	const secs = $derived(at ? Math.max(0, Math.round((now - at) / 1000)) : null);
</script>

<span class="up" title={at ? new Date(at).toLocaleTimeString(undefined, { hour12: false }) : ''}>
	{#if paused}
		<span class="d paused"></span>paused
	{:else if secs === null}
		<span class="d pulse"></span>Connecting…
	{:else}
		<span class="d"></span>updated {secs < 2 ? 'just now' : `${secs}s ago`}
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
