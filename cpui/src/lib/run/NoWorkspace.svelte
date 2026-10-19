<script lang="ts">
	// What a workspace-only view shows when there is no live workspace behind
	// it (DESIGN 7.1): the Record keeps every snapshot.
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import { ago } from '$lib/format';
	import type { RunStore } from './store.svelte';

	let { rs, onrecord }: { rs: RunStore; onrecord: () => void } = $props();

	const title = $derived(
		rs.destroyedAt
			? `Workspace destroyed ${ago(rs.destroyedAt)}`
			: rs.run?.state === 'archived'
				? 'Workspace archived away'
				: rs.run && !rs.run.envId && !rs.terminal
					? 'No workspace yet'
					: 'No live workspace'
	);
	const text = $derived(
		rs.run && !rs.run.envId && !rs.terminal
			? 'It appears here once the run has provisioned one.'
			: 'The Record keeps every snapshot and log this run captured.'
	);
</script>

<div class="nw">
	<EmptyState {title} {text} compact>
		<button onclick={onrecord}>Open the Record</button>
	</EmptyState>
</div>

<style>
	.nw {
		flex: 1;
		min-height: 0;
		padding: 12px;
		display: flex;
		flex-direction: column;
		justify-content: center;
		overflow: auto;
	}
</style>
