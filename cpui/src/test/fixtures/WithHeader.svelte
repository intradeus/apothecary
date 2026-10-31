<script lang="ts">
	// A page under a shared header, for a tab that puts its main action there
	// through HeaderAction.
	import type { Component } from 'svelte';
	import { provideHeaderSlot } from '$lib/ui/headerAction.svelte';

	let { page, props }: { page: unknown; props: Record<string, unknown> } = $props();

	const Page = $derived(page as Component<Record<string, unknown>>);
	const slot = provideHeaderSlot();
</script>

<header>{@render slot.action?.()}</header>
<Page {...props} />
