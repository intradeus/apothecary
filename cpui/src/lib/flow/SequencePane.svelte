<script lang="ts">
	// The sequence open beside the flow: where it sits among the sequences
	// opened, and its chain drawn like a flow. The page holds its half.
	import { I } from '$lib/design/icons';
	import type { ActionContract, FlowDoc } from '$lib/domain/flowdoc';
	import FlowGraph from '$lib/flow/FlowGraph.svelte';
	import type { FlowSelection } from '$lib/flow/flowSelection.svelte';
	import type { Level } from '$lib/flow/graph/sequence';
	import { t } from '$lib/i18n/index.svelte';

	let {
		panels,
		open,
		doc,
		actions
	}: {
		panels: FlowSelection;
		open: Level;
		doc: FlowDoc;
		actions: ActionContract[];
	} = $props();
</script>

<header class="bh">
	<nav class="crumbs mono">
		{#each panels.levels as l, d (d)}
			{#if d > 0}<span class="faint">›</span>{/if}
			{#if d < panels.levels.length - 1}
				<button
					class="rowbtn"
					onclick={() => panels.select({ kind: 'sequence', id: l.sequence.id })}
					>{l.sequence.id}</button
				>
			{:else}
				<span class="cur">{l.sequence.id}</span>
			{/if}
		{/each}
	</nav>
	<span class="faint small truncate grow"
		>{open.sequence.description ??
			(panels.levels[0].stage
				? t('page.flow.sequenceRunsAs', { stage: panels.levels[0].stage.id })
				: '')}</span
	>
	<button
		class="btn-icon"
		title={t('page.flow.sequenceClose')}
		aria-label={t('page.flow.sequenceClose')}
		onclick={() => panels.select(null)}>{I.close}</button
	>
</header>
{#key panels.paneKey}
	<FlowGraph
		{doc}
		layout={panels.sequenceLayout}
		rev={panels.sequenceRev}
		sequence={open.view}
		readonly
		legible={0.2}
		onselect={panels.onSequenceSelect}
		onmove={() => {}}
		onlink={() => {}}
		onmeasured={panels.onSequenceMeasured}
		agents={doc.agents ?? []}
		{actions}
	/>
{/key}

<style>
	.bh {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 6px 8px 6px 12px;
		border-bottom: 1px solid var(--line);
		background: var(--glass-bg);
	}
	.crumbs {
		display: flex;
		align-items: center;
		gap: 4px;
		font-size: var(--fs-label);
	}
	.crumbs .cur {
		color: var(--text);
		font-weight: 600;
	}
</style>
