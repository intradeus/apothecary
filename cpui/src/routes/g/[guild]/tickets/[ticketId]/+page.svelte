<!-- Ticket detail (DESIGN 7.6): the goal and its attachments on the left,
     "Start a run" on the right, the ticket's runs below (shared 4s poll). -->
<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { currentGuild, to } from '$lib/guild';
	import { api } from '$lib/api';
	import { fleet } from '$lib/fleet.svelte';
	import { ago, fmtAbs, shortId } from '$lib/format';
	import type { Attachment, Branch, Ticket } from '$lib/domain';
	import FlowSelect from '$lib/FlowSelect.svelte';
	import TicketForm from '$lib/TicketForm.svelte';
	import FileChips from '$lib/FileChips.svelte';
	import AttachFiles from '$lib/AttachFiles.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import SectionHeader from '$lib/ui/SectionHeader.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Updated from '$lib/ui/Updated.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';
	import RunTable from '$lib/run/RunTable.svelte';
	import WorkshopName from '$lib/workshop/WorkshopName.svelte';

	const ticketId = $derived(page.params.ticketId ?? '');

	let ticket = $state<Ticket | null>(null);
	let loaded = $state(false);
	let loadErr = $state('');
	let error = $state('');
	let editing = $state(false);

	let flow = $state(''); // per-run flow; empty = project default
	let sourceBranch = $state(''); // branch to fork this run off from; '' until branches load
	let branches = $state<Branch[]>([]);
	let branchesErr = $state(''); // forge unreachable ⇒ fall back to project default
	let runGoal = $state(''); // direction for THIS attempt (additive to the ticket goal)
	let runFiles = $state<File[]>([]);
	let starting = $state(false);

	const project = $derived(ticket ? fleet.projectById.get(ticket.projectId) : undefined);

	// The ticket's runs, newest first, from the shared fleet poll (4s).
	const runs = $derived(fleet.runsByTicket.get(ticketId) ?? []);

	// The ticket's attachments — hand-uploaded or copied from a tracker. They
	// are delivered into the workspace of EVERY run of this ticket, so they
	// belong on the ticket page, not on a run's.
	let attachments = $state<Attachment[]>([]);

	const loadAttachments = async (id: string) => {
		try {
			attachments = await api.tickets.attachments(id);
		} catch {
			attachments = [];
		}
	};

	// Fetch the repo's branches for the source-branch picker; pre-select the
	// default branch. On failure the picker shows the project default and the
	// run starts there (source_branch left empty).
	const loadBranches = async (projectId: string) => {
		try {
			branches = await api.projects.branches(projectId);
			const def = branches.find((b) => b.isDefault) ?? branches[0];
			if (def && !sourceBranch) sourceBranch = def.name;
		} catch (e) {
			branchesErr = e instanceof Error ? e.message : String(e);
		}
	};

	$effect(() => {
		const id = ticketId;
		ticket = null;
		loaded = false;
		loadErr = '';
		editing = false;
		branches = [];
		sourceBranch = '';
		api.tickets
			.get(id)
			.then((t) => {
				if (id !== ticketId) return;
				ticket = t;
				loadBranches(t.projectId);
				loadAttachments(t.id);
			})
			.catch((e) => {
				if (id === ticketId) loadErr = e instanceof Error ? e.message : String(e);
			})
			.finally(() => {
				if (id === ticketId) loaded = true;
			});
	});

	async function startRun() {
		if (starting) return;
		starting = true;
		error = '';
		try {
			let attachmentIds: string[] | undefined;
			if (runFiles.length) attachmentIds = (await api.files.upload(runFiles)).map((a) => a.id);
			const runId = await api.tickets.startRun(ticketId, {
				flow: flow.trim() || undefined,
				goal: runGoal.trim() || undefined,
				sourceBranch: sourceBranch.trim() || undefined,
				attachmentIds
			});
			void fleet.refresh();
			toast(`Run ${shortId(runId)} started`, {
				action: { label: 'Open run', run: () => void goto(to.run(runId)) }
			});
			await goto(to.run(runId));
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			starting = false;
		}
	}

	let deleting = $state(false);
	async function del() {
		if (
			!(await confirmDialog({
				title: 'Delete this ticket?',
				body: `The <b>ticket</b> is deleted. Its <b>runs</b> keep their history, and their <b>workspaces</b> are untouched.`,
				action: 'Delete ticket'
			}))
		)
			return;
		deleting = true;
		try {
			const title = ticket?.title;
			await api.tickets.remove(ticketId);
			void fleet.refresh();
			toast(title ? `Ticket “${title}” deleted` : 'Ticket deleted');
			await goto(to.tickets());
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			deleting = false;
		}
	}

	const crumbs = $derived([
		{ label: currentGuild().name, href: to.desk() },
		...(ticket
			? [
					{
						label: project?.name ?? ticket.projectId.slice(-6),
						href: to.workshop(ticket.projectId)
					}
				]
			: []),
		{ label: 'Tickets', href: to.tickets() },
		{ label: ticket?.source?.key ?? `t_${ticketId.slice(-4).toLowerCase()}`, mono: true }
	]);
</script>

<svelte:head><title>{ticket?.title ?? 'Ticket'}</title></svelte:head>

{#if ticket}
	<PageHeader {crumbs} title={ticket.title}>
		{#snippet chip()}
			{#if runs.length}<StatusChip state={runs[0].state} suffix />{/if}
		{/snippet}
		{#snippet meta()}
			{#if ticket!.source?.key}
				<!-- Provenance: a copy taken once. Nothing here is written back. -->
				<span class="src">
					<span class="tag">{ticket!.source.key}</span>
					{#if ticket!.source.url}
						<a href={ticket!.source.url} target="_blank" rel="noreferrer"
							>{ticket!.source.kind} {I.external}</a
						>
					{:else}<span>{ticket!.source.kind}</span>{/if}
				</span>
			{/if}
			<WorkshopName projectId={ticket!.projectId} link />
			{#if ticket!.baseBranch}<span class="mono" title="Base branch — each run appends a suffix"
					>{I.branch} {ticket!.baseBranch}</span
				>{/if}
			<span title={fmtAbs(ticket!.createdAt)}>created {ago(ticket!.createdAt)}</span>
		{/snippet}
		{#snippet actions()}
			<div class="row nowrap">
				<button onclick={() => (editing = !editing)}>{editing ? 'Cancel edit' : 'Edit'}</button>
				<button class="btn-danger" aria-busy={deleting} onclick={del}>Delete…</button>
			</div>
		{/snippet}
	</PageHeader>

	<div class="page body">
		{#if error}<ErrorCard cause={error} ondismiss={() => (error = '')} />{/if}

		{#if editing}
			<div class="edit">
				<TicketForm
					projects={fleet.projects}
					existing={ticket}
					oncancel={() => (editing = false)}
					onsaved={(t) => {
						ticket = t;
						editing = false;
						loadAttachments(t.id);
					}}
				/>
			</div>
		{:else}
			<div class="grid">
				<section class="c pane">
					<div class="pane-head">
						<h2>The goal</h2>
						<span class="help">every run of this ticket works toward it</span>
					</div>
					<div class="pane-body goal">
						{#if ticket.goal}
							<Markdown src={ticket.goal} />
						{:else}
							<p class="empty-line">No goal written.</p>
						{/if}
					</div>
					{#if attachments.length}
						<div class="pane-body att">
							<div class="ah">
								<span class="flabel">Attachments · {attachments.length}</span>
								<span class="help"
									>delivered into every run's workspace under <code>.apothecary/attachments/</code
									></span
								>
							</div>
							<FileChips items={attachments.map((a) => ({ ...a, kind: 'attachment' as const }))} />
						</div>
					{/if}
				</section>

				<aside class="start lg pane">
					<div class="pane-head"><h2>Start a run</h2></div>
					<div class="pane-body sb">
						<div class="field">
							<label for="run-goal">Direction <span class="mut">· optional</span></label>
							<textarea
								id="run-goal"
								bind:value={runGoal}
								rows="3"
								placeholder="e.g. Last attempt broke the router — use the existing middleware."
							></textarea>
							<span class="help"
								>Added to the ticket's goal for this run only; never replaces it.</span
							>
						</div>
						<AttachFiles bind:files={runFiles} label="Files for this run only" compact />
						<div class="field">
							<label for="flow">Flow</label>
							<FlowSelect
								id="flow"
								projectId={ticket.projectId}
								bind:value={flow}
								fallbackLabel={`Default · ${project?.defaultFlow || 'project default'}`}
							/>
						</div>
						<div class="field">
							<label for="source-branch">Source branch</label>
							{#if branches.length}
								<select id="source-branch" bind:value={sourceBranch} class="mono">
									{#each branches as b (b.name)}
										<option value={b.name}>{b.isDefault ? `${I.star} ${b.name}` : b.name}</option>
									{/each}
								</select>
							{:else}
								<input
									id="source-branch"
									class="mono"
									disabled
									placeholder={branchesErr ? 'branches unavailable' : 'loading branches…'}
									value={branchesErr ? (project?.repo.defaultBranch ?? '') : ''}
								/>
							{/if}
							<span class="help"
								>{branchesErr
									? 'Branches could not be listed — the run forks off the default branch.'
									: `The run forks its branch off this one. ${I.star} = the repo default.`}</span
							>
						</div>
						<button class="btn-primary btn-lg go" onclick={startRun} aria-busy={starting}>
							{I.play} Start run
						</button>
					</div>
				</aside>
			</div>
		{/if}

		<section class="runs">
			<SectionHeader title="Runs" count={runs.length}>
				{#snippet actions()}<Updated at={fleet.updatedAt} paused={fleet.paused} />{/snippet}
			</SectionHeader>
			{#if runs.length === 0}
				<EmptyState
					compact
					title="All still on the shelf"
					text="No runs of this ticket yet. Start one above."
				/>
			{:else}
				<div class="c pane">
					<RunTable
						{runs}
						cols={['id', 'state', 'ws', 'stage', 'branch', 'flow', 'mr', 'updated']}
					/>
				</div>
			{/if}
		</section>
	</div>
{:else if loaded}
	<div class="page">
		<EmptyState title="No such ticket" text={loadErr || 'It may have been deleted.'}>
			<a class="btn btn-primary" href={to.tickets()}>All tickets</a>
		</EmptyState>
	</div>
{:else}
	<PageHeader {crumbs} title="Ticket" />
	<div class="page"><Skeleton pane lines={8} /></div>
{/if}

<style>
	.body {
		padding-top: 2px;
	}
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
	.start {
		border-radius: var(--r-group);
	}
	.start .pane-head {
		border-bottom-color: var(--lgLn);
	}
	.goal > :global(.md) {
		max-width: var(--measure);
	}
	.goal .empty-line {
		padding: 0;
		margin: 0;
	}
	.att {
		border-top: 1px solid var(--cLn);
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
	.flabel {
		font-size: var(--fs-label);
		font-weight: 500;
	}
	.sb {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.go {
		align-self: stretch;
		margin-top: 2px;
	}
	.runs {
		display: flex;
		flex-direction: column;
		gap: 10px;
		flex: none;
	}
	.edit {
		max-width: 900px;
	}
	@media (max-width: 1000px) {
		.grid {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
