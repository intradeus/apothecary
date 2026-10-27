<script lang="ts">
	// One ticket page per ticket (its run form drafts included).
	// SvelteKit reuses a route's components when only its params change, so
	// without this key a page would keep the previous ticketId's state — and its
	// in-flight requests could land on the new one. A fresh instance per
	// ticketId makes every load, draft and late response belong to one id.
	import { page } from '$app/state';

	let { children } = $props();
</script>

{#key page.params.ticketId}
	{@render children()}
{/key}
