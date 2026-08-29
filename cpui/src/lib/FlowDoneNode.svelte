<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';

	let {}: NodeProps = $props();
</script>

<div class="done-node">
	<Handle type="target" position={Position.Left} />
	done ✓
</div>

<style>
	.done-node {
		background: var(--bg2, #1f2430);
		border: 1px dashed #4caf7d;
		border-radius: 16px;
		padding: 6px 14px;
		font-size: 12px;
		color: #4caf7d;
	}
</style>
