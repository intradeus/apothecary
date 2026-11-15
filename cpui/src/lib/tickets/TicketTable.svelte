<script lang="ts" module>
	import { t } from '$lib/i18n/index.svelte';
	export type TicketCol = 'key' | 'title' | 'status' | 'type' | 'branch' | 'run' | 'updated';
</script>

<script lang="ts">
	// A dense tickets table (DESIGN 7.6), like RunTable: one grid per row, the
	// title link covers the row, columns picked per page. Rows are one
	// workshop's source read live. Two states, kept apart: the issue's status
	// in its tracker, and its active run (one at a time across workshops).
	import type { TicketRow } from '$lib/domain';
	import { to } from '$lib/core/urls';
	import { fleet } from '$lib/app/fleet.svelte';
	import { ago, fmtAbs, shortId } from '$lib/core/format';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import ChangeTypeTag from './ChangeTypeTag.svelte';
	import TicketStatusChip from './TicketStatusChip.svelte';

	let {
		projectId,
		tickets,
		cols = ['key', 'title', 'status', 'type', 'branch', 'run', 'updated']
	}: {
		/** The workshop the rows are read through: their pages are its. */
		projectId: string;
		tickets: TicketRow[];
		cols?: TicketCol[];
	} = $props();

	const WIDTH: Record<TicketCol, string> = {
		key: '96px',
		title: 'minmax(0, 2fr)',
		status: '120px',
		type: '56px',
		branch: 'minmax(0, 1.2fr)',
		run: '150px',
		updated: '74px'
	};
	const WIDE: TicketCol[] = ['key', 'branch'];
	const tpl = $derived(cols.map((c) => WIDTH[c]).join(' '));
	const tplSm = $derived(
		cols
			.filter((c) => !WIDE.includes(c))
			.map((c) => (c === 'run' ? 'auto' : WIDTH[c]))
			.join(' ')
	);
	const label = (c: TicketCol) =>
		({
			key: t('tickets.col.key'),
			title: t('tickets.col.title'),
			status: t('tickets.col.status'),
			type: t('tickets.col.type'),
			branch: t('tickets.col.branch'),
			run: t('tickets.col.run'),
			updated: t('tickets.col.updated')
		})[c];
</script>

<div class="tt rows" style:--tpl={tpl} style:--tpl-sm={tplSm}>
	<div class="r head">
		{#each cols as c (c)}<span class:hide-sm={WIDE.includes(c)} class:num={c === 'updated'}
				>{label(c)}</span
			>{/each}
	</div>
	{#each tickets as tk (tk.key)}
		<div class="r">
			{#each cols as c (c)}
				{#if c === 'key'}
					<span class="hide-sm truncate"><span class="tag">{tk.key}</span></span>
				{:else if c === 'title'}
					<a class="title truncate stretch" href={to.workshopTicket(projectId, tk.key)}
						>{tk.title}</a
					>
				{:else if c === 'status'}
					<span class="st"><TicketStatusChip status={tk.status} done={tk.done} /></span>
				{:else if c === 'type'}
					<span><ChangeTypeTag type={tk.changeType} sourceType={tk.type} /></span>
				{:else if c === 'branch'}
					<span class="mono muted truncate hide-sm" title={tk.baseBranch}>{tk.baseBranch}</span>
				{:else if c === 'run'}
					<span class="st">
						{#if tk.activeRunId}
							{@const run = fleet.runById.get(tk.activeRunId)}
							<a
								class="mono over-stretch"
								href={to.run(tk.activeRunId)}
								title={t('tickets.activeRun')}
								>{#if run}<StatusChip state={run.state} size="sm" />{:else}{shortId(
										tk.activeRunId
									)}{/if}</a
							>
						{:else}
							<span class="faint">{t('tickets.noActiveRun')}</span>
						{/if}
					</span>
				{:else if c === 'updated'}
					{#if tk.updatedAt}
						<span class="mono muted num" title={fmtAbs(tk.updatedAt)}>{ago(tk.updatedAt)}</span>
					{:else}<span class="faint num">—</span>{/if}
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
		color: var(--text);
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
