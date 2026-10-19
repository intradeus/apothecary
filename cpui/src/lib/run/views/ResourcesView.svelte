<script lang="ts">
	// Resources (DESIGN §6): process count, the sample window and the three
	// detailed graphs with a shared crosshair. The engine samples the whole
	// sandbox (~10s); there is no per-process breakdown to show yet.
	import ResourceGraphs from '$lib/ResourceGraphs.svelte';
	import type { RunStore } from '../store.svelte';

	let { rs, visible }: { rs: RunStore; visible: boolean } = $props();

	$effect(() => {
		if (!visible) return;
		return rs.useResources();
	});
</script>

<div class="rv">
	<ResourceGraphs samples={rs.samples} />
</div>

<style>
	.rv {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding-top: 8px;
	}
</style>
