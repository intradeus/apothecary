<script lang="ts">
	import type { Snippet } from 'svelte';

	let { after }: { after?: Snippet } = $props();
</script>

{@render after?.()}
