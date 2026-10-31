<script lang="ts">
	// The ping card docked above the composer while the run waits on you
	// (DESIGN 4.11). PingCard draws its own solid sky; this only
	// docks it and says when earlier pings queue behind it.
	import PingCard from '$lib/pings/PingCard.svelte';
	import type { RunPing } from './derive';
	import type { FormContent } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';

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
		onanswer: (answer: string, note?: string, files?: string[], content?: FormContent) => void;
	} = $props();
</script>

<div class="dock">
	{#if queued > 0}
		<div class="queue">{t('run.pingDock.queued', { count: queued })}</div>
	{/if}
	{#key ping.id}
		<PingCard {ping} {busy} {onanswer} compact />
	{/key}
</div>

<style>
	.dock {
		flex: none;
		margin: 0 calc(12px + var(--fab-clear, 0px)) 12px 12px;
		max-height: 60%;
		overflow: auto;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.queue {
		font-size: var(--fs-help);
		color: var(--attention-text);
		padding: 0 6px;
	}
</style>
