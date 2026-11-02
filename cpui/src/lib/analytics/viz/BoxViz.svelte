<script lang="ts">
	// A spread per row: whiskers min to max, the box p25 to p75, a rule at the
	// median. It reads those five aggregations of one field off the query (the
	// editor adds them when box is chosen); without them it says so.
	import { Axis, BoxPlot, Chart, Svg } from 'layerchart';
	import type { Agg, AnalyticsResult, AnalyticsRow, MeasureRef } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import { fmtValue } from '../format';
	import { rowLabel } from './shape';

	let {
		result,
		measures,
		ondrill,
		width
	}: {
		result: AnalyticsResult;
		/** The query's measures, in result order: which column is which agg. */
		measures: MeasureRef[];
		ondrill: (row: AnalyticsRow) => void;
		/** Fixed; by default it fills its container. */
		width?: number;
	} = $props();

	const HEIGHT = 240;

	const NEEDS: Agg[] = ['min', 'p25', 'median', 'p75', 'max'];

	const at = $derived.by(() => {
		const field = measures[0]?.field;
		const idx = NEEDS.map((a) => measures.findIndex((m) => m.field === field && m.agg === a));
		return idx.some((i) => i < 0) ? null : idx;
	});
	const boxes = $derived(
		at
			? result.rows
					.map((row) => {
						const [min, q1, median, q3, max] = at.map((i) => row.values[i]);
						return { label: rowLabel(result, row), min, q1, median, q3, max, row };
					})
					.filter((b) => [b.min, b.q1, b.median, b.q3, b.max].every((v) => v != null))
			: []
	);
</script>

{#if !at}
	<p class="note">{t('analytics.viz.boxNeeds')}</p>
{:else}
	{@const type = result.measures[0].type}
	<div class="lc" style:height="{HEIGHT}px">
		<Chart
			data={boxes}
			x="label"
			y={['min', 'max']}
			{width}
			height={HEIGHT}
			padding={{ left: 56, bottom: 24, top: 8, right: 12 }}
		>
			<Svg>
				<Axis placement="left" format={(v: number) => fmtValue(type, v)} />
				<Axis placement="bottom" />
				{#each boxes as b, i (i)}
					<g
						role="button"
						tabindex="0"
						aria-label={t('analytics.viz.box', {
							label: b.label,
							median: fmtValue(type, b.median)
						})}
						class="box"
						onclick={() => ondrill(b.row)}
						onkeydown={(e) => e.key === 'Enter' && ondrill(b.row)}
					>
						<BoxPlot
							data={b}
							min="min"
							q1="q1"
							median="median"
							q3="q3"
							max="max"
							fill="var(--accent-bg)"
							stroke="var(--accent)"
						/>
					</g>
				{/each}
			</Svg>
		</Chart>
	</div>
{/if}

<style>
	.lc {
		--color-surface-content: var(--text-muted);
		position: relative;
		width: 100%;
	}
	.lc :global(.box) {
		cursor: pointer;
	}
</style>
