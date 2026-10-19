<!-- Tickets (DESIGN 7.6): units of work, each owning many runs. The status
     column is DERIVED — the latest run's state, or "no runs". -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { currentGuild, to } from '$lib/guild';
	import { api } from '$lib/api';
	import { fleet } from '$lib/fleet.svelte';
	import { plural, stateTab } from '$lib/format';
	import type { Ticket } from '$lib/domain';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import SearchInput from '$lib/ui/SearchInput.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import { I } from '$lib/ui/icons';
	import TicketTable from '$lib/tickets/TicketTable.svelte';
	import WorkshopFilter from '$lib/shell/WorkshopFilter.svelte';

	let tickets = $state<Ticket[]>([]);
	let loaded = $state(false);
	let error = $state('');

	let q = $state('');
	let source = $state(''); // '' all · 'manual' · a tracker kind
	let activeOnly = $state(false);
	const projectFilter = $derived(page.url.searchParams.get('project') ?? '');

	async function load() {
		error = '';
		try {
			tickets = await api.tickets.list();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loaded = true;
		}
	}
	onMount(load);

	function setProject(id: string) {
		void goto(to.tickets(id ? { project: id } : undefined), {
			replaceState: true,
			keepFocus: true,
			noScroll: true
		});
	}

	const hasActive = (t: Ticket) =>
		(fleet.runsByTicket.get(t.id) ?? []).some((r) => stateTab[r.state] === 'active');

	const kinds = $derived([
		...new Set(tickets.map((t) => t.source?.kind).filter(Boolean))
	] as string[]);

	const shown = $derived.by(() => {
		const s = q.trim().toLowerCase();
		return tickets
			.filter((t) => !projectFilter || t.projectId === projectFilter)
			.filter((t) => (!source ? true : source === 'manual' ? !t.source : t.source?.kind === source))
			.filter((t) => !activeOnly || hasActive(t))
			.filter(
				(t) =>
					!s ||
					[t.title, t.source?.key, t.goal, t.baseBranch]
						.filter(Boolean)
						.some((v) => v!.toLowerCase().includes(s))
			)
			.sort((a, b) => b.createdAt.localeCompare(a.createdAt));
	});
</script>

<svelte:head><title>Tickets</title></svelte:head>

<PageHeader
	crumbs={[
		{ label: currentGuild().name, href: to.desk() },
		...(projectFilter
			? [{ label: fleet.projectName(projectFilter), href: to.workshop(projectFilter) }]
			: []),
		{ label: 'Tickets' }
	]}
	title="Tickets"
	accent="for the bench"
>
	{#snippet meta()}
		<span>{plural(tickets.length, 'ticket')}</span>
		<span>runs start from a ticket</span>
	{/snippet}
	{#snippet actions()}
		<a
			class="btn btn-primary btn-lg"
			href={to.newTicket(projectFilter ? { project: projectFilter } : undefined)}
			>{I.add} New ticket</a
		>
	{/snippet}
</PageHeader>

<div class="page body">
	<div class="filters">
		<SearchInput bind:value={q} placeholder="Search title, key, goal…" />
		<WorkshopFilter value={projectFilter} onchange={setProject} />
		<select class="sel" aria-label="Filter by source" bind:value={source}>
			<option value="">Any source</option>
			<option value="manual">Written here</option>
			{#each kinds as k (k)}<option value={k}>Imported · {k}</option>{/each}
		</select>
		<Toggle bind:checked={activeOnly} label="Has an active run" />
	</div>

	{#if error && !tickets.length}
		<ErrorCard cause="Could not load tickets" detail={error} onretry={load} />
	{:else if !loaded}
		<Skeleton pane lines={6} />
	{:else if tickets.length === 0}
		<EmptyState
			title="No tickets yet"
			text="A ticket is a unit of work. Write one, then start runs against it."
		>
			<a class="btn btn-primary" href={to.newTicket()}>{I.add} New ticket</a>
		</EmptyState>
	{:else if shown.length === 0}
		<EmptyState compact title="No tickets match" text="Loosen the search or the filters.">
			<button
				onclick={() => {
					q = '';
					source = '';
					activeOnly = false;
					setProject('');
				}}>Clear filters</button
			>
		</EmptyState>
	{:else}
		<div class="c pane">
			<TicketTable
				tickets={shown}
				cols={projectFilter
					? ['title', 'source', 'branch', 'status', 'created']
					: ['title', 'source', 'workshop', 'branch', 'status', 'created']}
			/>
		</div>
	{/if}
</div>

<style>
	.body {
		padding-top: 2px;
	}
	.filters {
		display: flex;
		gap: 8px;
		align-items: center;
		flex-wrap: wrap;
		flex: none;
	}
	.sel {
		width: auto;
		min-width: 150px;
	}
	@media (max-width: 760px) {
		.sel {
			width: 100%;
		}
	}
</style>
