<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	import type { ResourceSample } from '$lib/domain';
	import { counterRates, fmtBytes, fmtPct, fmtRate, fmtTime } from '$lib/format';
	import EmptyState from '$lib/ui/EmptyState.svelte';

	// Charts of the run's workspace resource usage (CPU / memory /
	// network) from the engine's ~10s sample window. Hand-rolled SVG: the
	// series are small and the app has no chart dependency. The crosshair is
	// shared across charts (same x = same sample time); hovered values replace
	// the "now" readout in each chart's header.
	let { samples }: { samples: ResourceSample[] } = $props();

	const H = 82; // plot height
	const PAD = 5; // top inset so the 2px stroke never clips
	const BASE = H - 1; // baseline y

	let widths = $state<number[]>([]);
	let hoverIdx = $state<number | null>(null);

	const times = $derived(samples.map((s) => Date.parse(s.at)));

	type Series = { label: string; color: string; vals: (number | null)[]; dash?: boolean };
	type Chart = {
		title: string;
		sub?: string; // muted context beside the title (e.g. the CPU core count)
		yMax: number;
		fmt: (v: number) => string;
		pctOf?: number; // denominator: readouts also show (v/pctOf) as a percent
		series: Series[];
	};

	// Per-second rates from a cumulative counter (gaps and resets handled
	// in $lib/format).
	const rates = (cum: number[]) => counterRates(cum, times);

	// Round a raw maximum up to a clean 1/2/5×10^k so the scale label reads well.
	function nice(v: number): number {
		if (v <= 0) return 1;
		const p = Math.pow(10, Math.floor(Math.log10(v)));
		for (const m of [1, 2, 5]) if (v <= m * p) return m * p;
		return 10 * p;
	}

	// Same, but snapped within 1024-based units so the byte formatter prints
	// a clean label (plain nice(1e6) would read "977 KB").
	function niceBytes(v: number): number {
		if (v <= 1) return 1;
		let unit = Math.pow(1024, Math.floor(Math.log(v) / Math.log(1024)));
		let m = nice(v / unit);
		if (m >= 1000) {
			m = 1;
			unit *= 1024;
		}
		return m * unit;
	}

	const bytes = (v: number) => fmtBytes(Math.round(v)) || '0 B';

	const charts: Chart[] = $derived.by(() => {
		const cpu = samples.map((s) => s.cpuPerc);
		const mem = samples.map((s) => s.memUsed);
		const rx = rates(samples.map((s) => s.netRx));
		const tx = rates(samples.map((s) => s.netTx));
		const memLimit = samples.at(-1)?.memLimit || 0;
		const cpus = samples.at(-1)?.cpus || 0;
		// Docker reports % of ONE core (ceiling = cpus×100); shown as % of the
		// whole machine when the core count is known, raw per-core % otherwise.
		const cpuShown = cpus ? cpu.map((v) => v / cpus) : cpu;
		return [
			{
				title: t('runview.graphs.cpu'),
				sub: cpus ? t('runview.graphs.cpu_cores', { count: cpus }) : undefined,
				yMax: cpus
					? Math.min(100, nice(Math.max(...cpuShown, 1)))
					: Math.max(100, Math.ceil(Math.max(...cpu, 0) / 100) * 100),
				fmt: fmtPct,
				series: [{ label: t('runview.graphs.s_cpu'), color: 'var(--emb)', vals: cpuShown }]
			},
			{
				title: t('runview.graphs.memory'),
				yMax: memLimit || niceBytes(Math.max(...mem, 1)),
				fmt: bytes,
				pctOf: memLimit || undefined,
				series: [{ label: t('runview.graphs.s_used'), color: 'var(--gold)', vals: mem }]
			},
			{
				title: t('runview.graphs.network'),
				yMax: niceBytes(Math.max(...[...rx, ...tx].map((v) => v ?? 0), 1)),
				fmt: fmtRate,
				series: [
					{ label: t('runview.graphs.s_in'), color: 'var(--sky)', vals: rx },
					{ label: t('runview.graphs.s_out'), color: 'var(--sky)', vals: tx, dash: true }
				]
			}
		];
	});

	const span = $derived(Math.max(1, (times.at(-1) ?? 0) - (times[0] ?? 0)));
	const x = (i: number, w: number) => 1 + ((times[i] - times[0]) / span) * (w - 2);
	const y = (v: number, yMax: number) => PAD + (1 - Math.min(v, yMax) / yMax) * (BASE - PAD);

	function linePath(vals: (number | null)[], w: number, yMax: number): string {
		let d = '';
		let pen = false;
		vals.forEach((v, i) => {
			if (v == null) {
				pen = false;
				return;
			}
			d += `${pen ? 'L' : 'M'}${x(i, w).toFixed(1)},${y(v, yMax).toFixed(1)}`;
			pen = true;
		});
		return d;
	}

	// The wash under the line, one closed shape per gap-free segment.
	function areaPath(vals: (number | null)[], w: number, yMax: number): string {
		let d = '';
		let seg: number[] = [];
		const flush = () => {
			if (seg.length >= 2) {
				d += `M${x(seg[0], w).toFixed(1)},${BASE}`;
				for (const i of seg) d += `L${x(i, w).toFixed(1)},${y(vals[i]!, yMax).toFixed(1)}`;
				d += `L${x(seg[seg.length - 1], w).toFixed(1)},${BASE}Z`;
			}
			seg = [];
		};
		vals.forEach((v, i) => (v == null ? flush() : seg.push(i)));
		flush();
		return d;
	}

	function lastVal(vals: (number | null)[]): number | null {
		for (let i = vals.length - 1; i >= 0; i--) if (vals[i] != null) return vals[i];
		return null;
	}
	const readout = (s: Series) => (hoverIdx != null ? s.vals[hoverIdx] : lastVal(s.vals));

	function move(e: MouseEvent) {
		if (times.length < 2) return;
		const r = (e.currentTarget as SVGElement).getBoundingClientRect();
		const at = times[0] + ((e.clientX - r.left) / r.width) * span;
		let best = 0;
		for (let i = 1; i < times.length; i++) {
			if (Math.abs(times[i] - at) < Math.abs(times[best] - at)) best = i;
		}
		hoverIdx = best;
	}

	const windowLabel = $derived.by(() => {
		const m = Math.round(span / 60000);
		return m < 1
			? t('runview.graphs.last_minute')
			: m < 60
				? t('runview.graphs.last_m', { n: m })
				: t('runview.graphs.last_h', { n: Math.round(m / 60) });
	});
</script>

<div class="rg">
	<div class="top">
		<span
			>{samples.at(-1)?.pids
				? t('runview.graphs.top_pids', { count: samples.at(-1)!.pids })
				: t('runview.graphs.top')}</span
		>
		<span
			>{hoverIdx != null && times[hoverIdx]
				? t('runview.graphs.at', { time: fmtTime(times[hoverIdx]) })
				: windowLabel}</span
		>
	</div>
	{#if samples.length < 2}
		<EmptyState
			compact
			title={t('runview.graphs.collecting')}
			text={t('runview.graphs.collecting_text')}
		/>
	{:else}
		{#each charts as c, ci (c.title)}
			<div class="chart">
				<div class="head">
					<span class="name display">{c.title}</span>
					<span class="vals">
						{#each c.series as s (s.label)}
							{@const v = readout(s)}
							<span>
								{#if c.series.length > 1}<span
										class="key"
										class:dash={s.dash}
										style:border-color={s.color}
									></span><span class="fnt">{s.label}&nbsp;</span>{/if}{v == null
									? '—'
									: c.fmt(v)}{#if c.pctOf && v != null}
									<span class="fnt">({Math.round((v / c.pctOf) * 100)}%)</span>{/if}
							</span>
						{/each}
						{#if c.sub}<span class="fnt sub">{c.sub}</span>{/if}
					</span>
				</div>
				<div class="plot" bind:clientWidth={widths[ci]}>
					<span class="ymax">{c.fmt(c.yMax)}</span>
					{#if (widths[ci] ?? 0) > 10}
						{@const w = widths[ci]}
						<svg
							width={w}
							height={H}
							role="img"
							aria-label={t('runview.graphs.aria', { title: c.title, window: windowLabel })}
							onmousemove={move}
							onmouseleave={() => (hoverIdx = null)}
						>
							<line x1="0" x2={w} y1={(PAD + BASE) / 2} y2={(PAD + BASE) / 2} class="grid-line" />
							{#each c.series as s (s.label)}
								{#if !s.dash}<path
										d={areaPath(s.vals, w, c.yMax)}
										fill={s.color}
										opacity="0.16"
									/>{/if}
								<path
									d={linePath(s.vals, w, c.yMax)}
									fill="none"
									stroke={s.color}
									stroke-width={s.dash ? 1 : 1.75}
									stroke-dasharray={s.dash ? '3 3' : undefined}
									opacity={s.dash ? 0.75 : 1}
									stroke-linejoin="round"
									stroke-linecap="round"
								/>
							{/each}
							{#if hoverIdx != null}
								<line x1={x(hoverIdx, w)} x2={x(hoverIdx, w)} y1="0" y2={H} class="crosshair" />
								{#each c.series as s (s.label)}
									{#if s.vals[hoverIdx] != null}
										<circle
											cx={x(hoverIdx, w)}
											cy={y(s.vals[hoverIdx]!, c.yMax)}
											r="3"
											fill={s.color}
										/>
									{/if}
								{/each}
							{/if}
						</svg>
					{/if}
				</div>
			</div>
		{/each}
	{/if}
</div>

<style>
	.rg {
		padding: 2px 8px 10px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.top {
		display: flex;
		justify-content: space-between;
		gap: 10px;
		font-size: var(--fs-help);
		color: var(--fnt);
		padding: 0 4px;
	}
	.chart {
		border-radius: var(--r-card);
		background: var(--p2);
		display: flex;
		flex-direction: column;
		overflow: hidden;
		min-width: 0;
	}
	.head {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
		gap: 8px;
		padding: 8px 12px 2px;
		white-space: nowrap;
	}
	.name {
		font-size: var(--fs-title);
	}
	.vals {
		display: flex;
		gap: 10px;
		font-family: var(--mono);
		font-size: var(--fs-help);
		font-variant-numeric: tabular-nums;
		overflow: hidden;
	}
	.vals .fnt {
		font-family: var(--sans);
		font-size: var(--fs-meta);
	}
	.key {
		display: inline-block;
		width: 10px;
		height: 0;
		border-top: 2px solid;
		margin-right: 4px;
		vertical-align: middle;
	}
	.key.dash {
		border-top-style: dashed;
	}
	.plot {
		position: relative;
		cursor: crosshair;
	}
	.plot svg {
		display: block;
	}
	.ymax {
		position: absolute;
		top: 2px;
		left: 12px;
		font-size: var(--fs-id);
		line-height: 1;
		color: var(--fnt);
		pointer-events: none;
		font-family: var(--mono);
	}
	.grid-line {
		stroke: var(--ln);
		stroke-width: 1;
		stroke-dasharray: 2 4;
	}
	.crosshair {
		stroke: var(--mut);
		stroke-width: 1;
	}
</style>
