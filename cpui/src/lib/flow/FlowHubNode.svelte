<script lang="ts">
	// The hub (FLOWLANG §1): the run between its chains. The main chain's end
	// comes back here — the run is open, waiting on what happens to its merge
	// requests — and every watcher and outcome branches from it, each on its
	// own signal. A route dropped on it ends its chain (`end`).
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import { I } from '$lib/ui/icons';

	let { data }: NodeProps = $props();
	const handle = '--xy-handle-background-color: var(--h)';
</script>

<div class="hub h-tea" title={(data?.title as string | undefined) ?? ''}>
	<Handle type="target" position={Position.Top} style={handle} />
	<span class="l"><span class="g">{I.loop}</span> {data?.label}</span>
	{#if data?.sub}<span class="sub">{data.sub}</span>{/if}
	<Handle type="source" position={Position.Bottom} style={handle} isConnectable={false} />
</div>

<style>
	.hub {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1px;
		padding: 6px 16px 7px;
		border-radius: var(--r-card);
		background: var(--cBg);
		backdrop-filter: var(--cBf);
		-webkit-backdrop-filter: var(--cBf);
		border: 1.5px solid color-mix(in oklch, var(--h) 38%, transparent);
		box-shadow: 0 0 var(--glow-md) color-mix(in oklch, var(--h) 12%, transparent);
		color: var(--h);
		white-space: nowrap;
	}
	.l {
		font-family: var(--display);
		font-weight: 600;
		font-size: var(--fs-title);
		line-height: 1.1;
	}
	.g {
		font-family: var(--sans);
		font-style: normal;
		font-size: var(--fs-badge);
		font-weight: 700;
	}
	.sub {
		font-size: var(--fs-badge);
		color: var(--mut);
	}
</style>
