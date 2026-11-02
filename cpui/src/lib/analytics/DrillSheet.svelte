<script lang="ts">
	// The runs behind a panel, or behind one row of it: a side sheet listing
	// them (newest first, up to 200), each opening its run.
	import { api } from '$lib/api';
	import { errorMessage } from '$lib/core/errors';
	import { ago, fmtCost, fmtDuration } from '$lib/core/format';
	import { to } from '$lib/core/urls';
	import { I } from '$lib/design/icons';
	import type { AnalyticsQuery, AnalyticsResult, AnalyticsRow, DrillRun } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Sheet from '$lib/ui/Sheet.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import { drillScope } from './scope';
	import { rowLabel } from './viz/shape';

	let {
		open = $bindable(false),
		title,
		query,
		result,
		row
	}: {
		open: boolean;
		/** The panel's title. */
		title: string;
		/** The query as the panel ran it. */
		query: AnalyticsQuery;
		result?: AnalyticsResult | null;
		/** The clicked row; none: the whole panel. */
		row?: AnalyticsRow;
	} = $props();

	let runs = $state<DrillRun[] | null>(null);
	let error = $state('');

	async function load() {
		runs = null;
		error = '';
		const { filters, time } = drillScope(query, result ?? undefined, row);
		try {
			runs = await api.analytics.drill(query.grain, filters, time);
		} catch (e) {
			error = errorMessage(e);
		}
	}

	$effect(() => {
		if (open) load();
	});

	const sub = $derived(result && row ? rowLabel(result, row) : '');
</script>

<Sheet bind:open side="right" label={t('analytics.drill.label', { title })}>
	<div class="ds island">
		<header>
			<div class="titles">
				<h2 class="truncate">{title}</h2>
				{#if sub}<p class="muted truncate">{sub}</p>{/if}
			</div>
			<button
				type="button"
				class="btn-icon"
				aria-label={t('analytics.drill.close')}
				onclick={() => (open = false)}>{I.close}</button
			>
		</header>
		{#if error}
			<ErrorCard cause={t('analytics.drill.error')} detail={error} onretry={load} />
		{:else if !runs}
			<Skeleton lines={6} />
		{:else if !runs.length}
			<EmptyState compact title={t('analytics.drill.empty')} />
		{:else}
			<p class="faint caption">{t('analytics.drill.count', { count: runs.length })}</p>
			<ul class="rows">
				{#each runs as r (r.runId)}
					<li class="r">
						<a class="stretch truncate" href={to.run(r.runId)}
							>{r.title || t('analytics.drill.untitled')}</a
						>
						<StatusChip state={r.state} size="sm" />
						<span class="mono faint truncate">{r.flowName}</span>
						<span class="num muted">{fmtCost(r.costUsd)}</span>
						<span class="num muted">{fmtDuration(r.wallMs)}</span>
						<span class="faint">{ago(r.createdAt)}</span>
					</li>
				{/each}
			</ul>
		{/if}
	</div>
</Sheet>

<style>
	.ds {
		width: min(560px, 100vw);
		height: 100%;
		overflow: auto;
		padding: 18px 20px;
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	header {
		display: flex;
		align-items: flex-start;
		gap: 12px;
	}
	.titles {
		flex: 1;
		min-width: 0;
	}
	h2 {
		font-size: var(--fs-title);
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.r {
		grid-template-columns: minmax(0, 1fr) auto minmax(60px, 110px) 64px 64px 64px;
		padding-inline: 4px;
	}
</style>
