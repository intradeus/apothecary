<script lang="ts">
	// A workshop by name: its vial and the name, ellipsised; a short id while
	// projects load. With `link` it opens the workshop (above a row's
	// stretched link).
	import { fleet } from '$lib/app/fleet.svelte';
	import { to } from '$lib/core/urls';
	import Vial from '$lib/ui/Vial.svelte';

	let {
		projectId,
		link = false,
		idle = false
	}: { projectId: string | undefined; link?: boolean; idle?: boolean } = $props();
</script>

{#if projectId}
	{#if link}
		<a class="wn over-stretch" href={to.workshop(projectId)}
			><Vial {projectId} {idle} /><span class="truncate">{fleet.projectName(projectId)}</span></a
		>
	{:else}
		<span class="wn"
			><Vial {projectId} {idle} /><span class="truncate">{fleet.projectName(projectId)}</span></span
		>
	{/if}
{:else}
	<span class="faint">—</span>
{/if}

<style>
	.wn {
		display: inline-flex;
		align-items: center;
		gap: 7px;
		min-width: 0;
		max-width: 100%;
		font-size: var(--fs-label);
		color: inherit;
	}
	a.wn:hover {
		color: var(--text);
		text-decoration: none;
	}
</style>
