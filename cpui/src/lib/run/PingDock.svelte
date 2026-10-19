<script lang="ts">
	// The ping card docked above the composer while the run waits on you
	// (DESIGN 4.11). PingCard draws its own amber control glass; this only
	// docks it and says when earlier pings queue behind it.
	import PingCard from '$lib/PingCard.svelte';
	import type { RunPing } from './derive';

	let {
		ping,
		queued,
		busy,
		onanswer
	}: {
		ping: RunPing;
		/** Earlier pings of this run still waiting behind this one. */
		queued: number;
		busy: boolean;
		onanswer: (answer: string, note?: string, files?: string[]) => void;
	} = $props();
</script>

<div class="dock">
	{#if queued > 0}
		<div class="queue">Answer this first — {queued} earlier ping{queued > 1 ? 's' : ''} queued</div>
	{/if}
	{#key ping.id}
		<PingCard {ping} {busy} {onanswer} compact />
	{/key}
</div>

<style>
	.dock {
		flex: none;
		margin: 0 12px 12px;
		max-height: 60%;
		overflow: auto;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.queue {
		font-size: 11.5px;
		color: var(--amb);
		padding: 0 6px;
	}
</style>
