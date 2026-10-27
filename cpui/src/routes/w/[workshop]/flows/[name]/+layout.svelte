<script lang="ts">
	// One flow editor per flow: never save one flow's draft under another.
	// SvelteKit reuses a route's components when only its params change, so
	// without this key a page would keep the previous name's state — and its
	// in-flight requests could land on the new one. A fresh instance per
	// name makes every load, draft and late response belong to one id.
	import { page } from '$app/state';

	let { children } = $props();
</script>

{#key page.params.name}
	{@render children()}
{/key}
