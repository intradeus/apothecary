<script lang="ts">
	// One ticket page per ticket of a workshop (its run form drafts included).
	// SvelteKit reuses a route's components when only its params change, so
	// without these keys a page would keep the previous ticket's state — and
	// its in-flight requests could land on the new one. A ticket's key is not
	// enough: workshops sharing a source show the same key, and the page reads,
	// writes and starts runs through its workshop. A fresh instance per
	// workshop and key makes every load, draft and late response belong to one.
	import { page } from '$app/state';

	let { children } = $props();
</script>

{#key page.params.workshop}
	{#key page.params.key}
		{@render children()}
	{/key}
{/key}
