<script lang="ts">
	// The war room: every sandbox the person has in flight, across all their
	// workshops, as a live tactical map. An always-dark board (the terminal's
	// fixed colours, so it is dark in every theme) holds the HUD, the map, the
	// kill feed and, for a selected unit, its dossier. It can go fullscreen
	// for a wall screen; its motion stops while the tab is away.
	import { fleet } from '$lib/app/fleet.svelte';
	import { errorMessage } from '$lib/core/errors';
	import { to } from '$lib/core/urls';
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import Dossier from './Dossier.svelte';
	import Hud from './Hud.svelte';
	import KillFeed from './KillFeed.svelte';
	import SectorMap from './SectorMap.svelte';
	import { hudStats } from './stats';
	import { swarmFeed } from './swarmFeed.svelte';

	$effect(() => swarmFeed.use());
	$effect(() => fleet.use());

	const swarm = $derived(swarmFeed.swarm);
	const units = $derived(swarm?.units ?? []);
	const stats = $derived(hudStats(units));
	const nameOf = (projectId: string) => fleet.projectName(projectId);

	/** The selected unit's run id; its dossier is open. */
	let selected = $state<string | null>(null);
	const unit = $derived(selected ? units.find((u) => u.runId === selected) : undefined);
	// A unit that leaves the board takes its dossier with it.
	$effect(() => {
		if (selected && swarm && !unit) selected = null;
	});

	let board = $state<HTMLElement>();
	let full = $state(false);
	$effect(() => {
		const sync = () => (full = !!board && document.fullscreenElement === board);
		document.addEventListener('fullscreenchange', sync);
		return () => document.removeEventListener('fullscreenchange', sync);
	});

	async function toggleFullscreen() {
		try {
			if (full) {
				await document.exitFullscreen();
				full = false;
			} else if (board) {
				await board.requestFullscreen();
				full = true;
			}
		} catch (e) {
			toast(`${t('page.warRoom.fullscreenError')}: ${errorMessage(e)}`, { tone: 'bad' });
		}
	}
</script>

<PageHeader
	crumbs={[{ label: t('nav.desk'), href: to.desk() }, { label: t('page.warRoom.title') }]}
	title={t('page.warRoom.title')}
	accent={t('page.warRoom.accent')}
>
	{#snippet meta()}
		{#if swarm}<span>{t('page.warRoom.meta', { count: units.length })}</span>{/if}
	{/snippet}
</PageHeader>

<div class="page glass">
	{#if !swarm && swarmFeed.error}
		<ErrorCard
			cause={t('page.warRoom.loadError')}
			detail={swarmFeed.error}
			onretry={() => swarmFeed.refresh()}
		/>
	{:else if !swarm}
		<Skeleton pane lines={8} />
	{:else if units.length === 0}
		<EmptyState title={t('page.warRoom.empty')} text={t('page.warRoom.emptyText')}>
			<a class="btn btn-primary" href={to.tickets()}>{t('page.warRoom.openTickets')}</a>
		</EmptyState>
	{:else}
		<section
			class="board"
			class:paused={swarmFeed.paused}
			bind:this={board}
			aria-label={t('page.warRoom.board')}
		>
			<div class="bar">
				<Hud {stats} capacity={fleet.capacity} />
				<button class="btn-sm fs" onclick={toggleFullscreen}>
					{full ? I.restore : I.maximise}
					{full ? t('page.warRoom.exitFullscreen') : t('page.warRoom.fullscreen')}
				</button>
			</div>
			{#if swarmFeed.error}
				<p class="stale" role="status">
					{I.warn}
					{t('page.warRoom.stale', { error: swarmFeed.error })}
				</p>
			{/if}
			<div class="theatre" class:open={!!unit}>
				<div class="map">
					<SectorMap {units} {nameOf} {selected} onselect={(id) => (selected = id)} />
				</div>
				{#if unit}
					<Dossier {unit} {nameOf} onclose={() => (selected = null)} />
				{/if}
			</div>
			<div class="ticker">
				<KillFeed entries={swarmFeed.feed} {nameOf} />
			</div>
		</section>
	{/if}
</div>

<style>
	.page > .board {
		flex: 1;
		max-width: none;
	}
	/* The terminal's fixed colours, and the neutrals re-pointed to them, so
	 * what sits on the board (chips, graphs, buttons) reads in every theme. */
	.board {
		--text: var(--term-text);
		--text-muted: var(--term-muted);
		--text-faint: var(--term-faint);
		--text-code: var(--term-text);
		--line: var(--term-line);
		--line-strong: var(--term-button-line);
		--glass-line: var(--term-button-line);
		--fill-1: var(--term-button-bg);
		--fill-2: var(--term-button-bg);
		--code-bg: var(--term-bg);
		--running-text: var(--running);
		--attention-text: var(--attention);
		--warning-text: var(--warning);
		--success-text: var(--success);
		--danger-text: var(--danger);
		--accent-text: var(--accent);
		--accent-2-text: var(--accent-2);
		--muted-text: var(--term-muted);
		position: relative;
		display: grid;
		grid-template-rows: auto auto minmax(0, 1fr) auto;
		gap: 10px;
		min-height: 560px;
		padding: 12px;
		overflow: hidden;
		border: 1px solid var(--term-button-line);
		border-radius: var(--r-pane);
		color: var(--term-text);
		background:
			radial-gradient(ellipse at center, var(--term-button-bg), transparent 70%),
			linear-gradient(var(--term-bg), var(--term-bg)), var(--term-black);
	}
	.board:fullscreen {
		border: none;
		border-radius: 0;
	}
	/* Scanlines over everything; they never take a click. */
	.board::after {
		position: absolute;
		inset: 0;
		background: repeating-linear-gradient(
			transparent 0,
			transparent 2px,
			var(--term-line) 3px,
			transparent 4px
		);
		content: '';
		pointer-events: none;
	}
	/* The tab was away: everything stands still until the next picture. */
	.board.paused :global(*) {
		animation-play-state: paused;
	}
	.bar {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 12px;
	}
	.fs {
		flex: none;
	}
	.stale {
		margin: 0;
		padding: 4px 10px;
		border: 1px solid var(--warning-line);
		border-radius: var(--r-xs);
		color: var(--warning);
		background: var(--warning-bg);
		font-size: var(--fs-help);
	}
	/* Rows are pinned: the stale line (row 2) comes and goes, and the map
	 * must keep the free row either way. */
	.theatre {
		display: grid;
		grid-row: 3;
		grid-template-rows: minmax(0, 1fr);
		grid-template-columns: minmax(0, 1fr);
		min-height: 0;
	}
	.theatre.open {
		grid-template-columns: minmax(0, 1fr) var(--aside-w);
	}
	/* The map fills what the row leaves: the SVG sits on it, so its aspect
	 * ratio never makes the row taller than the board. */
	.map {
		position: relative;
		min-width: 0;
		min-height: 360px;
	}
	.map > :global(svg) {
		position: absolute;
		inset: 0;
	}
	/* A fixed strip: the feed scrolls inside it (an auto row would size to a
	 * scroller's zero intrinsic height). */
	.ticker {
		display: flex;
		grid-row: 4;
		height: 132px;
		min-height: 0;
		padding-top: 8px;
		border-top: 1px solid var(--term-line);
	}
	@media (max-width: 1000px) {
		.theatre.open {
			grid-template-columns: minmax(0, 1fr);
		}
		.bar {
			flex-direction: column;
		}
	}
</style>
