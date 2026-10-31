<script lang="ts">
	import type { Snippet } from 'svelte';

	let { x, y, children }: { x: number; y: number; children?: Snippet } = $props();
</script>

<div data-testid="edge-label" data-x={x} data-y={y}>{@render children?.()}</div>
