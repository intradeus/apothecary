<script lang="ts">
	// One dashboard: its panels over the chosen range and filters.
	import { page } from '$app/state';
	import Dashboard from '$lib/analytics/Dashboard.svelte';
</script>

{#key page.params.id}
	<Dashboard id={page.params.id ?? ''} />
{/key}
