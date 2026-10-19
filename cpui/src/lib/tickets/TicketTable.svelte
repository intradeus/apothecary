<script lang="ts" module>
	export type TicketCol = 'title' | 'source' | 'workshop' | 'branch' | 'status' | 'created';
</script>

<script lang="ts">
	// A dense tickets table (DESIGN 7.6), like RunTable: one grid per row, the
	// title link covers the row, columns picked per page. Status is derived:
	// the latest run's state, "×N" when there are several, "no runs" before.
	import type { Ticket } from '$lib/domain';
	import { to } from '$lib/guild';
	import { fleet } from '$lib/fleet.svelte';
	import { ago, fmtAbs } from '$lib/format';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import WorkshopName from '$lib/workshop/WorkshopName.svelte';

	let {
		tickets,
		cols = ['title', 'source', 'workshop', 'branch', 'status', 'created']
	}: { tickets: Ticket[]; cols?: TicketCol[] } = $props();

	const WIDTH: Record<TicketCol, string> = {
		title: 'minmax(0, 2fr)',
		source: '110px',
		workshop: 'minmax(0, 1fr)',
		branch: 'minmax(0, 1.2fr)',
		status: '170px',
		created: '74px'
	};
	const WIDE: TicketCol[] = ['source', 'workshop', 'branch'];
	const tpl = $derived(cols.map((c) => WIDTH[c]).join(' '));
	const tplSm = $derived(
		cols
			.filter((c) => !WIDE.includes(c))
			.map((c) => (c === 'status' ? 'auto' : WIDTH[c]))
			.join(' ')
	);
	const label = (c: TicketCol) =>
		({
			title: 'Title',
			source: 'Source',
			workshop: 'Workshop',
			branch: 'Base branch',
			status: 'Status',
			created: 'Created'
		})[c];
</script>

<div class="tt rows" style:--tpl={tpl} style:--tpl-sm={tplSm}>
	<div class="r head">
		{#each cols as c (c)}<span class:hide-sm={WIDE.includes(c)} class:num={c === 'created'}
				>{label(c)}</span
			>{/each}
	</div>
	{#each tickets as t (t.id)}
		{@const runs = fleet.runsByTicket.get(t.id) ?? []}
		<div class="r">
			{#each cols as c (c)}
				{#if c === 'title'}
					<a class="title ell stretch" href={to.ticket(t.id)}>{t.title}</a>
				{:else if c === 'source'}
					<span class="hide-sm">
						{#if t.source?.key}<span class="tag" title="Imported from {t.source.kind}"
								>{t.source.key}</span
							>{:else}<span class="fnt">—</span>{/if}
					</span>
				{:else if c === 'workshop'}
					<span class="hide-sm ell"><WorkshopName projectId={t.projectId} /></span>
				{:else if c === 'branch'}
					<span class="mono mut ell hide-sm" title={t.baseBranch}>{t.baseBranch}</span>
				{:else if c === 'status'}
					<span class="st">
						{#if runs.length}
							<StatusChip state={runs[0].state} size="sm" />
							{#if runs.length > 1}<span class="fnt mono">×{runs.length}</span>{/if}
						{:else}
							<span class="fnt">no runs</span>
						{/if}
					</span>
				{:else if c === 'created'}
					<span class="mono mut num" title={fmtAbs(t.createdAt)}>{ago(t.createdAt)}</span>
				{/if}
			{/each}
		</div>
	{/each}
</div>

<style>
	.tt > .r {
		grid-template-columns: var(--tpl);
		font-size: var(--fs-ui);
	}
	.title {
		font-weight: 500;
		color: var(--tx);
	}
	.title:hover {
		text-decoration: none;
	}
	.st {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
	}
	.num {
		font-size: var(--fs-meta);
	}
	@media (max-width: 760px) {
		.tt > .r {
			grid-template-columns: var(--tpl-sm);
			padding-left: 14px;
			padding-right: 14px;
		}
		.hide-sm {
			display: none;
		}
	}
</style>
