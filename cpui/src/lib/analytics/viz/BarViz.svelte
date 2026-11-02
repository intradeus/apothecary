<script lang="ts">
	// Horizontal bars, one per row (its dimensions read on the left), one
	// series per measure. A bar opens the runs behind its row. The band
	// tooltip's rects sit over the bars and take a pointer's clicks, so its
	// click drills too; the bar's own click covers a chart drawn without them.
	import { BarChart } from 'layerchart';
	import type { AnalyticsResult, AnalyticsRow } from '$lib/domain';
	import { fmtValue } from '../format';
	import { rowLabel, seriesColor, valueFields, valuesOf } from './shape';

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

	const HEIGHT = 240;

	type Datum = { label: string; row: AnalyticsRow } & Record<string, unknown>;

	const fields = $derived(valueFields(result));
	const data = $derived<Datum[]>(
		result.rows.map((row) => ({
			label: rowLabel(result, row),
			row,
			...Object.fromEntries(valuesOf(result, row).map((v, i) => [`m${i}`, v ?? 0]))
		}))
	);
	const series = $derived(
		fields.map((f, i) => ({ key: `m${i}`, label: f.label, color: seriesColor(i) }))
	);
	const short = (s: unknown) => {
		const v = String(s);
		return v.length > 18 ? `${v.slice(0, 17)}…` : v;
	};
</script>

<div class="lc" style:height="{HEIGHT}px">
	<BarChart
		{data}
		y="label"
		{series}
		seriesLayout="group"
		orientation="horizontal"
		{width}
		height={HEIGHT}
		padding={{ left: 120, bottom: 24, top: 4, right: 12 }}
		props={{
			bars: { strokeWidth: 0 },
			xAxis: { format: (v: number) => fmtValue(fields[0].type, v) },
			yAxis: { format: short }
		}}
		onBarClick={(_e, d) => ondrill((d.data as Datum).row)}
		onTooltipClick={(_e, { data }) => data && ondrill((data as Datum).row)}
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
