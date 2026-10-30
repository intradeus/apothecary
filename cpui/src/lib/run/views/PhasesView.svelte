<script lang="ts">
	// The flow's watchers as a window of their own, beside its main chain and
	// its outcomes: what opens each one, how often it ran, which one is live
	// or parked, what holds right now, and a way to start one by hand between
	// chains. Still there once the run is over — what ran, without
	// the buttons.
	import type { RunStore } from '../store.svelte';
	import PhasesCard from '../PhasesCard.svelte';

	let { rs }: { rs: RunStore } = $props();
	const run = $derived(rs.run);
</script>

<div class="pv">
	{#if run}
		{#key `${run.idle}|${run.phase ?? ''}|${rs.terminal}`}<PhasesCard
				runId={rs.runId}
				idle={run.idle && !rs.terminal}
				ended={rs.terminal}
			/>{/key}
	{/if}
</div>

<style>
	.pv {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: 12px 14px;
	}
</style>
