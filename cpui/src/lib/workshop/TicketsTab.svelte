<script lang="ts">
	// Workshop tickets tab (DESIGN 7.6 list, filtered to this workshop). The
	// status is derived: the latest run's state, or "no runs".
	import type { Project, Ticket } from '$lib/domain';
	import { fleet } from '$lib/fleet.svelte';
	import { to } from '$lib/guild';
	import { ago, fmtAbs, stateTab } from '$lib/format';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';

	let {
		project,
		tickets,
		error,
		onretry
	}: { project: Project; tickets: Ticket[] | null; error: string; onretry: () => void } = $props();

	let q = $state('');
	let filter = $state<'all' | 'active' | 'idle'>('all');

	const runsOf = $derived.by(() => {
		const m = new Map<string, typeof fleet.runs>();
		for (const r of fleet.runs) {
			if (!r.ticketId) continue;
			m.set(r.ticketId, [...(m.get(r.ticketId) ?? []), r]);
		}
		for (const l of m.values()) l.sort((a, b) => b.createdAt.localeCompare(a.createdAt));
		return m;
	});
	const hasActive = (t: Ticket) =>
		(runsOf.get(t.id) ?? []).some((r) => stateTab[r.state] === 'active');

	const shown = $derived(
		(tickets ?? [])
			.filter((t) => {
				const s = q.trim().toLowerCase();
				if (
					s &&
					!t.title.toLowerCase().includes(s) &&
					!(t.source?.key ?? '').toLowerCase().includes(s) &&
					!t.goal.toLowerCase().includes(s)
				)
					return false;
				if (filter === 'active') return hasActive(t);
				if (filter === 'idle') return !hasActive(t);
				return true;
			})
			.sort((a, b) => b.createdAt.localeCompare(a.createdAt))
	);
	const activeN = $derived((tickets ?? []).filter(hasActive).length);
</script>

<div class="bar">
	<Segmented
		options={[
			{ value: 'all', label: 'All', count: tickets?.length ?? '…' },
			{ value: 'active', label: 'Running', count: activeN },
			{ value: 'idle', label: 'Idle', count: (tickets?.length ?? 0) - activeN }
		]}
		bind:value={filter}
	/>
	<input class="q" type="search" placeholder="⌕  Search title, key, goal" bind:value={q} />
	<span class="spacer"></span>
	<a class="btn btn-sm" href={to.tickets({ project: project.id })}>Open in Tickets →</a>
	<a class="btn btn-primary" href={to.newTicket({ project: project.id })}>+ New ticket</a>
</div>

{#if error && !tickets}
	<ErrorCard cause="Couldn’t load tickets" detail={error} {onretry} />
{:else if !tickets}
	<div class="c pane"><div class="pane-body"><Skeleton lines={4} /></div></div>
{:else if shown.length}
	<section class="c pane">
		<div class="rows">
			<div class="r head tr">
				<span>Ticket</span><span>Source</span><span>Base branch</span><span>Status</span><span
					class="num">Runs</span
				><span class="num">Created</span>
			</div>
			{#each shown as t (t.id)}
				{@const rs = runsOf.get(t.id) ?? []}
				<a class="r tr" href={to.ticket(t.id)}>
					<span class="ell ttl">{t.title}</span>
					<span>
						{#if t.source}<span class="tag" title="imported from {t.source.kind}"
								>{t.source.key}</span
							>{:else}<span class="fnt small">by hand</span>{/if}
					</span>
					<span class="mono ell br">{t.baseBranch}</span>
					<span>
						{#if rs.length}<StatusChip state={rs[0].state} size="sm" />{:else}<span
								class="fnt small">no runs</span
							>{/if}
					</span>
					<span class="num mono mut">{rs.length}</span>
					<span class="num mono fnt" title={fmtAbs(t.createdAt)}>{ago(t.createdAt)}</span>
				</a>
			{/each}
		</div>
	</section>
{:else if q || filter !== 'all'}
	<EmptyState compact title="No match" text="No ticket matches this filter." />
{:else}
	<EmptyState
		title="No tickets yet"
		text="A ticket is the work: a goal, files, and a branch. Runs start from it."
	>
		<a class="btn btn-primary" href={to.newTicket({ project: project.id })}>+ New ticket</a>
	</EmptyState>
{/if}

<style>
	.bar {
		display: flex;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
	}
	.q {
		max-width: 280px;
	}
	.tr {
		grid-template-columns: minmax(0, 1fr) 110px minmax(0, 200px) 140px 48px 72px;
	}
	.tr:hover {
		text-decoration: none;
	}
	.ttl {
		font-weight: 500;
	}
	.br {
		font-size: 11px;
		color: var(--mut);
	}
	@media (max-width: 1000px) {
		.tr {
			grid-template-columns: minmax(0, 1fr) 130px 60px;
		}
		.tr > :nth-child(2),
		.tr > :nth-child(3),
		.tr > :nth-child(5) {
			display: none;
		}
	}
</style>
