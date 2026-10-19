<script lang="ts">
	// Vitals (DESIGN 4.16): CPU, memory and network as three sparkline tiles
	// sharing one crosshair.
	import Sparkline from '$lib/ui/Sparkline.svelte';
	import { fmtBytes } from '$lib/format';
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
	// Per-second rates from the cumulative counters; a reset reads 0.
	const rate = (k: 'netRx' | 'netTx') =>
		s.map((x, i) => {
			if (i === 0) return 0;
			const dt = (times[i] - times[i - 1]) / 1000;
			return dt > 0 ? Math.max(0, (x[k] - s[i - 1][k]) / dt) : 0;
		});
	const rx = $derived(rate('netRx'));
	const tx = $derived(rate('netTx'));
	const pct = (v: number) => `${v >= 10 ? Math.round(v) : v.toFixed(1)}%`;
	const bytes = (v: number) => fmtBytes(Math.round(v)) || '0 B';
	const rateFmt = (v: number) => `${bytes(v)}/s`;
</script>

<div class="vt">
	<Sparkline
		label="CPU"
		hue="var(--az)"
		value={cpu.length ? pct(cpu.at(-1)!) : '—'}
		series={cpu}
		{times}
		fmt={pct}
		bind:hover
	/>
	<Sparkline
		label="Memory"
		hue="var(--vio)"
		value={mem.length ? `${bytes(mem.at(-1)!)}${memLimit ? ` / ${bytes(memLimit)}` : ''}` : '—'}
		series={mem}
		{times}
		fmt={bytes}
		bind:hover
	/>
	<Sparkline
		label="Network in / out"
		hue="var(--tea)"
		value={rx.length > 1 ? `${rateFmt(rx.at(-1)!)} · ${rateFmt(tx.at(-1)!)}` : '—'}
		series={rx}
		series2={tx}
		{times}
		fmt={rateFmt}
		bind:hover
	/>
</div>
{#if !rs.hasWorkspace && s.length < 2}
	<div class="gone">No live workspace to sample.</div>
{/if}

<style>
	.vt {
		flex: none;
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 6px;
		padding: 8px;
	}
	.gone {
		padding: 0 12px;
		font-size: 11.5px;
		color: var(--fnt);
	}
</style>
