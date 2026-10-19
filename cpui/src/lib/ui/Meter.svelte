<script lang="ts">
	// A capacity meter: label, mono n/max, a bar. `rail` is the sidebar's
	// compact dark form. The bar's width rides the spring when it changes.
	let {
		label,
		value,
		max,
		tone = 'parch',
		sub,
		rail = false
	}: {
		label: string;
		value: number;
		max: number;
		tone?: 'parch' | 'grn';
		/** A line under the bar ("at the cap, new runs are refused"). */
		sub?: string;
		rail?: boolean;
	} = $props();

	const pct = $derived(max > 0 ? Math.min(100, Math.round((value / max) * 100)) : 0);
</script>

<div class="mt" class:rail title="{label}: {value} of {max || '–'}">
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
	.rail {
		color: var(--railMut);
	}
	.rail .v {
		color: var(--railTx);
	}
	.bar {
		height: 5px;
		border-radius: var(--r-xs);
		background: var(--railTrack);
		overflow: hidden;
		display: flex;
	}
	.fill {
		transition: width var(--dur-spring) var(--spring);
	}
	.parch {
		background: var(--barParch);
	}
	.grn {
		background: var(--barGrn);
	}
	.sub {
		color: var(--fnt);
		font-size: var(--fs-help);
	}
</style>
