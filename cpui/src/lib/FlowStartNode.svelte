<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';

	// A phased flow draws one marker per phase: the entry's says start, a
	// reaction's names its trigger. Serif italic pill (DESIGN 7.8).
	let { data }: NodeProps = $props();
	const label = $derived((data?.label as string | undefined) ?? 'start');
	const trigger = $derived(!!data?.label);
	const title = $derived(
		(data?.title as string | undefined) ??
			'The entry — drag this handle onto a stage to start the flow there'
	);
</script>

<div class="term start" class:trigger {title}>
	<span class="l">{label}</span><span class="g">▶︎</span>
	<Handle type="source" position={Position.Right} class="h-start" />
</div>

<style>
	.term {
		display: inline-flex;
		align-items: center;
		gap: 7px;
		padding: 4px 14px 5px;
		border-radius: 999px;
		background: var(--cBg);
		backdrop-filter: var(--cBf);
		-webkit-backdrop-filter: var(--cBf);
		border: 1px solid var(--brandLn);
		box-shadow: 0 0 16px var(--brandGlow);
		color: var(--brand);
		white-space: nowrap;
	}
	.l {
		font-family: var(--serif);
		font-style: italic;
		font-size: 16px;
		line-height: 1.1;
	}
	.g {
		font-size: 9px;
	}
	.trigger {
		border: 1px dashed var(--vioLn);
		color: var(--vio);
		box-shadow: none;
	}
	.trigger .l {
		font-size: 14px;
	}
	.term :global(.h-start) {
		background: var(--brand);
	}
	.trigger :global(.h-start) {
		background: var(--vio);
	}
</style>
