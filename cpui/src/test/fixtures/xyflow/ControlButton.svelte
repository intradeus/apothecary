<script lang="ts">
	import type { Snippet } from 'svelte';

	let {
		children,
		onclick,
		title,
		'aria-label': label
	}: { children?: Snippet; onclick?: () => void; title?: string; 'aria-label'?: string } = $props();
</script>

<button {title} aria-label={label} {onclick}>{@render children?.()}</button>
