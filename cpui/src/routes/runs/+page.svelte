<!-- Runs list (DESIGN 7.4). Runs start from tickets, so "New run" picks one.
     Reads the shared fleet poll (4s); the workshop filter, the tab and the
     Finished tab's filters mirror ?project=, ?tab=, ?outcome= and ?archived=
     so links from a workshop land pre-filtered. -->
<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { to } from '$lib/core/urls';
	import { fleet } from '$lib/app/fleet.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { stateTab, type RunTab } from '$lib/core/format';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import { I } from '$lib/design/icons';
	import Updated from '$lib/ui/Updated.svelte';
	import SearchInput from '$lib/ui/SearchInput.svelte';
	import NewRunDialog from '$lib/run/NewRunDialog.svelte';
	import RunTable from '$lib/run/RunTable.svelte';
	import FinishedFilters from '$lib/run/FinishedFilters.svelte';
	import {
		finishedParams,
		matchesFinished,
		readFinishedFilter,
		ANY_FINISHED,
		type FinishedFilter
	} from '$lib/run/filter';
	import WorkshopFilter from '$lib/shell/WorkshopFilter.svelte';

	const projectFilter = $derived(page.url.searchParams.get('project') ?? '');
	const tab = $derived<RunTab>(
		page.url.searchParams.get('tab') === 'finished' ? 'finished' : 'active'
	);
	const filter = $derived(readFinishedFilter(page.url.searchParams));
	let q = $state('');
	let newRun = $state(false);

	function setParams(next: { project?: string; tab?: RunTab; filter?: FinishedFilter }) {
		const project = next.project ?? projectFilter;
		const tb = next.tab ?? tab;
		void goto(
			to.runs({
				project: project || undefined,
				tab: tb === 'finished' ? 'finished' : undefined,
				...finishedParams(next.filter ?? filter)
			}),
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
	const allFinished = $derived(searched.filter((r) => stateTab[r.state] === 'finished'));
	const finished = $derived(
		allFinished
			.filter((r) => matchesFinished(r, filter))
			.sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
	);
	const shown = $derived(tab === 'active' ? active : finished);
	const projectName = $derived(projectFilter ? fleet.projectName(projectFilter) : '');
</script>

<svelte:head><title>{t('page.runs.title')}</title></svelte:head>

<PageHeader
	crumbs={[
		{ label: t('nav.desk'), href: to.desk() },
		...(projectFilter ? [{ label: projectName, href: to.workshop(projectFilter) }] : []),
		{ label: t('page.runs.title') }
	]}
	title={projectName ? t('page.runs.titleIn', { name: projectName }) : t('page.runs.title')}
	accent={tab === 'active' ? t('page.runs.accentActive') : t('page.runs.accentFinished')}
>
	{#snippet meta()}
		<span
			>{t('page.runs.meta', { active: fleet.active.length, finished: fleet.finished.length })}</span
		>
		<Updated at={fleet.updatedAt} paused={fleet.paused} />
	{/snippet}
	{#snippet actions()}
		<button class="btn-primary btn-lg" onclick={() => (newRun = true)}
			>{I.add} {t('page.runs.new')}</button
		>
	{/snippet}
</PageHeader>

<NewRunDialog bind:open={newRun} projectId={projectFilter} />

<div class="page glass">
	<div class="filters">
		<SearchInput bind:value={q} placeholder={t('page.runs.search')} />
		<WorkshopFilter value={projectFilter} onchange={(project) => setParams({ project })} />
		<Segmented
			value={tab}
			label={t('page.runs.title')}
			onchange={(v) => setParams({ tab: v })}
			options={[
				{ value: 'active', label: t('page.runs.tabActive'), count: active.length },
				{ value: 'finished', label: t('page.runs.tabFinished'), count: finished.length }
			]}
		/>
		{#if tab === 'finished'}
			<FinishedFilters value={filter} onchange={(f) => setParams({ filter: f })} />
		{/if}
	</div>

	{#if !fleet.updatedAt && fleet.error}
		<ErrorCard
			cause={t('page.runs.loadError')}
			detail={fleet.error}
			onretry={() => fleet.refresh()}
		/>
	{:else if !fleet.updatedAt}
		<Skeleton pane lines={6} />
	{:else if shown.length === 0}
		{#if fleet.runs.length === 0}
			<EmptyState title={t('page.runs.shelf')} text={t('page.runs.shelfText')}>
				<a class="btn btn-primary" href={to.tickets()}>{t('page.runs.openTickets')}</a>
			</EmptyState>
		{:else if q.trim()}
			<EmptyState
				compact
				title={t('page.runs.noMatch')}
				text={t('page.runs.noMatchText', { q: q.trim() })}
			>
				<button onclick={() => (q = '')}>{t('page.runs.clearSearch')}</button>
			</EmptyState>
		{:else if tab === 'active'}
			<EmptyState
				title={t('page.runs.shelf')}
				text={projectName
					? t('page.runs.noActiveIn', { name: projectName })
					: t('page.runs.noActive')}
			>
				<a
					class="btn btn-primary"
					href={to.tickets(projectFilter ? { project: projectFilter } : undefined)}
					>{t('page.runs.openTickets')}</a
				>
			</EmptyState>
		{:else if allFinished.length}
			<EmptyState compact title={t('run.filter.noMatch')} text={t('run.filter.noMatchText')}>
				<button onclick={() => setParams({ filter: ANY_FINISHED })}>{t('run.filter.clear')}</button>
			</EmptyState>
		{:else}
			<EmptyState
				compact
				title={t('page.runs.nothingFinished')}
				text={t('page.runs.nothingFinishedText')}
			/>
		{/if}
	{:else}
		<div class="pane">
			<RunTable
				runs={shown}
				cols={projectFilter
					? ['title', 'state', 'ws', 'stage', 'flow', 'mr', 'updated']
					: ['title', 'state', 'ws', 'stage', 'workshop', 'flow', 'mr', 'updated']}
			/>
		</div>
	{/if}
</div>
