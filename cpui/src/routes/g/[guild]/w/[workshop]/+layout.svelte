<script lang="ts">
	// One page instance per workshop (overview, settings, flows).
	// SvelteKit reuses a route's components when only its params change, so
	// without this key a page would keep the previous workshop's state — and its
	// in-flight requests could land on the new one. A fresh instance per
	// workshop makes every load, draft and late response belong to one id.
	import { page } from '$app/state';

	let { children } = $props();
</script>

{#key page.params.workshop}
	{@render children()}
{/key}
