<script lang="ts">
	// A section of the canvas (FLOWLANG §1): the faint label heading the
	// marker column where the main chain, the watchers or the outcomes begin.
	// Nothing anyone acts on — no handles, never selected or dragged.
	import type { NodeProps } from '@xyflow/svelte';

	let { data }: NodeProps = $props();
</script>

<div class="sec" title={(data?.title as string | undefined) ?? ''}>{data?.label}</div>

<style>
	.sec {
		font-size: var(--fs-badge);
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--text-faint);
		white-space: nowrap;
		cursor: default;
	}
</style>
