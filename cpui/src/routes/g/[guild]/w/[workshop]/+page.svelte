<script lang="ts">
	// Workshop overview (DESIGN 7.7): the header, then a tab strip — Overview,
	// Runs, Tickets, Flows, Agents, Health — driven by ?tab=.
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api } from '$lib/api';
	import type { Flow, Integration, Project, Ticket } from '$lib/domain';
	import { fleet } from '$lib/fleet.svelte';
	import { currentGuild, to, type WorkshopTab } from '$lib/guild';
	import { lore } from '$lib/lore';
	import { plural } from '$lib/format';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { esc } from '$lib/system/html';
	import WorkshopChips from '$lib/workshop/WorkshopChips.svelte';
	import WorkshopBadge from '$lib/workshop/WorkshopBadge.svelte';
	import OverviewTab from '$lib/workshop/OverviewTab.svelte';
	import RunsTab from '$lib/workshop/RunsTab.svelte';
	import TicketsTab from '$lib/workshop/TicketsTab.svelte';
	import FlowsTab from '$lib/workshop/FlowsTab.svelte';
	import AgentsTab from '$lib/workshop/AgentsTab.svelte';
	import HealthTab from '$lib/workshop/HealthTab.svelte';

	const projectId = $derived(page.params.workshop!);
	const TABS: WorkshopTab[] = ['overview', 'runs', 'tickets', 'flows', 'agents', 'health'];
	const tab = $derived.by<WorkshopTab>(() => {
		const t = page.url.searchParams.get('tab') as WorkshopTab | null;
		return t && TABS.includes(t) ? t : 'overview';
	});

	let project = $state<Project | null>(null);
	let integrations = $state<Integration[] | null>(null);
	let flows = $state<Flow[] | null>(null);
	let tickets = $state<Ticket[] | null>(null);
	let error = $state('');
	let flowsError = $state('');
	let ticketsError = $state('');

	async function loadProject() {
		try {
			project = await api.projects.get(projectId);
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}
	async function loadFlows() {
		try {
			flows = await api.flows.list(projectId);
			flowsError = '';
		} catch (e) {
			flowsError = e instanceof Error ? e.message : String(e);
		}
	}
	async function loadTickets() {
		try {
			tickets = await api.tickets.list(projectId);
			ticketsError = '';
		} catch (e) {
			ticketsError = e instanceof Error ? e.message : String(e);
		}
	}

	onMount(() => {
		void loadProject();
		void loadFlows();
		void loadTickets();
		api.integrations
			.list()
			.then((l) => (integrations = l))
			.catch(() => (integrations = []));
	});

	const runs = $derived(fleet.runs.filter((r) => r.projectId === projectId));
	const live = $derived(fleet.liveByProject.get(projectId) ?? []);
	const pings = $derived(fleet.pingsByProject.get(projectId) ?? 0);
	/** Runs that still hold the workshop: it can only be deleted once every
	 *  one of them is archived. */
	const blocking = $derived(runs.filter((r) => r.state !== 'archived'));
	const ownFlows = $derived((flows ?? []).filter((f) => !f.builtin));

	const counts = $derived<Partial<Record<WorkshopTab, number | undefined>>>({
		runs: live.length || undefined,
		tickets: tickets?.length,
		flows: flows ? ownFlows.length : undefined
	});
	const LABELS: Record<WorkshopTab, string> = {
		overview: 'Overview',
		runs: 'Runs',
		tickets: 'Tickets',
		flows: 'Flows',
		agents: 'Agents',
		health: 'Health'
	};

	let delBtn = $state<HTMLButtonElement | null>(null);
	let blockedOpen = $state(false);

	async function remove() {
		if (!project) return;
		if (blocking.length) {
			blockedOpen = true;
			return;
		}
		const ok = await confirmDialog({
			title: `Delete ${project.name}?`,
			body: `The ${lore('workshop').toLowerCase()} <b>${esc(project.name)}</b> is deleted, along with its <b>tickets</b>, <b>flows</b>, commands and egress policy. Every run is already archived, so no <b>workspace</b> is affected. Integrations are untouched.`,
			action: `Delete ${lore('workshop').toLowerCase()}`
		});
		if (!ok) return;
		try {
			await api.projects.remove(project.id);
			await fleet.refresh();
			await goto(to.workshops());
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	const repoLabel = (url: string) => url.replace(/^https?:\/\//, '').replace(/\.git$/, '');
	const repoHref = (url: string) => (/^https?:\/\//.test(url) ? url.replace(/\.git$/, '') : null);
	const guild = $derived(currentGuild());
	const fallbackName = $derived(fleet.projectById.get(projectId)?.name);
</script>

<PageHeader
	crumbs={[
		{ label: guild.name, href: to.desk() },
		{ label: lore('workshops'), href: to.workshops() },
		{ label: project?.name ?? fallbackName ?? '…' }
	]}
	title={project?.name ?? fallbackName ?? ''}
>
	{#snippet lead()}<WorkshopBadge {projectId} live={live.length} />{/snippet}
	{#snippet chip()}
		{#if live.length}<span class="pill t-az"><span class="dot pulse"></span>{live.length} live</span
			>{/if}
		{#if pings}<span class="pill hotpill">{plural(pings, 'ping')} awaiting you</span>{/if}
	{/snippet}
	{#snippet meta()}
		{#if project}
			{@const href = repoHref(project.repo.url)}
			{#if href}
				<a class="mono repo" {href} target="_blank" rel="noreferrer"
					>{repoLabel(project.repo.url)} ↗</a
				>
			{:else}
				<span class="mono repo">{project.repo.url}</span>
			{/if}
			{#if project.repos?.length}<span class="fnt">+{plural(project.repos.length, 'repo')}</span
				>{/if}
			<span class="mono fnt">⎇ {project.repo.defaultBranch}</span>
			<WorkshopChips {project} {integrations} />
		{:else if !error}
			<span class="skel" style="width:320px"></span>
		{/if}
	{/snippet}
	{#snippet actions()}
		{#if project}
			<div class="acts">
				<a class="btn" href={to.workshopSettings(project.id)}>Edit setup</a>
				<button
					bind:this={delBtn}
					class="btn-danger"
					onclick={remove}
					title={blocking.length
						? `${plural(blocking.length, 'run')} not archived yet`
						: `Delete this ${lore('workshop').toLowerCase()}`}>Delete…</button
				>
			</div>
		{/if}
	{/snippet}
</PageHeader>

<Popover anchor={delBtn} bind:open={blockedOpen} placement="below-end" width={320}>
	<div class="blocked">
		<div class="bt">Archive its runs first</div>
		<p>
			{plural(blocking.length, 'run')}
			{blocking.length === 1 ? 'still belongs' : 'still belong'} to this {lore(
				'workshop'
			).toLowerCase()}. Archiving a run destroys its workspace and keeps its record; once every run
			is archived the {lore('workshop').toLowerCase()} can be deleted.
		</p>
		<a
			class="btn btn-sm"
			href={to.workshop(projectId, 'runs')}
			onclick={() => (blockedOpen = false)}>Show runs →</a
		>
	</div>
</Popover>

<nav class="tabs wt" aria-label="{lore('workshop')} sections">
	{#each TABS as t (t)}
		<a
			href={to.workshop(projectId, t)}
			class:on={tab === t}
			aria-current={tab === t ? 'page' : undefined}
			data-sveltekit-noscroll
			>{LABELS[t]}{#if counts[t] !== undefined}<span class="count">{counts[t]}</span>{/if}</a
		>
	{/each}
</nav>

<div class="page">
	{#if error && !project}
		<ErrorCard
			cause="Couldn’t load this {lore('workshop').toLowerCase()}"
			detail={error}
			onretry={loadProject}
		/>
	{:else if !project}
		<div class="c pane"><div class="pane-body"><Skeleton lines={5} /></div></div>
	{:else}
		{#if error}<ErrorCard cause="That didn’t work" detail={error} />{/if}
		<div class="tab">
			{#if tab === 'overview'}
				<OverviewTab {project} {integrations} {flows} {runs} />
			{:else if tab === 'runs'}
				<RunsTab {project} {runs} />
			{:else if tab === 'tickets'}
				<TicketsTab {project} {tickets} error={ticketsError} onretry={loadTickets} />
			{:else if tab === 'flows'}
				<FlowsTab {project} {flows} error={flowsError} onreload={loadFlows} />
			{:else if tab === 'agents'}
				<AgentsTab {project} {flows} error={flowsError} onretry={loadFlows} />
			{:else if tab === 'health'}
				<HealthTab {project} />
			{/if}
		</div>
	{/if}
</div>

<style>
	.repo {
		color: var(--tx);
		font-size: 11.5px;
	}
	.acts {
		display: flex;
		gap: 6px;
		align-items: center;
		padding-top: 18px;
	}
	.hotpill {
		background: var(--amb);
		color: var(--ink);
		font-weight: 700;
	}
	.wt {
		flex: none;
		margin: 0 22px;
		padding: 0;
		gap: 4px;
		overflow-x: auto;
		scrollbar-width: none;
	}
	.wt > a {
		padding: 5px 11px;
	}
	.wt .count {
		padding: 0 5px;
	}
	/* The tab's panes clip (overflow: hidden), so they must not shrink in the
	   page's flex column. */
	.tab {
		flex: none;
		display: flex;
		flex-direction: column;
		gap: 14px;
	}
	.blocked {
		padding: 8px 10px 10px;
		display: flex;
		flex-direction: column;
		gap: 6px;
		align-items: flex-start;
	}
	.bt {
		font-family: var(--serif);
		font-size: 18px;
	}
	.blocked p {
		margin: 0;
		color: var(--mut);
		font-size: 12.5px;
		text-wrap: pretty;
	}
</style>
