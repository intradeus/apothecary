<!-- New ticket (DESIGN 7.6): a two-column form — the ticket on the left,
     "Import from tracker" on the right. ?project= preselects the workshop. -->
<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { currentGuild, to } from '$lib/guild';
	import { fleet } from '$lib/fleet.svelte';
	import { isLore } from '$lib/lore';
	import TicketForm from '$lib/TicketForm.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';

	const initialProject = $derived(page.url.searchParams.get('project') ?? '');
</script>

<svelte:head><title>New ticket</title></svelte:head>

<PageHeader
	crumbs={[
		{ label: currentGuild().name, href: to.desk() },
		{ label: 'Tickets', href: to.tickets() },
		{ label: 'New' }
	]}
	title="New"
	accent="ticket"
>
	{#snippet meta()}
		<span
			>{isLore()
				? 'The ingredients: what to build and what done looks like.'
				: 'What to build and what done looks like.'} Runs start from it.</span
		>
	{/snippet}
</PageHeader>

<div class="page body">
	{#if !fleet.updatedAt && fleet.error}
		<ErrorCard
			cause="Could not load workshops"
			detail={fleet.error}
			onretry={() => fleet.refresh()}
		/>
	{:else if !fleet.updatedAt}
		<div class="c sk"><Skeleton lines={8} /></div>
	{:else}
		<TicketForm
			projects={fleet.projects}
			{initialProject}
			onsaved={(t) => goto(to.ticket(t.id))}
			oncancel={() => goto(to.tickets(initialProject ? { project: initialProject } : undefined))}
		/>
	{/if}
</div>

<style>
	.body {
		padding-top: 2px;
	}
	.body > :global(*) {
		max-width: 1180px;
		width: 100%;
	}
	.sk {
		border-radius: 16px;
		padding: 16px;
	}
</style>
