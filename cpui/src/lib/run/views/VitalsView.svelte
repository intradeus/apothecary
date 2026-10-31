<script lang="ts">
	// Vitals (DESIGN §6): the workspace's CPU, memory and network — process
	// count, the sample window and three detailed graphs with a shared
	// crosshair. The engine samples the whole sandbox (~10s); there is no
	// per-process breakdown to show yet.
	import ResourceGraphs from '$lib/run/ResourceGraphs.svelte';
	import type { RunStore } from '../store.svelte';

	let { rs, visible }: { rs: RunStore; visible: boolean } = $props();

	$effect(() => {
		if (!visible) return;
		return rs.useResources();
	});
</script>

<div class="view rv">
	<ResourceGraphs samples={rs.samples} />
</div>

<style>
	.rv {
		padding-top: 8px;
	}
</style>
