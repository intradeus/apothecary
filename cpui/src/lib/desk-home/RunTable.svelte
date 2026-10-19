<script lang="ts" module>
	export type RunCol =
		'title' | 'id' | 'state' | 'ws' | 'stage' | 'workshop' | 'flow' | 'branch' | 'mr' | 'updated';
</script>

<script lang="ts">
	import { lore } from '$lib/lore';
	// A dense runs table (DESIGN 4.13): one grid per row so long titles
	// ellipsise, the whole row opens the run (a stretched title link), the MR
	// link sits above it. Columns are picked per page.
	import { to } from '$lib/guild';
	import { fleet } from '$lib/fleet.svelte';
	import { ago, fmtAbs, shortId } from '$lib/format';
	import type { Run } from '$lib/domain';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import WorkspaceStatus from '$lib/ui/WorkspaceStatus.svelte';
	import StageFlask from '$lib/ui/StageFlask.svelte';
	import Vial from '$lib/ui/Vial.svelte';
	import { runFacts, stageStatus } from './runfacts.svelte';

	let {
		runs,
		cols = ['title', 'state', 'ws', 'stage', 'workshop', 'flow', 'mr', 'updated'],
		/** Fetch per-run facts (attempt) for the stage column. Keep to short lists. */
		facts = false
	}: { runs: Run[]; cols?: RunCol[]; facts?: boolean } = $props();

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
	const LABEL: Record<RunCol, string> = {
		title: 'Run',
		id: 'Id',
		state: 'State',
		ws: '',
		stage: 'Stage',
		workshop: 'Workshop',
		flow: 'Flow',
		branch: 'Branch',
		mr: 'MR',
		updated: 'Updated'
	};
	// Columns that fold away on a phone: title, state and age remain.
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
	const projectName = (id?: string) =>
		(id && fleet.projectById.get(id)?.name) || (id ? id.slice(-6) : '—');
</script>

<div class="rt rows" style:--tpl={tpl} style:--tpl-sm={tplSm}>
	<div class="r head">
		{#each cols as c (c)}<span class:wide={wide.includes(c)} class:right={c === 'updated'}
				>{c === 'workshop' ? lore('workshop') : LABEL[c]}</span
			>{/each}
	</div>
	{#each runs as r (r.id)}
		{@const f = facts ? runFacts(r) : undefined}
		<div class="r">
			{#each cols as c (c)}
				{#if c === 'title'}
					<span class="title">
						<a class="stretch ell" href={to.run(r.id)}>{r.title || 'Untitled run'}</a>
						{#if !cols.includes('id')}<span class="mono fnt sid">{shortId(r.id)}</span>{/if}
					</span>
				{:else if c === 'id'}
					{#if cols.includes('title')}
						<span class="mono mut wide">{shortId(r.id)}</span>
					{:else}
						<a class="mono idl stretch" href={to.run(r.id)} title={r.id}>{shortId(r.id)}</a>
					{/if}
				{:else if c === 'state'}
					<span class="st"><StatusChip state={r.state} size="sm" /></span>
				{:else if c === 'ws'}
					<span class="wide ws"
						><WorkspaceStatus
							state={r.state}
							envId={r.envId}
							envStatus={r.envStatus}
							compact
						/></span
					>
				{:else if c === 'stage'}
					<span class="stage wide">
						{#if r.currentStage}
							<StageFlask status={stageStatus(r.state)} size="sm" />
							<span class="mono ell">{r.currentStage}</span>
							{#if f?.attempt}<span class="fnt att" title="attempt {f.attempt}">#{f.attempt}</span
								>{/if}
						{:else}<span class="fnt">—</span>{/if}
					</span>
				{:else if c === 'workshop'}
					<span class="ws-name wide">
						{#if r.projectId}<Vial projectId={r.projectId} />{/if}
						<span class="ell">{projectName(r.projectId)}</span>
					</span>
				{:else if c === 'flow'}
					<span class="mono mut ell wide">{r.flowName}</span>
				{:else if c === 'branch'}
					<span class="mono mut ell wide" title={r.branch}>{r.branch ?? ''}</span>
				{:else if c === 'mr'}
					<span class="wide mr">
						{#if r.mrUrl}
							<a href={r.mrUrl} target="_blank" rel="noreferrer" title={r.mrUrl}
								>MR{r.mrs.length > 1 ? ` ×${r.mrs.length}` : ''} ↗</a
							>
						{:else}<span class="fnt">—</span>{/if}
					</span>
				{:else if c === 'updated'}
					<span class="mono mut upd" title={fmtAbs(r.updatedAt)}>{ago(r.updatedAt)}</span>
				{/if}
			{/each}
		</div>
	{/each}
</div>

<style>
	.rt > .r {
		grid-template-columns: var(--tpl);
		position: relative;
		font-size: 12.5px;
	}
	.title {
		display: flex;
		align-items: baseline;
		gap: 8px;
		min-width: 0;
	}
	.title a {
		font-weight: 500;
		color: var(--tx);
	}
	.title a:hover {
		text-decoration: none;
	}
	/* The title link covers the row; the MR link sits above it. */
	.stretch::after {
		content: '';
		position: absolute;
		inset: 0;
	}
	.idl {
		color: var(--tx);
		font-weight: 600;
	}
	.idl:hover {
		text-decoration: none;
	}
	.sid {
		flex: none;
		font-size: 10.5px;
	}
	.st {
		display: flex;
		min-width: 0;
	}
	.ws {
		display: flex;
		justify-content: center;
	}
	.stage,
	.ws-name {
		display: flex;
		align-items: center;
		gap: 7px;
		min-width: 0;
	}
	.stage .mono {
		font-size: 11.5px;
	}
	.att {
		font-family: var(--mono);
		font-size: 10.5px;
		flex: none;
	}
	.ws-name {
		font-size: 12px;
	}
	.mr {
		position: relative;
		z-index: 1;
	}
	.mr a {
		color: var(--brand);
		font-weight: 600;
		font-size: 12px;
	}
	.upd,
	.right {
		text-align: right;
		font-size: 11px;
	}
	.head > span {
		white-space: nowrap;
	}
	@media (max-width: 760px) {
		.rt > .r {
			grid-template-columns: var(--tpl-sm);
			padding-left: 14px;
			padding-right: 14px;
		}
		.wide,
		.sid {
			display: none !important;
		}
	}
</style>
