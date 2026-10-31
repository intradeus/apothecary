<script lang="ts" module>
	export type RunCol =
		'title' | 'id' | 'state' | 'ws' | 'stage' | 'workshop' | 'flow' | 'branch' | 'mr' | 'updated';
</script>

<script lang="ts">
	// A dense runs table (DESIGN 4.13): one grid per row so long titles
	// ellipsise, the whole row opens the run (a stretched title link), the MR
	// link sits above it. Columns are picked per page.
	import { to } from '$lib/core/urls';
	import { t } from '$lib/i18n/index.svelte';
	import { ago, fmtAbs, shortId } from '$lib/core/format';
	import type { Run } from '$lib/domain';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import WorkspaceStatus from '$lib/ui/WorkspaceStatus.svelte';
	import StageFlask from '$lib/ui/StageFlask.svelte';
	import WorkshopName from '$lib/app/WorkshopName.svelte';
	import MrLink from './MrLink.svelte';
	import { runFacts, stageStatus } from './runfacts.svelte';

	let {
		runs,
		cols = ['title', 'state', 'ws', 'stage', 'workshop', 'flow', 'mr', 'updated'],
		/** Fetch per-run facts (attempt) for the stage column. Keep to short lists. */
		facts = false,
		/** Detail for a single workshop's list: the flow's @hash, the branch
		 *  under the title, one !iid link per merge request. */
		detail = false
	}: { runs: Run[]; cols?: RunCol[]; facts?: boolean; detail?: boolean } = $props();

	const WIDTH: Record<RunCol, string> = {
		title: 'minmax(0,1fr)',
		id: '70px',
		state: '150px',
		ws: '22px',
		stage: 'minmax(110px,170px)',
		workshop: 'minmax(90px,160px)',
		flow: 'minmax(70px,130px)',
		branch: 'minmax(100px,1fr)',
		mr: '58px',
		updated: '74px'
	};
	const label = (c: RunCol): string => {
		switch (c) {
			case 'title':
				return t('run.table.run');
			case 'id':
				return t('run.table.id');
			case 'state':
				return t('run.table.state');
			case 'ws':
				return '';
			case 'stage':
				return t('run.table.stage');
			case 'workshop':
				return t('run.table.workshop');
			case 'flow':
				return t('run.table.flow');
			case 'branch':
				return t('run.table.branch');
			case 'mr':
				return t('run.table.mr');
			case 'updated':
				return t('run.table.updated');
		}
	};
	// Columns that fold away when narrow: title, state and age remain.
	const WIDE: RunCol[] = ['id', 'ws', 'stage', 'workshop', 'flow', 'branch', 'mr'];

	// Without a title column the id is the row's link, so it stays on phones.
	const wide = $derived(cols.includes('title') ? WIDE : WIDE.filter((c) => c !== 'id'));
	const tpl = $derived(cols.map((c) => WIDTH[c]).join(' '));
	const tplSm = $derived(
		cols
			.filter((c) => !wide.includes(c))
			.map((c) => (c === 'state' ? 'auto' : WIDTH[c]))
			.join(' ')
	);
</script>

<div class="rt rows" style:--tpl={tpl} style:--tpl-sm={tplSm}>
	<div class="r head">
		{#each cols as c (c)}<span class:hide-sm={wide.includes(c)} class:right={c === 'updated'}
				>{label(c)}</span
			>{/each}
	</div>
	{#each runs as r (r.id)}
		{@const f = facts ? runFacts(r) : undefined}
		<div class="r">
			{#each cols as c (c)}
				{#if c === 'title'}
					<span class="title">
						<a class="stretch truncate" href={to.run(r.id)}>{r.title || t('run.row.untitled')}</a>
						{#if !cols.includes('id')}<span class="mono faint sid"
								>{shortId(r.id)}{#if detail && r.branch}{' · '}{r.branch}{/if}</span
							>{/if}
					</span>
				{:else if c === 'id'}
					{#if cols.includes('title')}
						<span class="mono muted hide-sm">{shortId(r.id)}</span>
					{:else}
						<a class="mono idl stretch" href={to.run(r.id)} title={r.id}>{shortId(r.id)}</a>
					{/if}
				{:else if c === 'state'}
					<span class="st"><StatusChip state={r.state} size="sm" /></span>
				{:else if c === 'ws'}
					<span class="hide-sm ws"
						><WorkspaceStatus
							state={r.state}
							envId={r.envId}
							envStatus={r.envStatus}
							compact
						/></span
					>
				{:else if c === 'stage'}
					<span class="stage hide-sm">
						{#if r.currentStage}
							<StageFlask status={stageStatus(r.state)} size="sm" />
							<span class="mono truncate">{r.currentStage}</span>
							{#if f?.attempt}<span
									class="faint att"
									title={t('run.table.attempt', { n: f.attempt })}>#{f.attempt}</span
								>{/if}
						{:else}<span class="faint">—</span>{/if}
					</span>
				{:else if c === 'workshop'}
					<span class="hide-sm truncate"><WorkshopName projectId={r.projectId} /></span>
				{:else if c === 'flow'}
					<span class="mono muted truncate hide-sm"
						>{r.flowName}{#if detail && r.flowHash}<span class="faint">
								@{r.flowHash.slice(0, 6)}</span
							>{/if}</span
					>
				{:else if c === 'branch'}
					<span class="mono muted truncate hide-sm" title={r.branch}>{r.branch ?? ''}</span>
				{:else if c === 'mr'}
					<span class="hide-sm"><MrLink run={r} {detail} /></span>
				{:else if c === 'updated'}
					<span class="mono muted upd" title={fmtAbs(r.updatedAt)}>{ago(r.updatedAt)}</span>
				{/if}
			{/each}
		</div>
	{/each}
</div>

<style>
	/* It folds by its own width: a table can sit in a column of a wide screen. */
	.rt {
		container: runlist / inline-size;
	}
	.rt > .r {
		grid-template-columns: var(--tpl);
		position: relative;
		font-size: var(--fs-ui);
	}
	.title {
		display: flex;
		align-items: baseline;
		gap: 8px;
		min-width: 0;
	}
	.title a {
		font-weight: 500;
		color: var(--text);
	}
	.title a:hover {
		text-decoration: none;
	}
	/* The title link covers the row (.stretch); MR and workshop links sit
	 * above it (.over-stretch), both shared classes. */
	.idl {
		color: var(--text);
		font-weight: 600;
	}
	.idl:hover {
		text-decoration: none;
	}
	.sid {
		flex: none;
		font-size: var(--fs-id);
	}
	.st {
		display: flex;
		min-width: 0;
	}
	.ws {
		display: flex;
		justify-content: center;
	}
	.stage {
		display: flex;
		align-items: center;
		gap: 7px;
		min-width: 0;
	}
	.stage .mono {
		font-size: var(--fs-help);
	}
	.att {
		font-family: var(--font-code);
		font-size: var(--fs-id);
		flex: none;
	}
	.upd,
	.right {
		text-align: right;
		font-size: var(--fs-meta);
	}
	.head > span {
		white-space: nowrap;
	}
	@container runlist (max-width: 860px) {
		.rt > .r {
			grid-template-columns: var(--tpl-sm);
			padding-left: 14px;
			padding-right: 14px;
		}
		/* Specific enough to beat each cell's own display rule. */
		.rt > .r > .hide-sm,
		.rt .sid {
			display: none;
		}
	}
</style>
