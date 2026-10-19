<script lang="ts">
	// Fit the viewport once the nodes have been measured. SvelteFlow's own
	// `fitView` prop fires before custom nodes report their size, which lands
	// a wide flow at max zoom on its first stage.
	//
	// A long flow fitted whole is unreadable, so below a legible zoom it
	// anchors on the entry (left edge) at that zoom instead, keeping the
	// fitted vertical centre.
	import { useNodesInitialized, useSvelteFlow } from '@xyflow/svelte';

	let {
		padding = 0.12,
		maxZoom = 1.1,
		legible = 0.55
	}: { padding?: number; maxZoom?: number; legible?: number } = $props();

	const initialized = useNodesInitialized();
	const flow = useSvelteFlow();
	let done = false;

	$effect(() => {
		if (done || !initialized.current) return;
		done = true;
		void (async () => {
			await flow.fitView({ padding, maxZoom });
			const vp = flow.getViewport();
			if (vp.zoom >= legible) return;
			const b = flow.getNodesBounds(flow.getNodes());
			const midY = b.y + b.height / 2;
			const screenMidY = vp.y + midY * vp.zoom;
			await flow.setViewport({
				x: 40 - b.x * legible,
				y: screenMidY - midY * legible,
				zoom: legible
			});
		})();
	});
</script>
