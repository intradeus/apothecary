<script lang="ts">
	// A trend over the time bucket. One line per measure; grouped by a second
	// dimension, one line per value of it (of the first measure) instead.
	import { LineChart } from 'layerchart';
	import type { AnalyticsResult } from '$lib/domain';
	import { fmtValue } from '../format';
	import { bucketDate, dimText, seriesColor, valueFields, valuesOf } from './shape';

	let {
		result,
		width
	}: {
		result: AnalyticsResult;
		/** Fixed; by default it fills its container. */
		width?: number;
	} = $props();

	const HEIGHT = 220;

	const fields = $derived(valueFields(result));
	const split = $derived(result.dims.length > 1);

	// Wide rows, one per bucket: { at, s0, s1, … }.
	const shaped = $derived.by(() => {
		const keys = new Map<string, string>();
		const byBucket = new Map<string, Record<string, unknown>>();
		for (const row of result.rows) {
			const bucket = row.dims[0] ?? '';
			const at = byBucket.get(bucket) ?? { at: bucketDate(bucket) };
			byBucket.set(bucket, at);
			const vals = valuesOf(result, row);
			if (split) {
				const name = row.dims
					.slice(1)
					.map((_, i) => dimText(result, row, i + 1))
					.join(' · ');
				if (!keys.has(name)) keys.set(name, `s${keys.size}`);
				at[keys.get(name)!] = vals[0];
			} else {
				fields.forEach((f, i) => {
					keys.set(f.label, `s${i}`);
					at[`s${i}`] = vals[i];
				});
			}
		}
		return {
			data: [...byBucket.values()].sort((a, b) => +(a.at as Date) - +(b.at as Date)),
			series: [...keys].map(([label, key], i) => ({ key, label, color: seriesColor(i) }))
		};
	});
</script>

<div class="lc" style:height="{HEIGHT}px">
	<LineChart
		data={shaped.data}
		x="at"
		series={shaped.series}
		{width}
		height={HEIGHT}
		legend={shaped.series.length > 1}
		padding={{ left: 56, bottom: 24, top: 8, right: 12 }}
		props={{ yAxis: { format: (v: number) => fmtValue(fields[0].type, v) } }}
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
