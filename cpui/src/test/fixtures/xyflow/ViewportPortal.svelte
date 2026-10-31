<script lang="ts">
	import type { Snippet } from 'svelte';

	let { target, children }: { target: string; children?: Snippet } = $props();
</script>

<div data-testid="portal-{target}">{@render children?.()}</div>
