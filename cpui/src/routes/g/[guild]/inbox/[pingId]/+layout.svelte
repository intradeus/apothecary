<script lang="ts">
	// One review workbench per ping.
	// SvelteKit reuses a route's components when only its params change, so
	// without this key a page would keep the previous pingId's state — and its
	// in-flight requests could land on the new one. A fresh instance per
	// pingId makes every load, draft and late response belong to one id.
	import { page } from '$app/state';

	let { children } = $props();
</script>

{#key page.params.pingId}
	{@render children()}
{/key}
