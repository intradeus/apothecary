<script lang="ts">
	// The hub (FLOWLANG §1): the run between its chains. The main chain's end
	// comes back here — the run is open, waiting on what happens to its merge
	// requests — and every watcher and outcome branches from it, each on its
	// own signal. A route dropped on it ends its chain (`end`).
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import { I } from '$lib/design/icons';

	let { data }: NodeProps = $props();
	const handle = '--xy-handle-background-color: var(--tone)';
</script>

<div class="hub tone-accent-2" title={(data?.title as string | undefined) ?? ''}>
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
		background: var(--glass-bg);
		backdrop-filter: var(--glass-filter);
		-webkit-backdrop-filter: var(--glass-filter);
		border: 1.5px solid color-mix(in oklch, var(--tone) 38%, transparent);
		box-shadow: 0 0 var(--glow-md) color-mix(in oklch, var(--tone) 12%, transparent);
		color: var(--tone);
		white-space: nowrap;
	}
	.l {
		font-family: var(--font-display);
		font-weight: 600;
		font-size: var(--fs-title);
		line-height: 1.1;
	}
	.g {
		font-family: var(--font-body);
		font-style: normal;
		font-size: var(--fs-badge);
		font-weight: 700;
	}
	.sub {
		font-size: var(--fs-badge);
		color: var(--text-muted);
	}
</style>
