<script lang="ts">
	// A page's main action, from a page below a shared header (DESIGN 7): what it
	// wraps renders in that header while the page is open.
	//
	//   <HeaderAction><a class="btn btn-primary btn-lg" href={…}>…</a></HeaderAction>
	import type { Snippet } from 'svelte';
	import { headerSlot } from './headerSlot.svelte';

	let { children }: { children: Snippet } = $props();

	const slot = headerSlot();

	$effect(() => {
		slot.action = children;
		return () => {
			// The next page may already have put its own in.
			if (slot.action === children) slot.action = undefined;
		};
	});
</script>
