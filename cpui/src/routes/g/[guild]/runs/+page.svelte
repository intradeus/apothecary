<!-- Runs list (DESIGN 7.4). Runs start from tickets, so there is no "New run"
     here. Reads the shared fleet poll (4s); the workshop filter and tab mirror
     ?project= and ?tab= so links from a workshop land pre-filtered. -->
<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { currentGuild, to } from '$lib/guild';
	import { fleet } from '$lib/fleet.svelte';
	import { stateTab, type RunTab } from '$lib/format';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Updated from '$lib/ui/Updated.svelte';
	import SearchInput from '$lib/ui/SearchInput.svelte';
	import RunTable from '$lib/run/RunTable.svelte';
	import WorkshopFilter from '$lib/shell/WorkshopFilter.svelte';

	const projectFilter = $derived(page.url.searchParams.get('project') ?? '');
	const tab = $derived<RunTab>(
		page.url.searchParams.get('tab') === 'finished' ? 'finished' : 'active'
	);
	let q = $state('');

	function setParams(next: { project?: string; tab?: RunTab }) {
		const project = next.project ?? projectFilter;
		const t = next.tab ?? tab;
		void goto(
			to.runs({ project: project || undefined, tab: t === 'finished' ? 'finished' : undefined }),
			{
				replaceState: true,
				keepFocus: true,
				noScroll: true
			}
		);
	}

	const scoped = $derived(
		fleet.runs.filter((r) => !projectFilter || r.projectId === projectFilter)
	);
	const searched = $derived.by(() => {
		const s = q.trim().toLowerCase();
		if (!s) return scoped;
		return scoped.filter((r) =>
			[r.title, r.id, r.flowName, r.currentStage, r.branch]
				.filter(Boolean)
				.some((v) => v!.toLowerCase().includes(s))
		);
	});
	const active = $derived(searched.filter((r) => stateTab[r.state] === 'active'));
	const finished = $derived(
		searched
			.filter((r) => stateTab[r.state] === 'finished')
			.sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
	);
	const shown = $derived(tab === 'active' ? active : finished);
	const projectName = $derived(projectFilter ? fleet.projectName(projectFilter) : '');
</script>

<svelte:head><title>Runs</title></svelte:head>

<PageHeader
	crumbs={[
		{ label: currentGuild().name, href: to.desk() },
		...(projectFilter ? [{ label: projectName, href: to.workshop(projectFilter) }] : []),
		{ label: 'Runs' }
	]}
	title={projectName ? `Runs in ${projectName}` : 'Runs'}
	accent={tab === 'active' ? 'on the fire' : 'bottled & spoiled'}
>
	{#snippet meta()}
		<span>{fleet.active.length} active · {fleet.finished.length} finished</span>
		<Updated at={fleet.updatedAt} paused={fleet.paused} />
	{/snippet}
	{#snippet actions()}
		<div class="ctl">
			<Segmented
				value={tab}
				label="Runs"
				onchange={(v) => setParams({ tab: v })}
				options={[
					{ value: 'active', label: 'Active', count: active.length },
					{ value: 'finished', label: 'Finished', count: finished.length }
				]}
			/>
			<WorkshopFilter value={projectFilter} onchange={(project) => setParams({ project })} />
			<span class="sw"
				><SearchInput bind:value={q} width={220} placeholder="Search title, id, stage…" /></span
			>
		</div>
	{/snippet}
</PageHeader>

<div class="page body">
	{#if !fleet.updatedAt && fleet.error}
		<ErrorCard cause="Could not load runs" detail={fleet.error} onretry={() => fleet.refresh()} />
	{:else if !fleet.updatedAt}
		<Skeleton pane lines={6} />
	{:else if shown.length === 0}
		{#if fleet.runs.length === 0}
			<EmptyState
				title="All still on the shelf"
				text="Runs start from tickets. Write one, then start a run against it."
			>
				<a class="btn btn-primary" href={to.tickets()}>Open tickets</a>
			</EmptyState>
		{:else if q.trim()}
			<EmptyState compact title="No runs match" text="Nothing here matches “{q.trim()}”.">
				<button onclick={() => (q = '')}>Clear search</button>
			</EmptyState>
		{:else if tab === 'active'}
			<EmptyState
				title="All still on the shelf"
				text="No active runs{projectName ? ` in ${projectName}` : ''}. Start one from a ticket."
			>
				<a
					class="btn btn-primary"
					href={to.tickets(projectFilter ? { project: projectFilter } : undefined)}>Open tickets</a
				>
			</EmptyState>
		{:else}
			<EmptyState
				compact
				title="Nothing finished yet"
				text="Completed, failed and archived runs land here."
			/>
		{/if}
	{:else}
		<div class="c pane">
			<RunTable
				runs={shown}
				facts={tab === 'active'}
				cols={projectFilter
					? ['title', 'state', 'ws', 'stage', 'flow', 'mr', 'updated']
					: ['title', 'state', 'ws', 'stage', 'workshop', 'flow', 'mr', 'updated']}
			/>
		</div>
	{/if}
</div>

<style>
	.sw {
		display: flex;
		width: 220px;
	}
	@media (max-width: 760px) {
		.sw {
			width: 100%;
		}
	}
</style>
