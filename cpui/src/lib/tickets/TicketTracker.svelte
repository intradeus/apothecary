<script lang="ts">
	// A ticket as its tracker holds it: the status (changed through the
	// tracker — the remote issue, or the user's internal tracker), the labels,
	// the links, and how fresh the mirror is.
	import { untrack } from 'svelte';
	import { api } from '$lib/api';
	import {
		statusOptions,
		trackerIdOf,
		type Ticket,
		type TicketStatusRef,
		type TrackerStatus
	} from '$lib/domain';
	import { errorMessage } from '$lib/core/errors';
	import { ago, fmtAbs } from '$lib/core/format';
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import Select from '$lib/ui/Select.svelte';
	import TicketStatusChip from './TicketStatusChip.svelte';

	let { ticket, onchange }: { ticket: Ticket; onchange: (tk: Ticket) => void } = $props();

	let statuses = $state<TrackerStatus[]>([]);
	let error = $state('');
	let busy = $state<'' | 'status' | 'labels' | 'sync'>('');
	let newLabel = $state('');

	// Reload only when the tracker changes, not on every update of the ticket.
	const trackerKey = $derived(ticket.source?.integrationId ?? '');
	$effect(() => {
		void trackerKey;
		untrack(() => void loadStatuses());
	});

	async function loadStatuses() {
		try {
			const id = trackerIdOf(ticket, ticket.source ? [] : await api.integrations.list());
			statuses = id ? (await api.integrations.statuses(id)).statuses : [];
		} catch {
			// The select then offers only the current status: changing it is
			// what failed, not the ticket.
			statuses = [];
		}
	}

	const options = $derived(
		statusOptions(statuses, ticket.status).map((s: TicketStatusRef) => ({
			value: s.id,
			label: s.name,
			detail: s.isDone ? t('tickets.doneMark') : undefined
		}))
	);

	async function run(what: 'status' | 'labels' | 'sync', call: () => Promise<Ticket>) {
		busy = what;
		error = '';
		try {
			onchange(await call());
		} catch (e) {
			error = errorMessage(e);
		} finally {
			busy = '';
		}
	}

	const setStatus = (statusId: string) =>
		statusId !== ticket.status?.id &&
		run('status', () => api.tickets.setStatus(ticket.id, statusId));

	const setLabels = (labels: string[]) =>
		run('labels', () => api.tickets.setLabels(ticket.id, labels));

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
</script>

<section class="glass pane tracker" aria-label={t('tickets.tracker')}>
	{#if ticket.syncState === 'gone'}
		<div class="banner tint-danger" role="alert">
			{t('tickets.gone', { kind: ticket.source?.kind ?? '' })}
		</div>
	{:else if ticket.syncState === 'error'}
		<div class="banner tint-warning" role="alert">
			{t('tickets.syncFailed', { error: ticket.syncError ?? '' })}
		</div>
	{/if}
	<div class="pane-body rows">
		<div class="line">
			<span class="field-label">{t('tickets.col.status')}</span>
			<TicketStatusChip status={ticket.status} size="md" />
			{#if options.length}
				<span class="sel">
					<Select
						size="sm"
						{options}
						value={ticket.status?.id}
						placeholder={t('tickets.setStatus')}
						label={t('tickets.setStatus')}
						disabled={busy !== ''}
						onchange={setStatus}
					/>
				</span>
			{/if}
			<span class="spacer"></span>
			{#if ticket.source}
				<span class="muted small" title={ticket.syncedAt ? fmtAbs(ticket.syncedAt) : undefined}
					>{ticket.syncedAt
						? t('tickets.syncedAgo', { ago: ago(ticket.syncedAt) })
						: t('tickets.neverSynced')}</span
				>
			{/if}
			<button
				class="btn-sm"
				disabled={busy !== ''}
				aria-busy={busy === 'sync'}
				onclick={() => run('sync', () => api.tickets.sync(ticket.id))}
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
						disabled={busy !== ''}
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
					disabled={busy !== ''}
				/>
			</form>
		</div>

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
	.links {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-wrap: wrap;
		gap: 4px 14px;
	}
	.banner {
		padding: 8px 14px;
		border-bottom-width: 1px;
		border-bottom-style: solid;
	}
</style>
