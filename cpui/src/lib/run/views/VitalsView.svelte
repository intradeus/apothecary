<script lang="ts">
	// Vitals (DESIGN 4.16): CPU, memory and network as three sparkline tiles
	// sharing one crosshair.
	import { t } from '$lib/i18n/index.svelte';
	import Sparkline from '$lib/ui/Sparkline.svelte';
	import { counterRates, fmtBytes, fmtPct, fmtRate } from '$lib/format';
	import type { RunStore } from '../store.svelte';

	let { rs, visible }: { rs: RunStore; visible: boolean } = $props();

	$effect(() => {
		if (!visible) return;
		return rs.useResources();
	});

	let hover = $state<number | null>(null);

	const s = $derived(rs.samples);
	const times = $derived(s.map((x) => Date.parse(x.at)));
	const cpus = $derived(s.at(-1)?.cpus || 0);
	// % of the whole machine when the core count is known (docker reports % of one core).
	const cpu = $derived(s.map((x) => (cpus ? x.cpuPerc / cpus : x.cpuPerc)));
	const mem = $derived(s.map((x) => x.memUsed));
	const memLimit = $derived(s.at(-1)?.memLimit || 0);
	// Per-second rates from the cumulative counters (a gap reads 0 here: a
	// sparkline has no breaks).
	const rate = (k: 'netRx' | 'netTx') =>
		counterRates(
			s.map((x) => x[k]),
			times
		).map((v) => v ?? 0);
	const rx = $derived(rate('netRx'));
	const tx = $derived(rate('netTx'));
	const bytes = (v: number) => fmtBytes(Math.round(v)) || '0 B';
</script>

<div class="vt">
	<Sparkline
		label={t('runview.vitals.cpu')}
		hue="var(--az)"
		value={cpu.length ? fmtPct(cpu.at(-1)!) : '—'}
		series={cpu}
		{times}
		fmt={fmtPct}
		bind:hover
	/>
	<Sparkline
		label={t('runview.vitals.memory')}
		hue="var(--vio)"
		value={mem.length ? `${bytes(mem.at(-1)!)}${memLimit ? ` / ${bytes(memLimit)}` : ''}` : '—'}
		series={mem}
		{times}
		fmt={bytes}
		bind:hover
	/>
	<Sparkline
		label={t('runview.vitals.network')}
		hue="var(--tea)"
		value={rx.length > 1 ? `${fmtRate(rx.at(-1)!)} · ${fmtRate(tx.at(-1)!)}` : '—'}
		series={rx}
		series2={tx}
		{times}
		fmt={fmtRate}
		bind:hover
	/>
</div>
{#if !rs.hasWorkspace && s.length < 2}
	<div class="empty-line">{t('runview.vitals.no_workspace')}</div>
{/if}

<style>
	.vt {
		flex: none;
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 6px;
		padding: 8px;
	}
</style>
