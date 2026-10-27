<!-- Tickets (DESIGN 7.6): units of work, each owning many runs. The status
     column is DERIVED — the latest run's state, or "no runs". -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { currentGuild, to } from '$lib/guild';
	import { api } from '$lib/api';
	import { fleet } from '$lib/fleet.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { stateTab } from '$lib/format';
	import type { Ticket } from '$lib/domain';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import SearchInput from '$lib/ui/SearchInput.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
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
	const sourceOptions = $derived<SelectOption[]>([
		{ value: '', label: t('page.tickets.anySource') },
		{ value: 'manual', label: t('page.tickets.writtenHere') },
		...kinds.map((k) => ({ value: k, label: t('page.tickets.imported', { kind: k }) }))
	]);

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

<svelte:head><title>{t('page.tickets.title')}</title></svelte:head>

<PageHeader
	crumbs={[
		{ label: currentGuild().name, href: to.desk() },
		...(projectFilter
			? [{ label: fleet.projectName(projectFilter), href: to.workshop(projectFilter) }]
			: []),
		{ label: t('page.tickets.title') }
	]}
	title={t('page.tickets.title')}
	accent={t('page.tickets.accent')}
>
	{#snippet meta()}
		<span>{t('page.tickets.count', { count: tickets.length })}</span>
		<span>{t('page.tickets.meta')}</span>
	{/snippet}
	{#snippet actions()}
		<a
			class="btn btn-primary btn-lg"
			href={to.newTicket(projectFilter ? { project: projectFilter } : undefined)}
			>{I.add} {t('page.desk.newTicket')}</a
		>
	{/snippet}
</PageHeader>

<div class="page body">
	<div class="filters">
		<SearchInput bind:value={q} placeholder={t('page.tickets.search')} />
		<WorkshopFilter value={projectFilter} onchange={setProject} />
		<span class="sel">
			<Select label={t('page.tickets.filterSource')} options={sourceOptions} bind:value={source} />
		</span>
		<Toggle bind:checked={activeOnly} label={t('page.tickets.hasActive')} />
	</div>

	{#if error && !tickets.length}
		<ErrorCard cause={t('page.tickets.loadError')} detail={error} onretry={load} />
	{:else if !loaded}
		<Skeleton pane lines={6} />
	{:else if tickets.length === 0}
		<EmptyState title={t('page.tickets.empty')} text={t('page.tickets.emptyText')}>
			<a class="btn btn-primary" href={to.newTicket()}>{I.add} {t('page.desk.newTicket')}</a>
		</EmptyState>
	{:else if shown.length === 0}
		<EmptyState compact title={t('page.tickets.noMatch')} text={t('page.tickets.noMatchText')}>
			<button
				onclick={() => {
					q = '';
					source = '';
					activeOnly = false;
					setProject('');
				}}>{t('page.tickets.clearFilters')}</button
			>
		</EmptyState>
	{:else}
		<div class="c pane">
			<TicketTable
				tickets={shown}
				cols={projectFilter
					? ['title', 'source', 'branch', 'runs', 'created']
					: ['title', 'source', 'workshop', 'branch', 'runs', 'created']}
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
		display: inline-block;
		min-width: 150px;
	}
	@media (max-width: 760px) {
		.sel {
			display: block;
			width: 100%;
		}
	}
</style>
