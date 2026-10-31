<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// A capacity meter: label, mono n/max, a bar. The bar's
	// width rides the spring when it changes.
	let {
		label,
		value,
		max,
		tone = 'brand',
		sub
	}: {
		label: string;
		value: number;
		max: number;
		tone?: 'brand' | 'grn';
		/** A line under the bar ("at the cap, new runs are refused"). */
		sub?: string;
	} = $props();

	const pct = $derived(max > 0 ? Math.min(100, Math.round((value / max) * 100)) : 0);
</script>

<div class="mt" title={t('ui.meter.title', { label, value, max: max || '–' })}>
	<div class="top">
		<span>{label}</span>
		<span class="mono v">{value}/{max || '–'}</span>
	</div>
	<div class="bar" role="meter" aria-valuenow={value} aria-valuemin={0} aria-valuemax={max}>
		<div class="fill {tone}" style:width="{pct}%"></div>
	</div>
	{#if sub}<div class="sub">{sub}</div>{/if}
</div>

<style>
	.mt {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
		font-size: var(--fs-meta);
		color: var(--mut);
	}
	.top {
		display: flex;
		justify-content: space-between;
		gap: 8px;
	}
	.v {
		color: var(--tx);
		font-size: var(--fs-meta);
	}
	.bar {
		height: 5px;
		border-radius: var(--r-xs);
		background: var(--barTrack);
		overflow: hidden;
		display: flex;
	}
	.fill {
		transition: width var(--dur-spring) var(--spring);
	}
	.brand {
		background: var(--barBrand);
	}
	.grn {
		background: var(--barGrn);
	}
	.sub {
		color: var(--fnt);
		font-size: var(--fs-help);
	}
</style>
