<script lang="ts">
	// One point per row: the first measure across, the second up (cost against
	// time per flow version, say). With one measure, the count goes up.
	import { ScatterChart } from 'layerchart';
	import type { AnalyticsResult } from '$lib/domain';
	import { fmtValue } from '../format';
	import { rowLabel, seriesColor, valueFields, valuesOf } from './shape';

	let {
		result,
		width
	}: {
		result: AnalyticsResult;
		/** Fixed; by default it fills its container. */
		width?: number;
	} = $props();

	const HEIGHT = 240;

	const fields = $derived(valueFields(result));
	const xType = $derived(fields[0].type);
	const yType = $derived(fields[1]?.type ?? 'count');
	// A row with either value missing has no place on the plane.
	const data = $derived(
		result.rows
			.map((row) => {
				const v = valuesOf(result, row);
				return { label: rowLabel(result, row), x: v[0], y: fields.length > 1 ? v[1] : row.n };
			})
			.filter((d) => d.x != null && d.y != null)
	);
</script>

<div class="lc" style:height="{HEIGHT}px">
	<ScatterChart
		{data}
		x="x"
		y="y"
		{width}
		height={HEIGHT}
		series={[{ key: 'rows', data, color: seriesColor(0) }]}
		padding={{ left: 56, bottom: 24, top: 8, right: 12 }}
		props={{
			xAxis: { format: (v: number) => fmtValue(xType, v) },
			yAxis: { format: (v: number) => fmtValue(yType, v) }
		}}
	/>
</div>

<style>
	.lc {
		--color-primary: var(--accent);
		--color-surface-content: var(--text-muted);
		--color-surface-100: var(--line);
		position: relative;
		width: 100%;
	}
</style>
