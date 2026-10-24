<script lang="ts">
	// A control edge whose label sits by its SOURCE, not at its midpoint.
	// xyflow centres a label on the path, and a retry loop's middle lands on a
	// node while the edges leaving one stage all meet in the same spot. Here a
	// failure edge's label hangs under the handle it leaves from, one row per
	// edge of that stage (FlowGraph numbers them, autoLayout reserves the
	// rows), and a debug leaf's return label sits right of the leaf. Labels
	// are left-aligned: EdgeLabel centres its box, and the inner pill shifts
	// back by half its own width. A label is the easiest part of an edge to
	// hit, but Svelte Flow reports no click on it: data.pick opens what the
	// edge is.
	import { BaseEdge, EdgeLabel, getSmoothStepPath, type EdgeProps } from '@xyflow/svelte';
	import { LABEL_PAD, LABEL_ROW } from '$lib/flowdoc';

	let {
		id,
		interactionWidth,
		label,
		labelStyle,
		style,
		markerEnd,
		markerStart,
		sourcePosition,
		sourceX,
		sourceY,
		targetPosition,
		targetX,
		targetY,
		data
	}: EdgeProps = $props();

	const [path] = $derived(
		getSmoothStepPath({ sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition })
	);
	const anchor = $derived((data?.anchor as 'below' | 'right' | undefined) ?? 'below');
	const row = $derived(Number(data?.row ?? 0));
	const at = $derived(
		anchor === 'right'
			? { x: sourceX + 6, y: sourceY }
			: { x: sourceX + 6, y: sourceY + LABEL_PAD + row * LABEL_ROW + LABEL_ROW / 2 }
	);
</script>

<BaseEdge {id} {path} {markerStart} {markerEnd} {interactionWidth} {style} />
{#if label}
	<EdgeLabel x={at.x} y={at.y} style={labelStyle} class="e-lbl" transparent selectEdgeOnClick>
		<button
			type="button"
			class="e-lbl-pill"
			tabindex="-1"
			onclick={() => (data?.pick as (() => void) | undefined)?.()}>{label}</button
		>
	</EdgeLabel>
{/if}
