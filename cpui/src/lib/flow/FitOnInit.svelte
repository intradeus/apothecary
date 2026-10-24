<script lang="ts">
	// Fit the viewport once the nodes have been measured. SvelteFlow's own
	// `fitView` prop fires before custom nodes report their size, which lands
	// a wide flow at max zoom on its first stage.
	//
	// A long flow fitted whole is unreadable, so below a legible zoom it
	// anchors on the entry (left edge) at that zoom instead — keeping the
	// fitted vertical centre while the flow still fits in height, and at the
	// top (where the entry is) once its phases' rows run taller than that. Each change of `refit` fits again, animated —
	// the canvas changed size (a pane opened or closed beside it).
	import { useNodesInitialized, useStore, useSvelteFlow } from '@xyflow/svelte';
	import { tick, untrack } from 'svelte';
	import { ms } from '$lib/ui/motion';

	let {
		padding = 0.12,
		maxZoom = 1.1,
		legible = 0.55,
		refit = 0,
		onmeasured
	}: {
		padding?: number;
		maxZoom?: number;
		legible?: number;
		refit?: number;
		/** Called once the nodes have sizes, BEFORE the fit — a layout guessed
		 *  without them can be redone first, and the fit sees the result. */
		onmeasured?: () => void;
	} = $props();

	const initialized = useNodesInitialized();
	const flow = useSvelteFlow();
	const store = useStore();
	let done = false;

	async function fit(duration?: number) {
		await flow.fitView({ padding, maxZoom, duration });
		const vp = flow.getViewport();
		if (vp.zoom >= legible) return;
		const b = flow.getNodesBounds(flow.getNodes());
		const midY = b.y + b.height / 2;
		const y =
			b.height * legible + 80 > store.height
				? 40 - b.y * legible
				: vp.y + midY * vp.zoom - midY * legible;
		await flow.setViewport({ x: 40 - b.x * legible, y, zoom: legible }, { duration });
	}

	$effect(() => {
		if (done || !initialized.current) return;
		done = true;
		void (async () => {
			onmeasured?.();
			await tick();
			await fit();
		})();
	});

	// The canvas resizes on the frame after the pane beside it changes; the
	// flow reads its new size on the next one. Fit after both.
	let fitted = untrack(() => refit);
	$effect(() => {
		if (refit === fitted) return;
		fitted = refit;
		requestAnimationFrame(() => requestAnimationFrame(() => void fit(ms('--dur-spring', 450))));
	});
</script>
