<script lang="ts">
	// One dashboard panel: runs its query (again whenever the query changes)
	// and draws the result in its viz. Its menu exports the result as CSV or
	// lists the runs behind it; a clicked bar, cell or row lists the runs
	// behind that row. Rows resting on fewer than five runs are flagged.
	import { untrack, type Snippet } from 'svelte';
	import { api } from '$lib/api';
	import { errorMessage } from '$lib/core/errors';
	import { I } from '$lib/design/icons';
	import type { AnalyticsQuery, AnalyticsResult, AnalyticsRow, Panel } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { downloadCsv, toCsv } from './csv';
	import DrillSheet from './DrillSheet.svelte';
	import BarViz from './viz/BarViz.svelte';
	import BoxViz from './viz/BoxViz.svelte';
	import HeatmapViz from './viz/HeatmapViz.svelte';
	import LineViz from './viz/LineViz.svelte';
	import NumberViz from './viz/NumberViz.svelte';
	import ScatterViz from './viz/ScatterViz.svelte';
	import TableViz from './viz/TableViz.svelte';

	let {
		panel,
		query,
		actions,
		onresult,
		chartWidth
	}: {
		panel: Panel;
		/** The query as run: the panel's, scoped by the dashboard's range and
		 *  filter bar. */
		query: AnalyticsQuery;
		/** Controls in the head (edit mode's move, edit, remove). */
		actions?: Snippet;
		/** Each result as it lands (the editor offers its rows as baselines). */
		onresult?: (r: AnalyticsResult) => void;
		/** A fixed chart width; by default charts fill the panel. */
		chartWidth?: number;
	} = $props();

	let result = $state<AnalyticsResult | null>(null);
	let error = $state('');
	let loading = $state(true);
	let seq = 0;

	let menuBtn = $state<HTMLButtonElement | null>(null);
	let menuOpen = $state(false);
	let drillOpen = $state(false);
	let drillRow = $state<AnalyticsRow | undefined>();

	// Only the last query's answer lands: a slow earlier one never overwrites it.
	async function load(q: AnalyticsQuery) {
		const mine = ++seq;
		loading = true;
		error = '';
		try {
			const r = await api.analytics.query(q);
			if (mine !== seq) return;
			result = r;
			onresult?.(r);
		} catch (e) {
			if (mine === seq) error = errorMessage(e);
		} finally {
			if (mine === seq) loading = false;
		}
	}

	// Again only when the query's content changes: the dashboard hands a new
	// object on every pick, often an equal one (a filter this grain lacks).
	const key = $derived(JSON.stringify(query));
	$effect(() => {
		void key;
		untrack(() => load(query));
	});

	const small = $derived(result?.rows.some((r) => r.small) ?? false);

	function drill(row?: AnalyticsRow) {
		menuOpen = false;
		drillRow = row;
		drillOpen = true;
	}

	function exportCsv() {
		menuOpen = false;
		// The item is disabled until there is a result.
		downloadCsv(panel.title, toCsv(result!));
	}
</script>

<section class="card-in pn" aria-label={panel.title}>
	<header>
		<h3 class="truncate">{panel.title}</h3>
		{#if small}
			<span class="pill sm flat tint-warning" title={t('analytics.panel.smallHelp')}
				>{t('analytics.panel.small')}</span
			>
		{/if}
		<span class="acts">
			{@render actions?.()}
			<button
				bind:this={menuBtn}
				type="button"
				class="btn-icon btn-sm"
				aria-label={t('analytics.panel.menu')}
				aria-haspopup="menu"
				aria-expanded={menuOpen}
				onclick={() => (menuOpen = !menuOpen)}>{I.more}</button
			>
		</span>
	</header>

	<Popover anchor={menuBtn} bind:open={menuOpen} placement="below-end" role="menu" portal flip>
		<button class="menu-item" role="menuitem" disabled={!result} onclick={exportCsv}
			>{t('analytics.panel.csv')}</button
		>
		<button class="menu-item" role="menuitem" onclick={() => drill()}
			>{t('analytics.panel.drill')}</button
		>
	</Popover>

	<div class="body">
		{#if error}
			<ErrorCard cause={t('analytics.panel.error')} detail={error} onretry={() => load(query)} />
		{:else if !result}
			<Skeleton lines={4} />
		{:else}
			<div class:stale={loading}>
				{#if panel.viz === 'number'}
					<NumberViz {result} />
				{:else if !result.rows.length}
					<EmptyState compact title={t('analytics.panel.empty')} />
				{:else if panel.viz === 'table'}
					<TableViz {result} ondrill={drill} />
				{:else if panel.viz === 'bar'}
					<BarViz {result} ondrill={drill} width={chartWidth} />
				{:else if panel.viz === 'line'}
					<LineViz {result} width={chartWidth} />
				{:else if panel.viz === 'scatter'}
					<ScatterViz {result} width={chartWidth} />
				{:else if panel.viz === 'heatmap'}
					<HeatmapViz {result} ondrill={drill} width={chartWidth} />
				{:else}
					<BoxViz {result} measures={query.measures} ondrill={drill} width={chartWidth} />
				{/if}
			</div>
		{/if}
	</div>
</section>

<DrillSheet bind:open={drillOpen} title={panel.title} {query} {result} row={drillRow} />

<style>
	/* A card's inset: the title and the viz never touch its edge. */
	.pn {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 12px 14px 14px;
		min-width: 0;
		height: 100%;
	}
	/* The title keeps its line: when a narrow panel's controls (edit mode's
	   move, edit, remove) would leave it two letters, they wrap under it. */
	header {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 6px 8px;
		min-width: 0;
	}
	h3 {
		flex: 0 1 auto;
		min-width: 0;
		font-size: var(--fs-section);
		font-weight: 600;
	}
	.acts {
		display: flex;
		align-items: center;
		gap: 8px;
		margin-left: auto;
		flex: none;
	}
	.body {
		min-width: 0;
	}
	.stale {
		opacity: 0.6;
		transition: opacity var(--dur-spring);
	}
</style>
