<!-- A ticket's history: its versions newest first (who, when, what changed),
     and the one selected against the ticket now — title and goal as a line
     diff, the other fields as before/after rows — with "Restore this
     version". Only the read view shows it: a restore never meets unsaved
     edits. Lives in workshop, not tickets, because it reuses the run
     feature's line diff. Reloads whenever the page's ticket changes. -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { session } from '$lib/app/session.svelte';
	import { errorMessage } from '$lib/core/errors';
	import { ago, fmtAbs, shortId } from '$lib/core/format';
	import { to } from '$lib/core/urls';
	import {
		ticketChanges,
		type TicketDetail,
		type TicketField,
		type TicketVersion,
		type TicketVersionSummary,
		type TrackerType,
		type VersionAuthor
	} from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import { editLines } from '$lib/run/diff';
	import CodeView from '$lib/ui/CodeView.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { toast } from '$lib/ui/toast.svelte';

	let {
		projectId,
		ticket,
		canWrite,
		onrestored
	}: {
		projectId: string;
		ticket: TicketDetail;
		canWrite: boolean;
		onrestored: (tk: TicketDetail) => void;
	} = $props();

	let versions = $state<TicketVersionSummary[] | null>(null);
	let loadErr = $state('');
	let selected = $state<TicketVersion | null>(null);
	let selectedSeq = $state(0);
	let versionErr = $state('');
	let restoreErr = $state('');
	let restoring = $state(false);
	/** The source's types, to name a version's type; ids when unreadable. */
	let types = $state<TrackerType[]>([]);

	async function load() {
		loadErr = '';
		try {
			versions = await api.tickets.versions(projectId, ticket.key);
		} catch (e) {
			loadErr = errorMessage(e);
		}
	}

	// The ticket prop is replaced on every save, restore and tracker change:
	// each may have added a version.
	$effect(() => {
		void ticket;
		void load();
	});

	onMount(() => {
		api.projects
			.ticketTypes(projectId)
			.then((l) => (types = l))
			.catch(() => {
				/* a type reads by its id */
			});
	});

	// Only the last pick's answer is shown: an earlier, slower one would show a
	// version other than the one Restore posts.
	async function pick(seq: number) {
		selectedSeq = seq;
		selected = null;
		versionErr = '';
		restoreErr = '';
		try {
			const v = await api.tickets.version(projectId, ticket.key, seq);
			if (selectedSeq === seq) selected = v;
		} catch (e) {
			if (selectedSeq === seq) versionErr = errorMessage(e);
		}
	}

	const changes = $derived(
		selected
			? ticketChanges(selected, ticket, (id) => types.find((y) => y.id === id)?.name ?? id)
			: []
	);
	const isNewest = $derived(versions?.[0]?.seq === selectedSeq);
	const tracker = $derived(ticket.source.name || ticket.source.kind);
	const blocked = $derived(
		!ticket.internal
			? t('tickets.history.remote', { tracker })
			: !canWrite
				? t('common.needsPermission')
				: undefined
	);

	function who(a: VersionAuthor): string {
		if (a.id && a.id === session.user?.id) return t('tickets.history.you');
		return a.handle
			? t('tickets.history.person', { handle: a.handle })
			: t('tickets.history.someone');
	}

	const fieldLabel = (f: string) =>
		(
			({
				title: t('tickets.history.field.title'),
				goal: t('tickets.history.field.goal'),
				change_type: t('tickets.history.field.change_type'),
				type: t('tickets.history.field.type'),
				labels: t('tickets.history.field.labels'),
				base_branch: t('tickets.history.field.base_branch')
			}) as Record<string, string>
		)[f] ?? f;

	const TEXT_FIELDS: TicketField[] = ['title', 'goal'];

	async function restore() {
		const seq = selectedSeq;
		if (
			!(await confirmDialog({
				title: t('tickets.history.restoreTitle', { seq }),
				body: t('tickets.history.restoreBody'),
				action: t('tickets.history.restoreAction'),
				tone: 'warn'
			}))
		)
			return;
		restoring = true;
		restoreErr = '';
		try {
			const tk = await api.tickets.restore(projectId, ticket.key, seq);
			toast(t('tickets.history.restored', { seq }));
			selected = null;
			selectedSeq = 0;
			onrestored(tk);
		} catch (e) {
			restoreErr = errorMessage(e);
		} finally {
			restoring = false;
		}
	}
</script>

<section class="glass pane" aria-label={t('tickets.history.title')}>
	<div class="pane-head">
		<h2>{t('tickets.history.title')}</h2>
		<span class="help">{t('tickets.history.help')}</span>
	</div>
	<div class="pane-body hist">
		{#if loadErr}
			<ErrorCard cause={t('tickets.history.loadError')} detail={loadErr} onretry={load} />
		{:else if versions === null}
			<Skeleton lines={3} />
		{:else if versions.length === 0}
			<EmptyState
				compact
				title={t('tickets.history.empty')}
				text={t('tickets.history.emptyText')}
			/>
		{:else}
			<ul class="versions" aria-label={t('tickets.history.title')}>
				{#each versions as v (v.seq)}
					<li>
						<button
							class="ver"
							class:on={v.seq === selectedSeq}
							aria-pressed={v.seq === selectedSeq}
							onclick={() => pick(v.seq)}
						>
							<b class="mono">{t('tickets.history.seq', { seq: v.seq })}</b>
							<span class="who">
								{#if v.author.kind === 'assistant'}
									{t('tickets.history.assistant', { who: who(v.author) })}
								{:else if v.author.kind === 'run'}
									{t('tickets.history.run')}
								{:else if v.author.kind === 'sync'}
									{t('tickets.history.sync')}
								{:else if v.author.kind === 'backfill'}
									{t('tickets.history.backfill')}
								{:else}
									{who(v.author)}
								{/if}
							</span>
							<span class="muted small" title={fmtAbs(v.createdAt)}>{ago(v.createdAt)}</span>
							{#each v.changed as f (f)}
								<span class="pill sm flat">{fieldLabel(f)}</span>
							{/each}
							{#if v.restoredFrom}
								<span class="pill sm flat"
									>{t('tickets.history.restoredFrom', { seq: v.restoredFrom })}</span
								>
							{/if}
							{#if v.oldest}<span class="faint small">{t('tickets.history.firstKept')}</span>{/if}
						</button>
						{#if v.author.kind === 'run'}
							<a class="mono small" href={to.run(v.author.id)}>{shortId(v.author.id)}</a>
						{/if}
					</li>
				{/each}
			</ul>

			{#if versionErr}
				<ErrorCard
					cause={t('tickets.history.versionError')}
					detail={versionErr}
					onretry={() => pick(selectedSeq)}
				/>
			{:else if selectedSeq && !selected}
				<Skeleton lines={4} />
			{:else if selected}
				<div class="cmp" aria-label={t('tickets.history.compare', { seq: selected.seq })}>
					<h3>{t('tickets.history.compare', { seq: selected.seq })}</h3>
					{#if isNewest}
						<p class="muted">{t('tickets.history.current')}</p>
					{:else if changes.length === 0}
						<p class="muted">{t('tickets.history.same')}</p>
					{/if}
					{#each changes as c (c.field)}
						<div class="change">
							<span class="field-label">{fieldLabel(c.field)}</span>
							{#if TEXT_FIELDS.includes(c.field)}
								<CodeView lines={editLines({ oldText: c.before, newText: c.after })} />
							{:else}
								<dl class="rows">
									<dt>{t('tickets.history.now')}</dt>
									<dd class="mono">{c.before || t('tickets.history.none')}</dd>
									<dt>{t('tickets.history.then', { seq: selected.seq })}</dt>
									<dd class="mono">{c.after || t('tickets.history.none')}</dd>
								</dl>
							{/if}
						</div>
					{/each}
					{#if restoreErr}
						<ErrorCard
							cause={t('tickets.history.restoreError', { seq: selected.seq })}
							detail={restoreErr}
							ondismiss={() => (restoreErr = '')}
						/>
					{/if}
					{#if !isNewest}
						<div class="acts">
							<button
								disabled={!!blocked || restoring}
								aria-busy={restoring}
								title={blocked}
								onclick={restore}>{t('tickets.history.restore')}</button
							>
							{#if blocked}<span class="help">{blocked}</span>{/if}
						</div>
					{/if}
				</div>
			{/if}
		{/if}
	</div>
</section>

<style>
	.hist {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.versions {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.versions li {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	/* A button: the global one centres its content, a version reads from the
	   left like the rows around it. */
	.ver {
		flex: 1;
		display: flex;
		align-items: center;
		justify-content: flex-start;
		flex-wrap: wrap;
		gap: 8px;
		text-align: left;
		background: none;
		border: 1px solid transparent;
	}
	.ver.on {
		border-color: var(--line-strong);
	}
	.cmp,
	.change {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	h3,
	p {
		margin: 0;
	}
	.rows {
		display: grid;
		grid-template-columns: auto 1fr;
		gap: 2px 12px;
		margin: 0;
	}
	.rows dd {
		margin: 0;
	}
	.acts {
		display: flex;
		align-items: center;
		gap: 8px;
	}
</style>
