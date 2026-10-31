<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// Workshop tickets tab (DESIGN 7.6 list, filtered to this workshop). The
	// status is derived: the latest run's state, or "no runs".
	import type { Project, Ticket } from '$lib/domain';
	import { fleet } from '$lib/fleet.svelte';
	import { to } from '$lib/urls';
	import { stateTab } from '$lib/format';
	import TicketTable from '$lib/tickets/TicketTable.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import HeaderAction from '$lib/ui/HeaderAction.svelte';
	import SearchInput from '$lib/ui/SearchInput.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import { I } from '$lib/ui/icons';

	let {
		project,
		tickets,
		error,
		onretry
	}: { project: Project; tickets: Ticket[] | null; error: string; onretry: () => void } = $props();

	let q = $state('');
	let activeOnly = $state(false);

	const hasActive = (tk: Ticket) =>
		(fleet.runsByTicket.get(tk.id) ?? []).some((r) => stateTab[r.state] === 'active');

	const shown = $derived(
		(tickets ?? [])
			.filter((tk) => {
				const s = q.trim().toLowerCase();
				if (
					s &&
					!tk.title.toLowerCase().includes(s) &&
					!(tk.source?.key ?? '').toLowerCase().includes(s) &&
					!tk.goal.toLowerCase().includes(s)
				)
					return false;
				return !activeOnly || hasActive(tk);
			})
			.sort((a, b) => b.createdAt.localeCompare(a.createdAt))
	);
</script>

<HeaderAction>
	<a class="btn btn-primary btn-lg" href={to.newTicket({ project: project.id })}
		>{I.add} {t('workshop.tickets.new')}</a
	>
</HeaderAction>

<div class="filters">
	<SearchInput bind:value={q} placeholder={t('workshop.tickets.search')} />
	<Toggle bind:checked={activeOnly} label={t('page.tickets.hasActive')} />
	<span class="spacer"></span>
	<a class="btn btn-sm" href={to.tickets({ project: project.id })}
		>{t('workshop.tickets.openIn')} {I.forward}</a
	>
</div>

{#if error && !tickets}
	<ErrorCard cause={t('workshop.tickets.loadError')} detail={error} {onretry} />
{:else if !tickets}
	<Skeleton pane lines={4} />
{:else if shown.length}
	<section class="pane">
		<TicketTable tickets={shown} cols={['title', 'type', 'source', 'branch', 'runs', 'created']} />
	</section>
{:else if q || activeOnly}
	<EmptyState
		compact
		title={t('workshop.tickets.noMatchTitle')}
		text={t('workshop.tickets.noMatchText')}
	/>
{:else}
	<EmptyState title={t('workshop.tickets.emptyTitle')} text={t('workshop.tickets.emptyText')}>
		<a class="btn btn-primary" href={to.newTicket({ project: project.id })}
			>{I.add} {t('workshop.tickets.new')}</a
		>
	</EmptyState>
{/if}
