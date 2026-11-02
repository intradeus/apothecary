<script lang="ts">
	// The result as a table: dimensions (labelled), measures, n and, when a
	// scorecard ranked it, the score. Headers sort in place (again: the other
	// way); the engine's order stands until one is clicked. A row opens the
	// runs behind it.
	import type { AnalyticsResult, AnalyticsRow } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import { fmtValue } from '../format';
	import { dimText, valueFields } from './shape';

	let { result, ondrill }: { result: AnalyticsResult; ondrill: (row: AnalyticsRow) => void } =
		$props();

	type Col = {
		key: string;
		label: string;
		num: boolean;
		get: (r: AnalyticsRow) => string | number | null;
	};

	const fields = $derived(valueFields(result));
	const scored = $derived(result.rows.some((r) => r.score != null));
	const cols = $derived<Col[]>([
		...result.dims.map((d, i) => ({
			key: `d${i}`,
			label: d.label,
			num: false,
			get: (r: AnalyticsRow) => dimText(result, r, i)
		})),
		...(result.measures.length
			? fields.map((f, i) => ({
					key: `m${i}`,
					label: f.label,
					num: true,
					get: (r: AnalyticsRow) => r.values[i]
				}))
			: []),
		{ key: 'n', label: t('analytics.col.n'), num: true, get: (r: AnalyticsRow) => r.n },
		...(scored
			? [
					{
						key: 'score',
						label: t('analytics.col.score'),
						num: true,
						get: (r: AnalyticsRow) => r.score ?? null
					}
				]
			: [])
	]);

	let sort = $state<{ key: string; desc: boolean } | null>(null);

	function sortBy(key: string) {
		sort = sort?.key === key ? { key, desc: !sort.desc } : { key, desc: false };
	}

	// Missing values sink to the bottom whichever way it sorts.
	const rows = $derived.by(() => {
		const col = cols.find((c) => c.key === sort?.key);
		if (!sort || !col) return result.rows;
		const dir = sort.desc ? -1 : 1;
		return [...result.rows].sort((a, b) => {
			const x = col.get(a);
			const y = col.get(b);
			if (x == null || y == null) return x == null ? (y == null ? 0 : 1) : -1;
			return dir * (typeof x === 'number' ? x - (y as number) : x.localeCompare(String(y)));
		});
	});

	const cell = (c: Col, r: AnalyticsRow): string => {
		const v = c.get(r);
		if (!c.num) return v as string;
		if (c.key === 'score') return fmtValue('count', v == null ? null : Math.round(v as number));
		if (c.key === 'n') return String(v);
		return fmtValue(fields[Number(c.key.slice(1))].type, v as number | null);
	};
</script>

<div class="tv">
	<table>
		<thead>
			<tr>
				{#each cols as c (c.key)}
					<th
						class:numc={c.num}
						aria-sort={sort?.key === c.key ? (sort.desc ? 'descending' : 'ascending') : 'none'}
					>
						<button type="button" class="btn-ghost btn-sm" onclick={() => sortBy(c.key)}
							>{c.label}{#if sort?.key === c.key}<span aria-hidden="true"
									>{sort.desc ? ' ↓' : ' ↑'}</span
								>{/if}</button
						>
					</th>
				{/each}
			</tr>
		</thead>
		<tbody>
			{#each rows as r, ri (ri)}
				<tr class:small={r.small}>
					{#each cols as c, ci (c.key)}
						<td class:numc={c.num} class:num={c.num}>
							{#if ci === 0}
								<button
									type="button"
									class="linklike stretch"
									title={t('analytics.viz.drillRow')}
									onclick={() => ondrill(r)}>{cell(c, r)}</button
								>
							{:else}
								{cell(c, r)}
							{/if}
						</td>
					{/each}
				</tr>
			{/each}
		</tbody>
	</table>
	{#if result.truncated}<p class="faint caption">{t('analytics.viz.truncated')}</p>{/if}
</div>

<style>
	.tv {
		overflow: auto;
		max-height: 420px;
	}
	table {
		width: 100%;
	}
	tr {
		position: relative;
	}
	.numc {
		text-align: right;
	}
	.small td {
		color: var(--text-muted);
	}
	th button {
		font: inherit;
		padding: 0;
	}
</style>
