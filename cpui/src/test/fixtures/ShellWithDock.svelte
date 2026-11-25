<script lang="ts">
	// The assistant's dock as the shell holds it: the page first, the corner
	// after it. The page goes when `target` does, the way a route's page goes
	// on a navigation: its offer is withdrawn while the shell updates, in the
	// same pass as the dock's own effects, not from outside it.
	import AssistantDock from '$lib/assistant/AssistantDock.svelte';
	import type { PageAssist } from '$lib/assistant/assistant.svelte';
	import OfferedPage from './OfferedPage.svelte';

	let { target }: { target: PageAssist | null } = $props();
</script>

{#if target}<OfferedPage {target} />{/if}
<AssistantDock />
