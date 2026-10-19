<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';

	let {}: NodeProps = $props();
</script>

<div class="term" title="The run stops deliberately">
	<Handle type="target" position={Position.Left} class="h-abort" />
	<span class="l">abort</span><span class="g">✕</span>
</div>

<style>
	.term {
		display: inline-flex;
		align-items: center;
		gap: 7px;
		padding: 4px 14px 5px;
		border-radius: 999px;
		background: var(--cBg);
		border: 1.5px dashed var(--rosLn);
		color: var(--ros);
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
	.term :global(.h-abort) {
		background: var(--ros);
	}
</style>
