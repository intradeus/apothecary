<!-- Tickets (DESIGN 7.6): units of work, each owning many runs. The status
     column is DERIVED — the latest run's state, or "no runs". -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { currentGuild, to } from '$lib/guild';
	import { api } from '$lib/api';
	import { fleet } from '$lib/fleet.svelte';
	import { lore, isLore } from '$lib/lore';
	import { ago, fmtAbs, plural, stateTab } from '$lib/format';
	import type { Run, Ticket } from '$lib/domain';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import Vial from '$lib/ui/Vial.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';

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

	// Runs per ticket, newest first.
	const runsByTicket = $derived.by(() => {
		const m = new Map<string, Run[]>();
		for (const r of fleet.runs) {
			if (!r.ticketId) continue;
			m.set(r.ticketId, [...(m.get(r.ticketId) ?? []), r]);
		}
		for (const arr of m.values()) arr.sort((a, b) => b.createdAt.localeCompare(a.createdAt));
		return m;
	});
	const hasActive = (t: Ticket) =>
		(runsByTicket.get(t.id) ?? []).some((r) => stateTab[r.state] === 'active');

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
	const projectName = (id: string) => fleet.projectById.get(id)?.name ?? id.slice(-6);
</script>

<svelte:head><title>Tickets</title></svelte:head>

<PageHeader
	crumbs={[
		{ label: currentGuild().name, href: to.desk() },
		...(projectFilter
			? [{ label: projectName(projectFilter), href: to.workshop(projectFilter) }]
			: []),
		{ label: 'Tickets' }
	]}
	title="Tickets"
	accent={isLore() ? 'for the bench' : undefined}
>
	{#snippet meta()}
		<span>{plural(tickets.length, 'ticket')}</span>
		<span>runs start from a ticket</span>
	{/snippet}
	{#snippet actions()}
		<a
			class="btn btn-primary btn-lg"
			href={to.newTicket(projectFilter ? { project: projectFilter } : undefined)}>+ New ticket</a
		>
	{/snippet}
</PageHeader>

<div class="page body">
	<div class="filters">
		<input class="search" type="search" bind:value={q} placeholder="⌕ Search title, key, goal…" />
		<select
			class="sel"
			aria-label="Filter by {lore('workshop').toLowerCase()}"
			value={projectFilter}
			onchange={(e) => setProject((e.target as HTMLSelectElement).value)}
		>
			<option value="">All {lore('workshops').toLowerCase()}</option>
			{#each fleet.projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
		</select>
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
		<div class="c pane sk"><Skeleton lines={6} /></div>
	{:else if tickets.length === 0}
		<EmptyState
			title="No tickets yet"
			text="A ticket is a unit of work. Write one, then start runs against it."
		>
			<a class="btn btn-primary" href={to.newTicket()}>+ New ticket</a>
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
			<div class="rows tk">
				<div class="r head">
					<span>Title</span><span class="wide">Source</span><span class="wide"
						>{lore('workshop')}</span
					><span class="wide">Base branch</span><span>Status</span><span class="right">Created</span
					>
				</div>
				{#each shown as t (t.id)}
					{@const runs = runsByTicket.get(t.id) ?? []}
					<div class="r">
						<a class="title ell stretch" href={to.ticket(t.id)}>{t.title}</a>
						<span class="wide">
							{#if t.source?.key}<span class="tag" title="Imported from {t.source.kind}"
									>{t.source.key}</span
								>{:else}<span class="fnt">—</span>{/if}
						</span>
						<span class="ws wide"
							><Vial projectId={t.projectId} /><span class="ell">{projectName(t.projectId)}</span
							></span
						>
						<span class="mono mut ell wide" title={t.baseBranch}>{t.baseBranch}</span>
						<span class="st">
							{#if runs.length}
								<StatusChip state={runs[0].state} size="sm" />
								{#if runs.length > 1}<span class="fnt mono n">×{runs.length}</span>{/if}
							{:else}
								<span class="fnt">no runs</span>
							{/if}
						</span>
						<span class="mono mut right" title={fmtAbs(t.createdAt)}>{ago(t.createdAt)}</span>
					</div>
				{/each}
			</div>
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
	.search {
		width: 280px !important;
	}
	.sel {
		width: auto;
		min-width: 150px;
	}
	.filters :global(label) {
		margin: 0;
	}
	.pane {
		border-radius: 16px;
		overflow: hidden;
		flex: none;
	}
	.sk {
		padding: 16px;
	}
	.tk > .r {
		grid-template-columns: minmax(0, 2fr) 110px minmax(0, 1fr) minmax(0, 1.2fr) 170px 74px;
		position: relative;
		font-size: 12.5px;
	}
	.title {
		font-weight: 500;
		color: var(--tx);
	}
	.title:hover {
		text-decoration: none;
	}
	.stretch::after {
		content: '';
		position: absolute;
		inset: 0;
	}
	.ws {
		display: flex;
		align-items: center;
		gap: 7px;
		min-width: 0;
		font-size: 12px;
	}
	.st {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.n {
		font-size: 10.5px;
	}
	.right {
		text-align: right;
		font-size: 11px;
	}
	@media (max-width: 760px) {
		.tk > .r {
			grid-template-columns: minmax(0, 1fr) auto 60px;
			padding-left: 14px;
			padding-right: 14px;
		}
		.wide {
			display: none !important;
		}
		.search,
		.sel {
			width: 100% !important;
		}
	}
</style>
