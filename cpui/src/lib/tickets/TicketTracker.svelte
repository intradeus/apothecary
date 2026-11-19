<script lang="ts">
	// A ticket as its tracker holds it, read live: the status (changed
	// through the tracker, by name — a remote one's list is read-only but its
	// statuses are still reachable), the labels and the links; and the one
	// thing kept here, the base branch.
	import { untrack } from 'svelte';
	import { api } from '$lib/api';
	import { statusOptions, type TicketDetail, type TrackerStatus } from '$lib/domain';
	import { renderBranch } from '$lib/core/branchname';
	import { errorMessage } from '$lib/core/errors';
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import Select from '$lib/ui/Select.svelte';
	import TicketStatusChip from './TicketStatusChip.svelte';

	let {
		projectId,
		ticket,
		branchFormat = '',
		readonly = false,
		onchange
	}: {
		/** The workshop the ticket is read through. */
		projectId: string;
		ticket: TicketDetail;
		/** The workshop's branch format: what an empty base branch becomes. */
		branchFormat?: string;
		/** The caller's role may not write the ticket (ticket:write): status,
		 *  labels and base branch are shown, not changed. */
		readonly?: boolean;
		onchange: (tk: TicketDetail) => void;
	} = $props();

	const deny = $derived(readonly ? t('common.needsPermission') : undefined);
	let statuses = $state<TrackerStatus[]>([]);
	let error = $state('');
	let busy = $state<'' | 'status' | 'labels' | 'branch' | 'refresh'>('');
	let newLabel = $state('');
	// svelte-ignore state_referenced_locally
	let branch = $state(ticket.baseBranch);

	// Reload only when the workshop or its source changes, not on every
	// update of the ticket. The list is read through the workshop: the source
	// is its owner's, which a collaborator does not read.
	const sourceId = $derived(ticket.source.id);
	$effect(() => {
		const id = projectId;
		void sourceId;
		untrack(() => void loadStatuses(id));
	});

	async function loadStatuses(id: string) {
		try {
			statuses = (await api.projects.ticketStatuses(id)).statuses;
		} catch {
			// The select then offers only the current status: changing it is
			// what failed, not the ticket.
			statuses = [];
		}
	}

	const options = $derived(
		statusOptions(statuses, ticket).map((s) => ({
			value: s.name,
			label: s.name,
			detail: s.isDone ? t('tickets.doneMark') : undefined
		}))
	);

	async function run(what: typeof busy, call: () => Promise<TicketDetail>) {
		busy = what;
		error = '';
		try {
			const tk = await call();
			branch = tk.baseBranch;
			onchange(tk);
		} catch (e) {
			error = errorMessage(e);
		} finally {
			busy = '';
		}
	}

	const setStatus = (name: string) =>
		name !== ticket.status &&
		run('status', () => api.tickets.setStatus(projectId, ticket.key, name));

	const setLabels = (labels: string[]) =>
		run('labels', () => api.tickets.setLabels(projectId, ticket.key, labels));

	function addLabel(e: SubmitEvent) {
		e.preventDefault();
		const l = newLabel.trim();
		if (!l || ticket.labels.includes(l)) return;
		if (l.includes(',')) {
			error = t('tickets.labelComma');
			return;
		}
		newLabel = '';
		void setLabels([...ticket.labels, l]);
	}

	// A remote issue takes only the base branch; an internal ticket takes the
	// whole draft, so the rest goes back as it is.
	function saveBranch(e: SubmitEvent) {
		e.preventDefault();
		void run('branch', () =>
			api.tickets.update(projectId, ticket.key, {
				title: ticket.title,
				goal: ticket.body,
				baseBranch: branch.trim(),
				changeType: ticket.changeType,
				typeId: ticket.typeId || undefined,
				attachmentIds: ticket.attachmentIds
			})
		);
	}
</script>

<section class="glass pane tracker" aria-label={t('tickets.tracker')}>
	<div class="pane-body rows">
		<div class="line">
			<span class="field-label">{t('tickets.col.status')}</span>
			<TicketStatusChip status={ticket.status} done={ticket.done} size="md" />
			{#if options.length}
				<span class="sel">
					<Select
						size="sm"
						{options}
						value={ticket.status}
						placeholder={t('tickets.setStatus')}
						label={t('tickets.setStatus')}
						disabled={readonly || busy !== ''}
						title={deny}
						onchange={setStatus}
					/>
				</span>
			{/if}
			<span class="spacer"></span>
			{#if ticket.url}
				<a class="btn btn-sm" href={ticket.url} target="_blank" rel="noreferrer"
					>{t('tickets.openIn', { name: ticket.source.name })} {I.external}</a
				>
			{/if}
			<button
				class="btn-sm"
				title={t('tickets.refreshHelp')}
				disabled={busy !== ''}
				aria-busy={busy === 'refresh'}
				onclick={() =>
					run('refresh', () => api.projects.ticket(projectId, ticket.key, { refresh: true }))}
				>{I.retry} {t('tickets.refresh')}</button
			>
		</div>

		<div class="line">
			<span class="field-label">{t('tickets.labels')}</span>
			{#each ticket.labels as l (l)}
				<span class="pill sm flat tint-muted label">
					{l}
					<button
						class="btn-icon bare"
						aria-label={t('tickets.removeLabel', { label: l })}
						disabled={readonly || busy !== ''}
						title={deny}
						onclick={() => setLabels(ticket.labels.filter((x) => x !== l))}>{I.close}</button
					>
				</span>
			{:else}
				<span class="faint small">{t('tickets.noLabels')}</span>
			{/each}
			<form class="add" onsubmit={addLabel}>
				<input
					class="small"
					bind:value={newLabel}
					placeholder={t('tickets.addLabel')}
					aria-label={t('tickets.addLabel')}
					disabled={readonly || busy !== ''}
					title={deny}
				/>
			</form>
		</div>

		<form class="line" onsubmit={saveBranch}>
			<label class="field-label" for="tk-branch">{t('tickets.baseBranch')}</label>
			<input
				id="tk-branch"
				class="mono small branch"
				bind:value={branch}
				placeholder={renderBranch(
					branchFormat,
					ticket.changeType,
					ticket.internal ? '' : ticket.key,
					ticket.title
				)}
				disabled={readonly || busy !== ''}
				title={deny}
			/>
			{#if branch.trim() !== ticket.baseBranch}
				<button class="btn-sm" type="submit" aria-busy={busy === 'branch'} disabled={busy !== ''}
					>{t('tickets.saveBranch')}</button
				>
			{/if}
			<span class="help">{t('tickets.branchHelp')}</span>
		</form>

		{#if ticket.links.length}
			<div class="line">
				<span class="field-label">{t('tickets.links')}</span>
				<ul class="links">
					{#each ticket.links as l (l.url)}
						<li>
							<a href={l.url} target="_blank" rel="noreferrer">{l.title || l.url} {I.external}</a>
						</li>
					{/each}
				</ul>
			</div>
		{/if}
		{#if error}<div class="error">{error}</div>{/if}
	</div>
</section>

<style>
	.rows {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.line {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 8px;
		min-width: 0;
	}
	.line > .field-label {
		min-width: 64px;
		margin: 0;
	}
	.sel {
		min-width: 160px;
	}
	.label {
		display: inline-flex;
		align-items: center;
		gap: 4px;
	}
	.add input {
		width: 14ch;
	}
	.branch {
		flex: 1;
		min-width: 18ch;
	}
	.links {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-wrap: wrap;
		gap: 4px 14px;
	}
</style>
