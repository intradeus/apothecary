<!-- A ticket by its record id (where runs link): the page is a workshop's,
     so this hands over to it — the workshop the link names (?workshop=)
     when the ticket is on its source, else the first one that is. A record
     no workshop reaches any more (its source switched away or gone) is
     shown as last seen. -->
<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api } from '$lib/api';
	import { errorMessage } from '$lib/core/errors';
	import { ago, fmtAbs } from '$lib/core/format';
	import { to } from '$lib/core/urls';
	import type { TicketRecord } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';

	const ticketId = $derived(page.params.ticketId ?? '');
	let record = $state<TicketRecord | null>(null);
	let loadErr = $state('');

	$effect(() => {
		const id = ticketId;
		const hint = page.url.searchParams.get('workshop') ?? '';
		api.tickets
			.get(id)
			.then((r) => {
				const ws = r.workshops.find((w) => w.id === hint) ?? r.workshops[0];
				if (ws) return goto(to.workshopTicket(ws.id, r.key), { replaceState: true });
				record = r;
			})
			.catch((e) => (loadErr = errorMessage(e)));
	});

	const crumbs = $derived([
		{ label: t('nav.desk'), href: to.desk() },
		{ label: t('page.tickets.title'), href: to.tickets() },
		{ label: record?.key || `t_${ticketId.slice(-4).toLowerCase()}`, mono: true }
	]);
</script>

<svelte:head><title>{record?.ticket.title ?? t('page.ticket.fallbackTitle')}</title></svelte:head>

{#if record}
	{@const tk = record.ticket}
	<PageHeader {crumbs} title={tk.title}>
		{#snippet meta()}
			<span class="tag">{record!.key}</span>
			{#if tk.sourceUrl}
				<a href={tk.sourceUrl} target="_blank" rel="noreferrer"
					>{record!.source?.name ?? tk.sourceKind} {I.external}</a
				>
			{/if}
			<span title={fmtAbs(tk.updatedAt)}
				>{t('page.ticket.lastSeen', { ago: ago(tk.updatedAt) })}</span
			>
		{/snippet}
	</PageHeader>
	<div class="page glass">
		<EmptyState title={t('page.ticket.orphan')} text={t('page.ticket.orphanText')}>
			<a class="btn btn-primary" href={to.tickets()}>{t('page.ticket.allTickets')}</a>
		</EmptyState>
	</div>
{:else if loadErr}
	<div class="page glass">
		<EmptyState title={t('page.ticket.gone')} text={loadErr}>
			<a class="btn btn-primary" href={to.tickets()}>{t('page.ticket.allTickets')}</a>
		</EmptyState>
	</div>
{:else}
	<PageHeader {crumbs} title={t('page.ticket.fallbackTitle')} />
	<div class="page glass"><Skeleton pane lines={8} /></div>
{/if}
