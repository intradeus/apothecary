<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';

	let {}: NodeProps = $props();
</script>

<div class="term" title="The run completes">
	<Handle type="target" position={Position.Left} class="h-done" />
	<span class="l">done</span><span class="g">✓</span>
</div>

<style>
	.term {
		display: inline-flex;
		align-items: center;
		gap: 7px;
		padding: 4px 14px 5px;
		border-radius: 999px;
		background: var(--cBg);
		border: 1.5px dashed var(--grnLn);
		color: var(--grn);
		white-space: nowrap;
	}
	.l {
		font-family: var(--serif);
		font-style: italic;
		font-size: 16px;
		line-height: 1.1;
	}
	.g {
		font-size: 11px;
		font-weight: 700;
	}
	.term :global(.h-done) {
		background: var(--grn);
	}
</style>
