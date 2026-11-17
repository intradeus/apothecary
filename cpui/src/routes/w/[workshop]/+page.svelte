<script lang="ts">
	// Workshop overview (DESIGN 7.7): the header, then a tab strip — Overview,
	// Runs, Tickets, Merge requests, Flows, Automations, Health, Settings —
	// driven by ?tab=.
	import { errorMessage } from '$lib/core/errors';
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { api } from '$lib/api';
	import type { Flow, Integration, Project } from '$lib/domain';
	import { fleet } from '$lib/app/fleet.svelte';
	import { to, type WorkshopTab } from '$lib/core/urls';
	import { t } from '$lib/i18n/index.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import { provideHeaderSlot } from '$lib/ui/headerSlot.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { I } from '$lib/design/icons';
	import Tabs from '$lib/ui/Tabs.svelte';
	import WorkshopChips from '$lib/workshop/WorkshopChips.svelte';
	import WorkshopBadge from '$lib/workshop/WorkshopBadge.svelte';
	import OverviewTab from '$lib/workshop/OverviewTab.svelte';
	import RunsTab from '$lib/workshop/RunsTab.svelte';
	import TicketsTab from '$lib/workshop/TicketsTab.svelte';
	import FlowsTab from '$lib/workshop/FlowsTab.svelte';
	import AutomationsTab from '$lib/workshop/AutomationsTab.svelte';
	import MergeRequestsTab from '$lib/workshop/MergeRequestsTab.svelte';
	import HealthTab from '$lib/workshop/HealthTab.svelte';
	import SettingsTab from '$lib/workshop/SettingsTab.svelte';

	const slot = provideHeaderSlot();
	const projectId = $derived(page.params.workshop!);
	const TABS: WorkshopTab[] = [
		'overview',
		'runs',
		'tickets',
		'mrs',
		'flows',
		'automations',
		'health',
		'settings'
	];
	const tab = $derived.by<WorkshopTab>(() => {
		const q = page.url.searchParams.get('tab') as WorkshopTab | null;
		return q && TABS.includes(q) ? q : 'overview';
	});

	let project = $state<Project | null>(null);
	let integrations = $state<Integration[] | null>(null);
	let flows = $state<Flow[] | null>(null);
	let error = $state('');
	let flowsError = $state('');

	async function loadProject() {
		try {
			project = await api.projects.get(projectId);
			error = '';
		} catch (e) {
			error = errorMessage(e);
		}
	}
	async function loadFlows() {
		try {
			flows = await api.flows.list(projectId);
			flowsError = '';
		} catch (e) {
			flowsError = errorMessage(e);
		}
	}

	onMount(() => {
		void loadProject();
		void loadFlows();
		api.integrations
			.list()
			.then((l) => (integrations = l))
			.catch(() => (integrations = []));
	});

	const runs = $derived(fleet.runs.filter((r) => r.projectId === projectId));
	const live = $derived(fleet.liveByProject.get(projectId) ?? []);
	const pings = $derived(fleet.pingsByProject.get(projectId) ?? 0);
	const ownFlows = $derived((flows ?? []).filter((f) => !f.builtin));

	const counts = $derived<Partial<Record<WorkshopTab, number | undefined>>>({
		runs: live.length || undefined,
		flows: flows ? ownFlows.length : undefined
	});
	const LABELS: Record<WorkshopTab, string> = $derived({
		overview: t('page.workshop.tabOverview'),
		runs: t('page.runs.title'),
		tickets: t('page.tickets.title'),
		mrs: t('page.workshop.tabMrs'),
		flows: t('page.workshops.flows'),
		automations: t('page.workshop.tabAutomations'),
		health: t('page.workshop.tabHealth'),
		settings: t('page.workshop.tabSettings')
	});

	const repoLabel = (url: string) => url.replace(/^https?:\/\//, '').replace(/\.git$/, '');
	const repoHref = (url: string) => (/^https?:\/\//.test(url) ? url.replace(/\.git$/, '') : null);
	const fallbackName = $derived(fleet.projectById.get(projectId)?.name);
</script>

<PageHeader
	crumbs={[
		{ label: t('nav.desk'), href: to.desk() },
		{ label: t('page.workshops.title'), href: to.workshops() },
		{ label: project?.name ?? fallbackName ?? '…' }
	]}
	title={project?.name ?? fallbackName ?? ''}
>
	{#snippet lead()}<WorkshopBadge {projectId} live={live.length} />{/snippet}
	{#snippet chip()}
		{#if live.length}<span class="pill tint-running"
				><span class="dot pulse"></span>{t('page.desk.nLive', { count: live.length })}</span
			>{/if}
		{#if pings}<span class="pill tint-hot"
				>{t('page.workshop.pingsAwaiting', { count: pings })}</span
			>{/if}
	{/snippet}
	{#snippet meta()}
		{#if project}
			{@const href = repoHref(project.repo.url)}
			{#if href}
				<a class="mono repo" {href} target="_blank" rel="noreferrer"
					>{repoLabel(project.repo.url)} {I.external}</a
				>
			{:else}
				<span class="mono repo">{project.repo.url}</span>
			{/if}
			{#if project.repos?.length}<span class="faint"
					>{t('page.workshops.moreRepos', { count: project.repos.length })}</span
				>{/if}
			<span class="mono faint">{I.branch} {project.repo.defaultBranch}</span>
			<WorkshopChips {project} {integrations} />
		{:else if !error}
			<span class="meta-skel"><Skeleton lines={1} /></span>
		{/if}
	{/snippet}
	{#snippet actions()}
		{#if project}
			<div class="acts">{@render slot.action?.()}</div>
		{/if}
	{/snippet}
</PageHeader>

<div class="wt glass">
	<Tabs
		label={t('page.workshop.sections')}
		value={tab}
		items={TABS.map((tb) => ({
			value: tb,
			label: LABELS[tb],
			count: counts[tb],
			href: to.workshop(projectId, tb)
		}))}
	/>
</div>

<div class="page glass">
	{#if error && !project}
		<ErrorCard cause={t('page.settings.loadError')} detail={error} onretry={loadProject} />
	{:else if !project}
		<Skeleton pane lines={5} />
	{:else}
		{#if error}<ErrorCard cause={t('page.channels.failed')} detail={error} />{/if}
		<div class="tab readable">
			{#if tab === 'overview'}
				<OverviewTab {project} {integrations} {flows} {runs} />
			{:else if tab === 'runs'}
				<RunsTab {project} {runs} {integrations} />
			{:else if tab === 'tickets'}
				<TicketsTab {project} {integrations} />
			{:else if tab === 'mrs'}
				<MergeRequestsTab {project} {flows} />
			{:else if tab === 'flows'}
				<FlowsTab {project} {flows} error={flowsError} onreload={loadFlows} />
			{:else if tab === 'automations'}
				<AutomationsTab {project} {integrations} />
			{:else if tab === 'health'}
				<HealthTab {project} />
			{:else if tab === 'settings'}
				<SettingsTab {project} {integrations} {flows} {runs} onsaved={(p) => (project = p)} />
			{/if}
		</div>
	{/if}
</div>

<style>
	.repo {
		color: var(--text);
		font-size: var(--fs-help);
	}
	.acts {
		display: flex;
		gap: 6px;
		align-items: center;
	}
	.meta-skel {
		display: block;
		width: var(--aside-w);
		max-width: 100%;
	}
	.wt {
		flex: none;
		margin: var(--shell-gap) var(--shell-gap) 0;
		padding: 0 8px;
		border-radius: var(--r-btn);
	}
	.wt :global(.tabs) {
		border-bottom: 0;
	}
	/* The tab's panes clip (overflow: hidden), so they must not shrink in the
	   page's flex column. */
	.tab {
		flex: none;
		display: flex;
		flex-direction: column;
		gap: 14px;
	}
</style>
