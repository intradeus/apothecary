<!-- A merge request, where a run is started on it. ?repo= names a
     repository other than the primary. -->
<script lang="ts">
	import { page } from '$app/state';
	import MergeRequestPage from '$lib/mrs/MergeRequestPage.svelte';

	const projectId = $derived(page.params.project ?? '');
	const iid = $derived(Number(page.params.iid) || 0);
	const repo = $derived(page.url.searchParams.get('repo') ?? '');
</script>

{#key `${projectId}/${iid}/${repo}`}
	<MergeRequestPage {projectId} {iid} {repo} />
{/key}
