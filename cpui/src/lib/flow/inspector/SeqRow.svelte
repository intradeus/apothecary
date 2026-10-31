<script lang="ts">
	// One stage of a sequence, as it will run: read-only on a sequence stage,
	// a way into the stage (`onclick`) on the sequence's own panel.
	import StageChip from '$lib/ui/StageChip.svelte';
	import type { DocStage } from '$lib/domain/flowdoc';

	let {
		stage,
		label,
		onclick
	}: {
		stage: DocStage;
		label: string;
		onclick?: () => void;
	} = $props();
</script>

{#snippet row()}
	<StageChip type={stage.type} />
	<span class="mono">{label}</span>
	{#if stage.sequence}<span class="muted small">→ {stage.sequence}</span>{/if}
{/snippet}

{#if onclick}
	<button class="rowbtn seq-row" {onclick}>{@render row()}</button>
{:else}
	<div class="seq-row">{@render row()}</div>
{/if}

<style>
	/* A sequence stage's sequence, stage by stage, as it will run. */
	.seq-row {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
		padding: 2px 0;
	}
	.seq-row.rowbtn {
		padding: 4px 6px;
	}
</style>
