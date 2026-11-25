<script lang="ts">
	// A page that offers itself to the assistant while it is on screen, as a
	// route's page does (offerPage).
	import { offerPage, type PageAssist } from '$lib/assistant/assistant.svelte';

	let { target }: { target: PageAssist } = $props();

	offerPage(() => target);
</script>
