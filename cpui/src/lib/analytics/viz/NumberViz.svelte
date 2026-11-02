<script lang="ts">
	// One figure: the first value of the first row (the count when the query
	// has no measures), with how many rows of facts stand behind it.
	import type { AnalyticsResult } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import { fmtValue } from '../format';
	import { valueFields, valuesOf } from './shape';

	let { result }: { result: AnalyticsResult } = $props();

	const row = $derived(result.rows[0]);
	const field = $derived(valueFields(result)[0]);
</script>

<div class="nv">
	<span class="big num">{fmtValue(field.type, row ? valuesOf(result, row)[0] : null)}</span>
	<span class="faint caption">{t('analytics.viz.n', { n: row?.n ?? 0 })}</span>
</div>

<style>
	.nv {
		display: flex;
		flex-direction: column;
		gap: 4px;
		padding: 6px 2px;
	}
	.big {
		font-family: var(--font-display);
		font-size: var(--fs-h1);
		line-height: 1.1;
		color: var(--text);
	}
</style>
