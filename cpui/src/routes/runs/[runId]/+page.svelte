<script lang="ts">
	// The live run (DESIGN 7.1). Everything lives in RunPage; this keys it on
	// the run id (a new run from this one navigates here) and on a retry,
	// which re-subscribes the same run from the top.
	import { page } from '$app/state';
	import RunPage from '$lib/run/RunPage.svelte';

	const runId = $derived(page.params.runId ?? '');
	let gen = $state(0);
</script>

{#key `${runId}:${gen}`}
	<RunPage {runId} onretried={() => gen++} />
{/key}
