<!-- All-guilds desk: only reachable with more than one guild (see +page.ts). -->
<script lang="ts">
	import { loreLc } from '$lib/lore';
	import { to } from '$lib/guild';
	import EmptyState from '$lib/ui/EmptyState.svelte';
</script>

<div class="page">
	<EmptyState
		title={`Every ${loreLc('guild')} at once`}
		text={`The cross-${loreLc('guild')} desk arrives with multi-user support.`}
	>
		<a class="btn btn-primary" href={to.desk()}>Open your desk</a>
	</EmptyState>
</div>
