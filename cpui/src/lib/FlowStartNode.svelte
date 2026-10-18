<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';

	// A phased flow draws one marker per phase: the entry's says start, a
	// reaction's names its trigger.
	let { data }: NodeProps = $props();
	const label = $derived((data?.label as string | undefined) ?? 'start');
	const title = $derived(
		(data?.title as string | undefined) ??
			'The entry — drag this handle onto a stage to start the flow there'
	);
</script>

<div class="start-node" {title}>
	{label} ▶
	<Handle type="source" position={Position.Right} style="background:#7aa2f7" />
</div>

<style>
	.start-node {
		background: var(--bg2, #1f2430);
		border: 1px dashed #7aa2f7;
		border-radius: 16px;
		padding: 6px 14px;
		font-size: 12px;
		color: #7aa2f7;
	}
</style>
