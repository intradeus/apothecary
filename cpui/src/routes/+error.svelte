<script lang="ts">
	import { page } from '$app/state';
	import { to } from '$lib/guild';
	import EmptyState from '$lib/ui/EmptyState.svelte';
</script>

<div class="page">
	<EmptyState
		title={page.status === 404 ? 'Not on the shelf' : 'Something spilled'}
		text={page.error?.message ?? 'An unexpected error occurred.'}
	>
		<a class="btn btn-primary" href={to.desk()}>Back to the desk</a>
	</EmptyState>
</div>
