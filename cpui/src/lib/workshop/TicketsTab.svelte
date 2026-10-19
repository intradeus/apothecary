<script lang="ts">
	// Workshop tickets tab (DESIGN 7.6 list, filtered to this workshop). The
	// status is derived: the latest run's state, or "no runs".
	import type { Project, Ticket } from '$lib/domain';
	import { fleet } from '$lib/fleet.svelte';
	import { to } from '$lib/guild';
	import { stateTab } from '$lib/format';
	import TicketTable from '$lib/tickets/TicketTable.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import SearchInput from '$lib/ui/SearchInput.svelte';
	import { I } from '$lib/ui/icons';

	let {
		project,
		tickets,
		error,
		onretry
	}: { project: Project; tickets: Ticket[] | null; error: string; onretry: () => void } = $props();

	let q = $state('');
	let filter = $state<'all' | 'active' | 'idle'>('all');

	const hasActive = (t: Ticket) =>
		(fleet.runsByTicket.get(t.id) ?? []).some((r) => stateTab[r.state] === 'active');

	const shown = $derived(
		(tickets ?? [])
			.filter((t) => {
				const s = q.trim().toLowerCase();
				if (
					s &&
					!t.title.toLowerCase().includes(s) &&
					!(t.source?.key ?? '').toLowerCase().includes(s) &&
					!t.goal.toLowerCase().includes(s)
				)
					return false;
				if (filter === 'active') return hasActive(t);
				if (filter === 'idle') return !hasActive(t);
				return true;
			})
			.sort((a, b) => b.createdAt.localeCompare(a.createdAt))
	);
	const activeN = $derived((tickets ?? []).filter(hasActive).length);
</script>

<div class="row">
	<Segmented
		options={[
			{ value: 'all', label: 'All', count: tickets?.length ?? '…' },
			{ value: 'active', label: 'Running', count: activeN },
			{ value: 'idle', label: 'Idle', count: (tickets?.length ?? 0) - activeN }
		]}
		bind:value={filter}
	/>
	<SearchInput bind:value={q} placeholder="Search title, key, goal" />
	<span class="spacer"></span>
	<a class="btn btn-sm" href={to.tickets({ project: project.id })}>Open in Tickets {I.forward}</a>
	<a class="btn btn-primary" href={to.newTicket({ project: project.id })}>{I.add} New ticket</a>
</div>

{#if error && !tickets}
	<ErrorCard cause="Couldn’t load tickets" detail={error} {onretry} />
{:else if !tickets}
	<Skeleton pane lines={4} />
{:else if shown.length}
	<section class="c pane">
		<TicketTable tickets={shown} cols={['title', 'source', 'branch', 'status', 'created']} />
	</section>
{:else if q || filter !== 'all'}
	<EmptyState compact title="No match" text="No ticket matches this filter." />
{:else}
	<EmptyState
		title="No tickets yet"
		text="A ticket is the work: a goal, files, and a branch. Runs start from it."
	>
		<a class="btn btn-primary" href={to.newTicket({ project: project.id })}>{I.add} New ticket</a>
	</EmptyState>
{/if}
