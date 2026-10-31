<script lang="ts">
	// A workshop on the desk (DESIGN 7.2/7.3): its live runs (state dot, title
	// over "state · stage", stage-type chip, elapsed over cost), its last
	// finished runs with their MR, and its tickets. Rows slide into their new
	// places on the spring when the list reorders.
	import { flip } from 'svelte/animate';
	import { t } from '$lib/i18n/index.svelte';
	import { to } from '$lib/urls';
	import { fmtCost, fmtDuration } from '$lib/format';
	import type { Project, Run } from '$lib/domain';
	import StageChip from '$lib/ui/StageChip.svelte';
	import { I } from '$lib/ui/icons';
	import { ms, spring } from '$lib/ui/motion';
	import RunRow from '$lib/run/RunRow.svelte';
	import WorkshopName from '$lib/workshop/WorkshopName.svelte';
	import { runFacts, stageType } from '$lib/run/runfacts.svelte';

	let {
		project,
		live,
		finished = [],
		liveTotal,
		pings = 0,
		tickets,
		now
	}: {
		project: Project;
		/** Live runs to show (after the desk filter). */
		live: Run[];
		/** The last finished runs (completed / failed). */
		finished?: Run[];
		/** All live runs of the workshop, for the head summary. */
		liveTotal: number;
		pings?: number;
		/** Ticket count; undefined while loading. */
		tickets?: number;
		now: number;
	} = $props();

	const stageLine = (r: Run) =>
		r.state === 'provisioning'
			? t('deskhome.card.starting')
			: r.state === 'pending'
				? t('deskhome.card.queued')
				: (r.currentStage ?? '—');
	const move = { duration: ms('--dur-spring', 450), easing: spring };
</script>

<article class="wc c pane">
	<header class="head">
		<span class="nm"><WorkshopName projectId={project.id} idle={liveTotal === 0} link /></span>
		<span class="sum">
			{liveTotal
				? t('deskhome.card.live', { count: liveTotal })
				: t('deskhome.card.idle')}{#if pings}{' · '}<span class="hue-sky"
					>{t('deskhome.card.pings', { count: pings })}</span
				>{/if}
		</span>
	</header>

	{#each live as r (r.id)}
		{@const f = runFacts(r)}
		{@const st = stageType(r)}
		<div animate:flip={move}>
			<RunRow
				run={r}
				detail={stageLine(r) +
					(f?.attempt ? ` · ${t('deskhome.card.attempt', { n: f.attempt })}` : '')}
			>
				{#snippet trailing()}
					<span class="tr">
						{#if st}<StageChip type={st} />{/if}
						<span class="nums">
							<span>{fmtDuration(now - new Date(r.createdAt).getTime())}</span>
							<span class="fnt">{f ? fmtCost(f.cost) : '…'}</span>
						</span>
					</span>
				{/snippet}
			</RunRow>
		</div>
	{/each}

	{#if finished.length}
		<div class="pane-sub">{t('deskhome.card.lastFinished')}</div>
		{#each finished as r (r.id)}
			<div animate:flip={move}><RunRow run={r} variant="finished" /></div>
		{/each}
	{/if}

	<footer class="pane-foot">
		<a href={to.tickets({ project: project.id })}
			>{tickets === undefined
				? t('deskhome.card.ticketsLoading')
				: t('deskhome.card.tickets', { count: tickets })}</a
		>
		<span class="spacer"></span>
		<a class="btn btn-sm" href={to.newTicket({ project: project.id })}
			>{I.add} {t('deskhome.card.newTicket')}</a
		>
	</footer>
</article>

<style>
	.wc {
		display: flex;
		flex-direction: column;
		min-width: 0;
		height: 100%;
	}
	.head {
		display: flex;
		align-items: center;
		gap: 9px;
		padding: 11px 14px;
	}
	.nm {
		flex: 1;
		min-width: 0;
		display: flex;
	}
	/* The workshop's name is the card's title. */
	.nm :global(.wn) {
		font-size: var(--fs-body);
		font-weight: 600;
		color: var(--tx);
	}
	.sum {
		font-size: var(--fs-meta);
		color: var(--mut);
		white-space: nowrap;
	}
	.tr {
		display: flex;
		align-items: center;
		gap: 10px;
	}
	.nums {
		font-family: var(--mono);
		font-size: var(--fs-id);
		color: var(--mut);
		text-align: right;
		white-space: nowrap;
		display: flex;
		flex-direction: column;
		line-height: 1.3;
	}
	.pane-foot {
		margin-top: auto;
		padding: 8px 10px 8px 14px;
		font-size: var(--fs-label);
	}
	.pane-foot > a:first-child {
		color: var(--mut);
	}
</style>
