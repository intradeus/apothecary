<!-- A merge request as the place to start a run on it (DESIGN 7.6, the
     ticket page's twin): its description on the left — what the run is
     given as its work, no ticket made — "Start a run" on the right with the
     flows that begin on a merge request, and the runs that went through it
     below. -->
<script lang="ts">
	import { errorMessage } from '$lib/core/errors';
	import { goto } from '$app/navigation';
	import { to } from '$lib/core/urls';
	import { api, busyRunOf } from '$lib/api';
	import { fleet } from '$lib/app/fleet.svelte';
	import { shortId } from '$lib/core/format';
	import type { MergeRequestRow, SkillsetRef } from '$lib/domain';
	import { t, th } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import FlowSelect from '$lib/flow/FlowSelect.svelte';
	import WorkshopName from '$lib/app/WorkshopName.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import SectionHeader from '$lib/ui/SectionHeader.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Updated from '$lib/ui/Updated.svelte';
	import TransportIcon from '$lib/ui/TransportIcon.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import RunTable from '$lib/run/RunTable.svelte';
	import StartRunOptions from '$lib/run/StartRunOptions.svelte';
	import MrMeta from './MrMeta.svelte';
	import { runsOnMR } from './runsOnMR';

	let { projectId, iid, repo = '' }: { projectId: string; iid: number; repo?: string } = $props();

	let row = $state<MergeRequestRow | null>(null);
	let loaded = $state(false);
	let loadErr = $state('');
	let error = $state('');

	let flow = $state(''); // '' = the workshop's default, when it starts on a merge request
	let params = $state<Record<string, string>>({});
	let skillsets = $state<SkillsetRef[]>([]);
	let starting = $state(false);
	/** The run in the way when a start was refused: one at a time on it. */
	let busyRun = $state('');

	const project = $derived(fleet.projectById.get(projectId));
	// The runs the shared poll carries that went through it: started on it,
	// or the one that opened it.
	const runs = $derived(runsOnMR(fleet.runs, projectId, iid, repo));
	const open = $derived(row?.mr.state === 'open');

	async function load() {
		const key = `${projectId}/${iid}/${repo}`;
		row = null;
		loaded = false;
		loadErr = '';
		try {
			const got = await api.projects.mergeRequest(projectId, iid, repo || undefined);
			if (key === `${projectId}/${iid}/${repo}`) row = got;
		} catch (e) {
			if (key === `${projectId}/${iid}/${repo}`) loadErr = errorMessage(e);
		} finally {
			if (key === `${projectId}/${iid}/${repo}`) loaded = true;
		}
	}
	$effect(() => {
		void [projectId, iid, repo];
		void load();
	});

	async function startRun() {
		if (starting || !row) return;
		starting = true;
		error = '';
		busyRun = '';
		try {
			const { runId } = await api.projects.startRunOnMR(projectId, iid, {
				flow: flow.trim() || undefined,
				repo: repo || undefined,
				params,
				skillsets
			});
			void fleet.refresh();
			toast(t('page.ticket.runStarted', { id: shortId(runId) }), {
				action: { label: t('page.ping.openRun'), run: () => void goto(to.run(runId)) }
			});
			await goto(to.run(runId));
		} catch (e) {
			busyRun = busyRunOf(e) ?? '';
			if (!busyRun) error = errorMessage(e);
		} finally {
			starting = false;
		}
	}

	async function abortBusy() {
		const id = busyRun;
		const ok = await confirmDialog({
			title: t('run.stop.title', { id: shortId(id) }),
			body: `${th('run.stop.lead')} ${t('run.stop.kept')}`,
			action: t('run.stop.action')
		});
		if (!ok) return;
		try {
			await api.runs.abort(id);
			busyRun = '';
			void fleet.refresh();
		} catch (e) {
			error = errorMessage(e);
		}
	}

	const crumbs = $derived([
		{ label: t('nav.desk'), href: to.desk() },
		{ label: fleet.projectName(projectId), href: to.workshop(projectId) },
		{ label: t('page.mr.title'), href: to.mergeRequests() },
		{ label: `!${iid}`, mono: true }
	]);
</script>

<svelte:head><title>{row?.mr.title ?? t('page.mr.fallbackTitle')}</title></svelte:head>

{#if row}
	<PageHeader {crumbs} title={row.mr.title}>
		{#snippet chip()}
			{#if runs.length}<StatusChip state={runs[0].state} suffix />{/if}
		{/snippet}
		{#snippet meta()}
			<span class="src">
				<span class="tag">!{iid}</span>
				<a href={row!.mr.url} target="_blank" rel="noreferrer" title={t('workshop.mrs.onHost')}
					>{I.external}</a
				>
			</span>
			<WorkshopName {projectId} link />
			<span class="mono">{I.branch} {row!.mr.source} → {row!.mr.target}</span>
			<MrMeta row={row!} />
		{/snippet}
	</PageHeader>

	<div class="page glass">
		{#if error}<ErrorCard cause={error} ondismiss={() => (error = '')} />{/if}
		<div class="grid">
			<div class="work">
				<section class="glass pane">
					<div class="pane-head">
						<h2>{t('page.mr.description')}</h2>
						<span class="help">{t('page.mr.descriptionHelp')}</span>
					</div>
					<div class="pane-body body">
						{#if row.mr.body.trim()}
							<Markdown src={row.mr.body} />
						{:else}
							<p class="empty-line">{t('page.mr.noDescription')}</p>
						{/if}
					</div>
				</section>
				<section class="runs">
					<SectionHeader title={t('page.runs.title')} count={runs.length}>
						{#snippet actions()}<Updated at={fleet.updatedAt} paused={fleet.paused} />{/snippet}
					</SectionHeader>
					{#if runs.length === 0}
						<EmptyState compact title={t('page.runs.shelf')} text={t('page.mr.noRuns')} />
					{:else}
						<div class="pane">
							<RunTable {runs} cols={['id', 'state', 'ws', 'stage', 'flow', 'updated']} />
						</div>
					{/if}
				</section>
			</div>

			<aside class="start glass pane">
				<div class="pane-head"><h2>{t('page.ticket.startRun')}</h2></div>
				<div class="pane-body sb">
					{#if !open}
						<p class="help hue-warning">{t('page.mr.notOpen', { state: row.mr.state })}</p>
					{/if}
					<div class="field">
						<label for="mr-flow">{t('page.ticket.flow')}</label>
						<FlowSelect
							id="mr-flow"
							{projectId}
							operatesOn="merge_request"
							fallbackFlow={project?.defaultFlow}
							bind:value={flow}
							fallbackLabel={t('page.ticket.defaultFlow', {
								flow: project?.defaultFlow || t('page.ticket.projectDefault')
							})}
						/>
					</div>
					{#if project}
						<StartRunOptions
							{projectId}
							flowName={flow.trim() || project.defaultFlow}
							bind:params
							bind:skillsets
							askMR={false}
							idPrefix="mr"
						/>
					{/if}
					{#if busyRun}
						<div class="busy tint-warning" role="alert">
							<span>{t('page.mr.busy', { id: shortId(busyRun) })}</span>
							<a class="btn btn-sm" href={to.run(busyRun)}>{t('page.ping.openRun')}</a>
							<button class="btn-sm btn-danger" onclick={abortBusy}>{t('run.stop.action')}</button>
						</div>
					{/if}
					<button
						class="btn-primary btn-lg go"
						onclick={startRun}
						disabled={!open}
						aria-busy={starting}
					>
						<TransportIcon kind="play" />
						{t('page.ticket.startRunBtn')}
					</button>
				</div>
			</aside>
		</div>
	</div>
{:else if loaded}
	<PageHeader {crumbs} title={t('page.mr.fallbackTitle')} />
	<div class="page glass">
		<EmptyState title={t('page.mr.gone')} text={loadErr || t('page.mr.goneText')}>
			<a class="btn btn-primary" href={to.mergeRequests()}>{t('page.mr.all')}</a>
		</EmptyState>
	</div>
{:else}
	<PageHeader {crumbs} title={t('page.mr.fallbackTitle')} />
	<div class="page glass"><Skeleton pane lines={8} /></div>
{/if}

<style>
	.src {
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}
	.grid {
		display: grid;
		grid-template-columns: minmax(0, 1fr) var(--aside-w);
		gap: 12px;
		align-items: start;
		flex: none;
	}
	.work {
		display: flex;
		flex-direction: column;
		gap: var(--shell-gap);
		min-width: 0;
	}
	.start {
		border-radius: var(--r-group);
	}
	.start .pane-head {
		border-bottom-color: var(--glass-line);
	}
	.body > :global(.md) {
		max-width: var(--measure);
	}
	.body .empty-line {
		padding: 0;
		margin: 0;
	}
	.sb {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.sb > .help {
		margin: 0;
	}
	.go {
		align-self: stretch;
		margin-top: 2px;
	}
	.busy {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 8px;
		padding: 8px 10px;
		border-width: 1px;
		border-style: solid;
		border-radius: var(--r-sm);
	}
	.runs {
		display: flex;
		flex-direction: column;
		gap: 10px;
		flex: none;
	}
	@media (max-width: 1000px) {
		.grid {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
