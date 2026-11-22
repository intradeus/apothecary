<!-- Ticket detail (DESIGN 7.6), read live from the workshop's source through
     its key: the tracker state (status, labels, links, base branch), the
     goal, attachments and discussion on the left, "Start a run" on the
     right, the ticket's runs below (shared 4s poll). Only a ticket on the
     internal tracker is edited or deleted here. The route keys one instance
     per workshop and key: neither prop changes under it. Lives in workshop,
     not tickets, because it composes the run feature's start panel and
     table. -->
<script lang="ts">
	import { errorMessage } from '$lib/core/errors';
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { to } from '$lib/core/urls';
	import { api, busyRunOf } from '$lib/api';
	import { fleet } from '$lib/app/fleet.svelte';
	import { shortId } from '$lib/core/format';
	import type { Branch, SkillsetRef, TicketAttachments, TicketDetail } from '$lib/domain';
	import { t, th } from '$lib/i18n/index.svelte';
	import TicketForm from '$lib/tickets/TicketForm.svelte';
	import ChangeTypeTag from '$lib/tickets/ChangeTypeTag.svelte';
	import TicketStatusChip from '$lib/tickets/TicketStatusChip.svelte';
	import TicketTracker from '$lib/tickets/TicketTracker.svelte';
	import TicketComments from '$lib/tickets/TicketComments.svelte';
	import FileChips from '$lib/artifacts/FileChips.svelte';
	import AttachFiles from '$lib/artifacts/AttachFiles.svelte';
	import FileChip from '$lib/ui/FileChip.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import SectionHeader from '$lib/ui/SectionHeader.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Updated from '$lib/ui/Updated.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/design/icons';
	import RunTable from '$lib/run/RunTable.svelte';
	import StartRunPanel from '$lib/run/StartRunPanel.svelte';
	import WorkshopName from '$lib/app/WorkshopName.svelte';
	import TicketHistory from './TicketHistory.svelte';

	let { projectId, ticketKey: key }: { projectId: string; ticketKey: string } = $props();

	let ticket = $state<TicketDetail | null>(null);
	let loaded = $state(false);
	let loadErr = $state('');
	let error = $state('');
	let editing = $state(false);
	/** Bumped on each read from the tracker: the thread follows. */
	let refreshed = $state(0);

	let flow = $state(''); // per-run flow; empty = project default
	let runParams = $state<Record<string, string>>({}); // parameter overrides for THIS run
	let runMR = $state(0); // the merge request to start the run on, for a flow started on one
	let runSkillsets = $state<SkillsetRef[]>([]); // skillsets installed into THIS run's workspace
	let sourceBranch = $state(''); // branch to fork this run off from; '' until branches load
	let branches = $state<Branch[]>([]);
	const branchOptions = $derived<SelectOption[]>(
		branches.map((b) => ({ value: b.name, label: b.isDefault ? `${I.star} ${b.name}` : b.name }))
	);
	let branchesErr = $state(''); // repository host unreachable ⇒ fall back to project default
	let runGoal = $state(''); // direction for THIS attempt (additive to the ticket goal)
	let runFiles = $state<File[]>([]);
	let starting = $state(false);
	/** The run in the way when a start was refused: one active run per ticket
	 *  across every workshop. */
	let busyRun = $state('');

	const project = $derived(fleet.projectById.get(projectId));
	/** Editing, deleting, moving and commenting on a ticket need ticket:write. */
	const canWrite = $derived(fleet.can(projectId, 'ticket:write'));
	const deny = $derived(canWrite ? undefined : t('common.needsPermission'));

	// The ticket's runs, newest first, from the shared fleet poll (4s); none
	// before it has a record.
	const runs = $derived(ticket?.ticketId ? (fleet.runsByTicket.get(ticket.ticketId) ?? []) : []);

	// The files every run of the ticket gets: those uploaded here and the
	// remote issue's own (copied in when a run starts).
	let attachments = $state<TicketAttachments>({ attachments: [], remote: [] });
	const fileCount = $derived(attachments.attachments.length + attachments.remote.length);

	async function loadAttachments() {
		try {
			attachments = await api.tickets.attachments(projectId, key);
		} catch {
			attachments = { attachments: [], remote: [] };
		}
	}

	// Fetch the repo's branches for the source-branch picker; pre-select the
	// default branch. On failure the picker shows the project default and the
	// run starts there (source_branch left empty).
	async function loadBranches() {
		try {
			branches = await api.projects.branches(projectId);
			const def = branches.find((b) => b.isDefault) ?? branches[0];
			if (def && !sourceBranch) sourceBranch = def.name;
		} catch (e) {
			branchesErr = errorMessage(e);
		}
	}

	onMount(() => {
		api.projects
			.ticket(projectId, key)
			.then((tk) => {
				ticket = tk;
				void loadBranches();
				void loadAttachments();
			})
			.catch((e) => (loadErr = errorMessage(e)))
			.finally(() => (loaded = true));
	});

	function changed(tk: TicketDetail) {
		ticket = tk;
		refreshed++;
		void loadAttachments();
	}

	async function startRun() {
		if (starting) return;
		starting = true;
		error = '';
		busyRun = '';
		try {
			let attachmentIds: string[] | undefined;
			if (runFiles.length) attachmentIds = (await api.files.upload(runFiles)).map((a) => a.id);
			const runId = await api.tickets.startRun(projectId, key, {
				flow: flow.trim() || undefined,
				goal: runGoal.trim() || undefined,
				sourceBranch: sourceBranch.trim() || undefined,
				attachmentIds,
				params: Object.keys(runParams).length ? runParams : undefined,
				mr: runMR ? { iid: runMR } : undefined,
				skillsets: runSkillsets.length ? runSkillsets : undefined
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

	let deleting = $state(false);
	async function del() {
		if (
			!(await confirmDialog({
				title: t('page.ticket.deleteTitle'),
				body: th('page.ticket.deleteBody'),
				action: t('page.ticket.deleteAction')
			}))
		)
			return;
		deleting = true;
		try {
			const title = ticket?.title;
			await api.tickets.remove(projectId, key);
			void fleet.refresh();
			toast(title ? t('page.ticket.deletedNamed', { title }) : t('page.ticket.deleted'));
			await goto(to.workshop(projectId, 'tickets'));
		} catch (e) {
			error = errorMessage(e);
		} finally {
			deleting = false;
		}
	}

	const crumbs = $derived([
		{ label: t('nav.desk'), href: to.desk() },
		{ label: fleet.projectName(projectId), href: to.workshop(projectId) },
		{ label: t('page.tickets.title'), href: to.tickets({ project: projectId }) },
		{ label: key, mono: true }
	]);
</script>

<svelte:head><title>{ticket?.title ?? t('page.ticket.fallbackTitle')}</title></svelte:head>

{#if ticket}
	<PageHeader {crumbs} title={ticket.title}>
		{#snippet chip()}
			<TicketStatusChip status={ticket!.status} done={ticket!.done} size="md" />
			{#if runs.length}<span title={t('page.ticket.latestRun')}
					><StatusChip state={runs[0].state} archived={!!runs[0].archivedAt} suffix /></span
				>{/if}
		{/snippet}
		{#snippet meta()}
			<ChangeTypeTag type={ticket!.changeType} sourceType={ticket!.type} />
			<span class="src">
				<span class="tag">{ticket!.key}</span>
				{#if ticket!.url}
					<a href={ticket!.url} target="_blank" rel="noreferrer"
						>{t('tickets.openIn', { name: ticket!.source.name })} {I.external}</a
					>
				{:else}<span>{ticket!.source.name}</span>{/if}
			</span>
			<WorkshopName {projectId} link />
			{#if ticket!.assignee}<span>{ticket!.assignee}</span>{/if}
		{/snippet}
		{#snippet actions()}
			{#if ticket!.internal}
				<div class="row nowrap">
					<button disabled={!canWrite} title={deny} onclick={() => (editing = !editing)}
						>{editing ? t('page.ticket.cancelEdit') : t('page.ticket.edit')}</button
					>
					<button
						class="btn-danger"
						aria-busy={deleting}
						disabled={!canWrite}
						title={deny}
						onclick={del}>{t('page.ticket.delete')}</button
					>
				</div>
			{/if}
		{/snippet}
	</PageHeader>

	<div class="page glass">
		{#if error}<ErrorCard cause={error} ondismiss={() => (error = '')} />{/if}

		{#snippet runList()}
			<section class="runs">
				<SectionHeader title={t('page.runs.title')} count={runs.length}>
					{#snippet actions()}<Updated at={fleet.updatedAt} paused={fleet.paused} />{/snippet}
				</SectionHeader>
				{#if runs.length === 0}
					<EmptyState compact title={t('page.runs.shelf')} text={t('page.ticket.noRuns')} />
				{:else}
					<div class="pane">
						<RunTable
							{runs}
							cols={['id', 'state', 'ws', 'stage', 'branch', 'flow', 'mr', 'updated']}
						/>
					</div>
				{/if}
			</section>
		{/snippet}

		{#if editing && project}
			<TicketForm
				{project}
				existing={ticket}
				oncancel={() => (editing = false)}
				onsaved={(tk) => {
					editing = false;
					changed(tk);
				}}
			/>
			{@render runList()}
		{:else}
			<div class="grid">
				<div class="work">
					<TicketTracker
						{projectId}
						{ticket}
						branchFormat={project?.repo.branchFormat ?? ''}
						readonly={!canWrite}
						onchange={changed}
					/>
					<section class="glass pane">
						<div class="pane-head">
							<h2>{t('page.ticket.goal')}</h2>
							<span class="help">{t('page.ticket.goalHelp')}</span>
						</div>
						<div class="pane-body goal">
							{#if ticket.body}
								<Markdown src={ticket.body} />
							{:else}
								<p class="empty-line">{t('page.ticket.noGoal')}</p>
							{/if}
						</div>
						{#if fileCount}
							<div class="pane-body att">
								<div class="ah">
									<span class="field-label"
										>{t('page.ticket.attachments', { count: fileCount })}</span
									>
									<span class="help">{@html th('page.ticket.attachmentsHelp')}</span>
								</div>
								{#if attachments.attachments.length}
									<FileChips
										items={attachments.attachments.map((a) => ({
											...a,
											kind: 'attachment' as const
										}))}
									/>
								{/if}
								{#if attachments.remote.length}
									<div class="chips" aria-label={t('page.ticket.remoteFiles')}>
										{#each attachments.remote as a (a.filename)}
											<FileChip name={a.filename} size={a.size} />
										{/each}
									</div>
								{/if}
							</div>
						{/if}
					</section>
					<TicketComments {projectId} ticketKey={key} {refreshed} readonly={!canWrite} />
					<TicketHistory {projectId} {ticket} {canWrite} onrestored={changed} />
					{@render runList()}
				</div>

				<StartRunPanel
					{projectId}
					bind:flow
					bind:params={runParams}
					bind:mr={runMR}
					bind:skillsets={runSkillsets}
					bind:busyRun
					busyText={(id) => t('page.ticket.busy', { id })}
					{starting}
					onstart={startRun}
					onerror={(m) => (error = m)}
					idPrefix="tk"
				>
					{#snippet before()}
						<div class="field">
							<label for="run-goal"
								>{t('page.ticket.direction')}
								<span class="muted">· {t('page.ticket.optional')}</span></label
							>
							<textarea
								id="run-goal"
								bind:value={runGoal}
								rows="3"
								placeholder={t('page.ticket.directionPlaceholder')}></textarea>
							<span class="help">{t('page.ticket.directionHelp')}</span>
						</div>
						<AttachFiles bind:files={runFiles} label={t('page.ticket.runFiles')} compact />
					{/snippet}
					{#snippet after()}
						<div class="field">
							<label for="source-branch">{t('page.ticket.sourceBranch')}</label>
							{#if branches.length}
								<Select id="source-branch" mono options={branchOptions} bind:value={sourceBranch} />
							{:else}
								<input
									id="source-branch"
									class="mono"
									disabled
									placeholder={branchesErr
										? t('page.ticket.branchesUnavailable')
										: t('page.ticket.loadingBranches')}
									value={branchesErr ? (project?.repo.defaultBranch ?? '') : ''}
								/>
							{/if}
							<span class="help"
								>{branchesErr
									? t('page.ticket.branchesFailed')
									: t('page.ticket.branchHelp', { star: I.star })}</span
							>
						</div>
					{/snippet}
				</StartRunPanel>
			</div>
		{/if}
	</div>
{:else if loaded}
	<div class="page glass">
		<EmptyState title={t('page.ticket.gone')} text={loadErr || t('page.ticket.goneText')}>
			<a class="btn btn-primary" href={to.workshop(projectId, 'tickets')}
				>{t('page.ticket.allTickets')}</a
			>
		</EmptyState>
	</div>
{:else}
	<PageHeader {crumbs} title={t('page.ticket.fallbackTitle')} />
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
	.goal > :global(.md) {
		max-width: var(--measure);
	}
	.goal .empty-line {
		padding: 0;
		margin: 0;
	}
	.att {
		border-top: 1px solid var(--glass-line);
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.ah {
		display: flex;
		align-items: baseline;
		gap: 10px;
		flex-wrap: wrap;
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
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
