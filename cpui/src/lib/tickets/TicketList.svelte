<script lang="ts">
	// One workshop's tickets, read live from its source a page at a time: a
	// search (sent to the tracker on Enter), open / done / all, a refresh that
	// reads the tracker now rather than the cached copy, and "Load more"
	// while the tracker has another page.
	import type { Snippet } from 'svelte';
	import { untrack } from 'svelte';
	import { api } from '$lib/api';
	import type { TicketDoneFilter, TicketRow } from '$lib/domain';
	import { errorMessage } from '$lib/core/errors';
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import SearchInput from '$lib/ui/SearchInput.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import TicketTable, { type TicketCol } from './TicketTable.svelte';

	let {
		projectId,
		cols,
		filters,
		empty
	}: {
		projectId: string;
		cols?: TicketCol[];
		/** More of the page's filter row (a workshop picker), before the list's own. */
		filters?: Snippet;
		/** What to do about an empty source (write a ticket, open the tracker). */
		empty?: Snippet;
	} = $props();

	let tickets = $state<TicketRow[] | null>(null);
	let next = $state('');
	let error = $state('');
	let busy = $state<'' | 'load' | 'more'>('');
	let q = $state('');
	let applied = $state(''); // the search the list shows
	let done = $state<TicketDoneFilter>('open');
	// A later load wins: an earlier answer landing after it is dropped.
	let seq = 0;

	const doneOptions: SelectOption<TicketDoneFilter>[] = [
		{ value: 'open', label: t('tickets.list.open') },
		{ value: 'done', label: t('tickets.list.done') },
		{ value: 'any', label: t('tickets.list.any') }
	];

	async function load(more = false, refresh = false) {
		const mine = ++seq;
		busy = more ? 'more' : 'load';
		error = '';
		try {
			const page = await api.projects.tickets(projectId, {
				q: applied || undefined,
				done,
				cursor: more ? next : undefined,
				refresh: refresh || undefined
			});
			if (mine !== seq) return;
			tickets = more ? [...(tickets ?? []), ...page.tickets] : page.tickets;
			next = page.next;
		} catch (e) {
			if (mine === seq) error = errorMessage(e);
		} finally {
			if (mine === seq) busy = '';
		}
	}

	// A new workshop, filter or search starts the list over.
	$effect(() => {
		void [projectId, done, applied];
		untrack(() => {
			tickets = null;
			next = '';
			void load();
		});
	});
	// Clearing the box shows everything again without an Enter.
	$effect(() => {
		if (!q.trim()) untrack(() => (applied = ''));
	});
</script>

<div class="filters">
	{@render filters?.()}
	<SearchInput
		bind:value={q}
		placeholder={t('tickets.list.search')}
		label={t('tickets.list.search')}
		onenter={(v) => (applied = v.trim())}
	/>
	<span class="sel">
		<Select label={t('tickets.list.filterDone')} options={doneOptions} bind:value={done} />
	</span>
	<span class="spacer"></span>
	<button
		class="btn-sm"
		title={t('tickets.refreshHelp')}
		disabled={busy !== ''}
		aria-busy={busy === 'load' && tickets !== null}
		onclick={() => load(false, true)}>{I.retry} {t('tickets.refresh')}</button
	>
</div>

{#if error && !tickets}
	<ErrorCard cause={t('tickets.list.loadError')} detail={error} onretry={() => load()} />
{:else if !tickets}
	<Skeleton pane lines={6} />
{:else if tickets.length === 0 && (applied || done !== 'open')}
	<EmptyState compact title={t('tickets.list.noMatch')} text={t('tickets.list.noMatchText')} />
{:else if tickets.length === 0}
	<EmptyState title={t('tickets.list.empty')} text={t('tickets.list.emptyText')}>
		{@render empty?.()}
	</EmptyState>
{:else}
	<section class="pane">
		<TicketTable {projectId} {tickets} {cols} />
	</section>
	{#if error}<ErrorCard
			cause={t('tickets.list.loadError')}
			detail={error}
			ondismiss={() => (error = '')}
		/>{/if}
	{#if next}
		<div class="more">
			<button disabled={busy !== ''} aria-busy={busy === 'more'} onclick={() => load(true)}
				>{t('tickets.list.more')}</button
			>
		</div>
	{/if}
{/if}

<style>
	.sel {
		display: inline-block;
		min-width: 150px;
	}
	.more {
		display: flex;
		justify-content: center;
	}
	@media (max-width: 760px) {
		.sel {
			display: block;
			width: 100%;
		}
	}
</style>
