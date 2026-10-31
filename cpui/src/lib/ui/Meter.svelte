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
		tone?: 'brand' | 'success';
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
	<div
		class="bar"
		role="meter"
		aria-label={label}
		aria-valuenow={value}
		aria-valuemin={0}
		aria-valuemax={max}
	>
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
		color: var(--text-muted);
	}
	.top {
		display: flex;
		justify-content: space-between;
		gap: 8px;
	}
	.v {
		color: var(--text);
		font-size: var(--fs-meta);
	}
	.bar {
		height: 5px;
		border-radius: var(--r-xs);
		background: var(--bar-track);
		overflow: hidden;
		display: flex;
	}
	.fill {
		transition: width var(--dur-spring) var(--spring);
	}
	.brand {
		background: var(--bar-brand);
	}
	.success {
		background: var(--bar-success);
	}
	.sub {
		color: var(--text-faint);
		font-size: var(--fs-help);
	}
</style>
