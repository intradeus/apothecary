<script lang="ts">
	// Two dimensions as a grid (stage × iteration): the first across, the
	// second down, each cell the accent at an opacity rising with its value.
	// A cell opens the runs behind it.
	import { Axis, Chart, Rect, Svg } from 'layerchart';
	import type { AnalyticsResult, AnalyticsRow } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import { fmtValue } from '../format';
	import { dimText, valueFields, valuesOf } from './shape';

	let {
		result,
		ondrill,
		width
	}: {
		result: AnalyticsResult;
		ondrill: (row: AnalyticsRow) => void;
		/** Fixed; by default it fills its container. */
		width?: number;
	} = $props();

	const HEIGHT = 260;

	// Text on both axes: layerchart picks band scales for them.
	type Band = { bandwidth(): number };

	const type = $derived(valueFields(result)[0].type);
	const cells = $derived(
		result.rows.map((row) => ({
			x: dimText(result, row, 0),
			y: dimText(result, row, 1),
			v: valuesOf(result, row)[0],
			row
		}))
	);
	const max = $derived(Math.max(0, ...cells.map((c) => Number(c.v))));
	// Never fully clear: an empty-looking cell still has a value.
	const alpha = (v: number | null) => (v == null || max === 0 ? 0.08 : 0.12 + (0.88 * v) / max);
</script>

<div class="lc" style:height="{HEIGHT}px">
	<Chart
		data={cells}
		x="x"
		y="y"
		{width}
		height={HEIGHT}
		padding={{ left: 72, bottom: 24, top: 4, right: 4 }}
	>
		{#snippet children({ context })}
			<Svg>
				<Axis placement="left" />
				<Axis placement="bottom" />
				{#each cells as c, i (i)}
					{@const w = (context.xScale as Band).bandwidth()}
					{@const h = (context.yScale as Band).bandwidth()}
					{@const x = context.xScale(c.x)}
					{@const y = context.yScale(c.y)}
					<Rect
						{x}
						{y}
						width={w}
						height={h}
						fill="var(--accent)"
						fillOpacity={alpha(c.v)}
						class="cell"
						role="button"
						tabindex={0}
						aria-label={t('analytics.viz.cell', { x: c.x, y: c.y, v: fmtValue(type, c.v) })}
						onclick={() => ondrill(c.row)}
						onkeydown={(e: KeyboardEvent) => e.key === 'Enter' && ondrill(c.row)}
					/>
					<text
						x={x + w / 2}
						y={y + h / 2}
						text-anchor="middle"
						dominant-baseline="middle"
						class="val">{fmtValue(type, c.v)}</text
					>
				{/each}
			</Svg>
		{/snippet}
	</Chart>
</div>

<style>
	.lc {
		--color-surface-content: var(--text-muted);
		position: relative;
		width: 100%;
	}
	.lc :global(.cell) {
		stroke: var(--line);
		stroke-width: 1;
		cursor: pointer;
	}
	.val {
		fill: var(--text);
		font-size: var(--fs-meta);
		pointer-events: none;
	}
</style>
