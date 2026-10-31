<script lang="ts">
	// The flow editor (DESIGN 7.8). Everything lives in FlowEditorPage; this
	// names the flow from the URL (the layout keys it on the name).
	import { page } from '$app/state';
	import FlowEditorPage from '$lib/flow/FlowEditorPage.svelte';
</script>

<FlowEditorPage projectId={page.params.workshop!} name={page.params.name ?? ''} />
