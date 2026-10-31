<script lang="ts">
	// The title block of the run's top bar: breadcrumb, a small title, a status
	// line. Context only; the windows hold the information.
	import { to } from '$lib/core/urls';
	import { fleet } from '$lib/app/fleet.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { ago, fmtAbs, fmtClockDuration, fmtCost, shortId } from '$lib/core/format';
	import { I } from '$lib/design/icons';
	import Breadcrumbs, { type Crumb } from '$lib/ui/Breadcrumbs.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import { accentSplit } from '../derive';
	import type { RunStore } from '../store.svelte';

	let { rs }: { rs: RunStore } = $props();

	const run = $derived(rs.run!);
	const project = $derived(run.projectId ? fleet.projectById.get(run.projectId) : undefined);
	const sid = $derived(shortId(run.id));
	const [plain, accent] = $derived(accentSplit(run.title));
	const ticketKey = $derived(rs.ticket?.source?.key);
	const crumbs = $derived<Crumb[]>([
		{ label: t('nav.desk'), href: to.desk() },
		...(run.projectId
			? [
					{
						label: project?.name ?? (rs.projectName || t('run.header.workshopFallback')),
						href: to.workshop(run.projectId)
					}
				]
			: []),
		{ label: sid, mono: true }
	]);
	// How long the batch has brewed: ticking while the run lives, stopped at
	// its last word once it ends (updatedAt for runs that never said one).
	const END = new Set(['run_completed', 'run_failed', 'run_abandoned']);
	const endedAt = $derived(
		rs.terminal ? (rs.events.findLast((e) => END.has(e.type))?.ts ?? run.updatedAt) : undefined
	);
	const brewed = $derived(
		fmtClockDuration(
			(endedAt ? new Date(endedAt).getTime() : rs.now) - new Date(run.createdAt).getTime()
		)
	);
	const active = $derived(!rs.terminal);
</script>

<div class="title">
	<div class="crow">
		<Breadcrumbs {crumbs} />
		{#if run.ticketId}
			<span class="dot-sep">·</span>
			<a class="link" href={to.ticket(run.ticketId)} title={rs.ticket?.title}
				>{ticketKey ?? t('run.header.ticket')} {I.external}</a
			>
		{/if}
		{#if run.ticketUrl}
			<a class="link" href={run.ticketUrl} target="_blank" rel="noreferrer"
				>{t('run.header.tracker')} {I.external}</a
			>
		{/if}
	</div>
	<h1 title={run.title}>
		{plain}{#if accent}{' '}<span class="accent">{accent}</span>{/if}
	</h1>
	<div class="status">
		<StatusChip state={run.state} size="sm" suffix={run.state === 'failed'} />
		{#if run.hold && !rs.terminal}
			<span
				class="pill sm tint-attention"
				title={t('run.header.heldTitle', { ago: ago(run.hold.since, rs.now) })}
				><span class="live-dot sm pulse hue-attention"></span>{t('run.header.held')} ·
				<span class="tick">{fmtClockDuration(rs.now - new Date(run.hold.since).getTime())}</span
				></span
			>
		{/if}
		<span class="mono stats tick">
			<span
				class:hue-running={active}
				title={t('run.header.started', {
					at: fmtAbs(run.createdAt),
					ago: ago(run.createdAt, rs.now)
				})}>{brewed}</span
			>
			· {fmtCost(rs.totals.cost)} · {t('run.header.turns', { count: rs.totals.turns })}
		</span>
		{#if run.skillsets?.length}
			<span class="sk" title={t('run.header.skillsets')}
				>{#each run.skillsets as p (p.id)}<span class="pill sm tint-muted"
						>{p.name} <span class="mono">v{p.seq}</span></span
					>{/each}</span
			>
		{/if}
	</div>
</div>

<style>
	.title {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 3px;
	}
	.crow {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		font-size: var(--fs-help);
		white-space: nowrap;
	}
	.crow :global(.bc) {
		flex: 0 1 auto;
		font-size: var(--fs-help);
	}
	.crow :global(.bc > *) {
		flex: 0 1 auto;
		min-width: 0;
	}
	.crow :global(.bc > .sep) {
		flex: none;
	}
	.dot-sep {
		color: var(--text-faint);
	}
	.crow .link {
		flex: none;
	}
	h1 {
		min-width: 0;
		font-size: var(--fs-mark);
		letter-spacing: var(--tr-run);
		line-height: 1.15;
		text-wrap: nowrap;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.status {
		display: flex;
		align-items: center;
		gap: 10px;
		min-width: 0;
		white-space: nowrap;
		overflow: hidden;
	}
	.stats {
		color: var(--text-muted);
	}
	.sk {
		display: inline-flex;
		gap: 4px;
	}
	/* Container queries on the run page's own width (the desk beside the
	 * sidebar), at the shared breakpoints. */
	@container runpage (max-width: 1000px) {
		.title {
			flex-basis: 100%;
		}
	}
	@container runpage (max-width: 760px) {
		h1 {
			white-space: normal;
			text-wrap: balance;
			display: -webkit-box;
			-webkit-box-orient: vertical;
			-webkit-line-clamp: 3;
			line-clamp: 3;
		}
		.status {
			flex-wrap: wrap;
			row-gap: 4px;
		}
	}
</style>
