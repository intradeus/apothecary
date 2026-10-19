<script lang="ts">
	// Sparkline tile (DESIGN 4.16): label, mono value, 120×26 polyline + 15%
	// area. A second series renders dashed (network out). `hover` is a shared
	// index so sibling tiles show one crosshair; bind it across tiles.
	let {
		label,
		value,
		series,
		series2,
		hue = 'var(--az)',
		max,
		hover = $bindable(null),
		times = [],
		fmt = (v: number) => String(Math.round(v))
	}: {
		label: string;
		value: string;
		series: number[];
		series2?: number[];
		hue?: string;
		/** Y ceiling; defaults to the data's own max. */
		max?: number;
		hover?: number | null;
		/** Timestamps (ms) per point, for the tooltip. */
		times?: number[];
		fmt?: (v: number) => string;
	} = $props();

	const W = 120;
	const H = 26;
	const top = $derived(Math.max(max ?? 0, ...series, ...(series2 ?? []), 0.0001));
	const x = (i: number, n: number) => (n <= 1 ? W : (i / (n - 1)) * W);
	const y = (v: number) => H - 1 - (Math.max(0, v) / top) * (H - 3);
	const pts = (s: number[]) =>
		s.map((v, i) => `${x(i, s.length).toFixed(1)},${y(v).toFixed(1)}`).join(' ');
	const area = (s: number[]) => `0,${H} ${pts(s)} ${W},${H}`;
	const collecting = $derived(series.length < 2);

	let svg = $state<SVGSVGElement | null>(null);
	function move(e: MouseEvent) {
		if (!svg || collecting) return;
		const r = svg.getBoundingClientRect();
		const f = Math.min(1, Math.max(0, (e.clientX - r.left) / r.width));
		hover = Math.round(f * (series.length - 1));
	}
	const hx = $derived(
		hover !== null && !collecting ? x(Math.min(hover, series.length - 1), series.length) : null
	);
	const tip = $derived(
		hover !== null && !collecting
			? `${fmt(series[Math.min(hover, series.length - 1)])}${series2 ? ` / ${fmt(series2[Math.min(hover, series2.length - 1)] ?? 0)}` : ''}${times[hover] ? ` · ${new Date(times[hover]).toLocaleTimeString(undefined, { hour12: false })}` : ''}`
			: ''
	);
</script>

<div class="sp" style:--h={hue}>
	<div class="top">
		<span class="l">{label}</span>
		<span class="v mono">{tip || value}</span>
	</div>
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<svg
		bind:this={svg}
		viewBox="0 0 {W} {H}"
		preserveAspectRatio="none"
		onmousemove={move}
		onmouseleave={() => (hover = null)}
	>
		{#if collecting}
			<line x1="0" x2={W} y1={H - 2} y2={H - 2} class="base" />
		{:else}
			<polygon points={area(series)} class="area" />
			<polyline points={pts(series)} class="line" />
			{#if series2}<polyline points={pts(series2)} class="line dash" />{/if}
			{#if hx !== null}<line x1={hx} x2={hx} y1="0" y2={H} class="cross" />{/if}
		{/if}
	</svg>
	{#if collecting}<div class="col"><span class="d pulse"></span>Collecting samples…</div>{/if}
</div>

<style>
	.sp {
		background: var(--p2);
		border-radius: var(--r-card);
		padding: 7px 10px 6px;
		display: flex;
		flex-direction: column;
		gap: 3px;
		min-width: 0;
		position: relative;
	}
	.top {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
		gap: 8px;
	}
	.l {
		font-size: var(--fs-meta);
		color: var(--mut);
	}
	.v {
		font-size: var(--fs-body);
		font-weight: 600;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	svg {
		width: 100%;
		height: 26px;
		display: block;
		overflow: visible;
	}
	.line {
		fill: none;
		stroke: var(--h);
		stroke-width: 1.5;
		vector-effect: non-scaling-stroke;
	}
	.dash {
		stroke-dasharray: 3 3;
		opacity: 0.8;
	}
	.area {
		fill: color-mix(in oklch, var(--h) 15%, transparent);
	}
	.cross {
		stroke: var(--mut);
		stroke-width: 1;
		vector-effect: non-scaling-stroke;
	}
	.base {
		stroke: var(--ln2);
		stroke-dasharray: 3 4;
		vector-effect: non-scaling-stroke;
	}
	.col {
		position: absolute;
		left: 10px;
		bottom: 8px;
		font-size: var(--fs-id);
		color: var(--fnt);
		display: flex;
		align-items: center;
		gap: 5px;
	}
	.col .d {
		width: 5px;
		height: 5px;
		border-radius: 50%;
		background: var(--az);
	}
</style>
