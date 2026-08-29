<script lang="ts">
  import type { ResourceSample } from '$lib/domain';

  // Sparkline charts of the run's workspace resource usage (CPU / memory /
  // network) from the engine's ~10s sample window. Hand-rolled SVG: the
  // series are small and the app has no chart dependency. The crosshair is
  // shared across charts (same x = same sample time); hovered values replace
  // the "now" readout in each chart's header.
  let { samples }: { samples: ResourceSample[] } = $props();

  const H = 64; // plot height
  const PAD = 5; // top inset so the 2px stroke never clips
  const BASE = H - 1; // baseline y

  let widths = $state<number[]>([]);
  let hoverIdx = $state<number | null>(null);

  const times = $derived(samples.map((s) => Date.parse(s.at)));

  type Series = { label: string; color: string; vals: (number | null)[] };
  type Chart = {
    title: string;
    sub?: string; // muted context beside the title (e.g. the CPU core count)
    yMax: number;
    fmt: (v: number) => string;
    pctOf?: number; // denominator: readouts also show (v/pctOf) as a percent
    series: Series[];
  };

  // Per-second rates from a cumulative counter; a negative delta is a counter
  // reset (container restart) and reads 0, a non-monotonic timestamp is a gap.
  function rates(cum: number[]): (number | null)[] {
    return cum.map((v, i) => {
      if (i === 0) return null;
      const dt = (times[i] - times[i - 1]) / 1000;
      if (dt <= 0) return null;
      return Math.max(0, (v - cum[i - 1]) / dt);
    });
  }

  // Round a raw maximum up to a clean 1/2/5×10^k so the scale label reads well.
  function nice(v: number): number {
    if (v <= 0) return 1;
    const p = Math.pow(10, Math.floor(Math.log10(v)));
    for (const m of [1, 2, 5]) if (v <= m * p) return m * p;
    return 10 * p;
  }

  // Same, but snapped within 1024-based units so fmtBytes prints a clean
  // label (plain nice(1e6) would read "977KB").
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

  function fmtBytes(n: number): string {
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) {
      n /= 1024;
      i++;
    }
    return `${n < 10 && i > 0 ? n.toFixed(1) : Math.round(n)}${u[i]}`;
  }
  const fmtPct = (v: number) => `${v >= 10 ? Math.round(v) : v.toFixed(1)}%`;
  const fmtRate = (v: number) => `${fmtBytes(v)}/s`;

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
        title: 'CPU',
        sub: cpus ? `% of ${cpus} cores` : undefined,
        yMax: cpus
          ? Math.min(100, nice(Math.max(...cpuShown, 1)))
          : Math.max(100, Math.ceil(Math.max(...cpu, 0) / 100) * 100),
        fmt: fmtPct,
        series: [{ label: 'cpu', color: 'var(--blue)', vals: cpuShown }]
      },
      {
        title: 'Memory',
        yMax: memLimit || niceBytes(Math.max(...mem, 1)),
        fmt: fmtBytes,
        pctOf: memLimit || undefined,
        series: [{ label: 'used', color: 'var(--accent)', vals: mem }]
      },
      {
        title: 'Network',
        yMax: niceBytes(Math.max(...[...rx, ...tx].map((v) => v ?? 0), 1)),
        fmt: fmtRate,
        series: [
          { label: 'in', color: 'var(--green)', vals: rx },
          { label: 'out', color: 'var(--amber)', vals: tx }
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
    const t = times[0] + ((e.clientX - r.left) / r.width) * span;
    let best = 0;
    for (let i = 1; i < times.length; i++) {
      if (Math.abs(times[i] - t) < Math.abs(times[best] - t)) best = i;
    }
    hoverIdx = best;
  }

  const windowLabel = $derived.by(() => {
    const m = Math.round(span / 60000);
    return m < 1 ? 'last minute' : m < 60 ? `last ${m}m` : `last ${Math.round(m / 60)}h`;
  });
</script>

{#if samples.length < 2}
  <p class="muted small" style="margin:8px 0 0">
    Collecting resource samples… (one every ~10s; needs a live workspace)
  </p>
{:else}
  <div class="grid">
    {#each charts as c, ci (c.title)}
      <div class="chart">
        <div class="head small">
          <span class="muted">{c.title}{#if c.sub}&nbsp;· {c.sub}{/if}</span>
          <span class="vals">
            {#each c.series as s (s.label)}
              {@const v = readout(s)}
              <span>
                {#if c.series.length > 1}<span class="key" style="background:{s.color}"
                  ></span><span class="muted">{s.label}&nbsp;</span>{/if}{v == null
                  ? '—'
                  : c.fmt(v)}{#if c.pctOf && v != null}
                  <span class="muted">({Math.round((v / c.pctOf) * 100)}%)</span>{/if}
              </span>
            {/each}
          </span>
        </div>
        <div class="plot" bind:clientWidth={widths[ci]}>
          <span class="ymax muted">{c.fmt(c.yMax)}</span>
          {#if (widths[ci] ?? 0) > 10}
            {@const w = widths[ci]}
            <svg
              width={w}
              height={H}
              role="img"
              aria-label={`${c.title} usage, ${windowLabel}`}
              onmousemove={move}
              onmouseleave={() => (hoverIdx = null)}
            >
              <line x1="0" x2={w} y1={PAD} y2={PAD} class="grid-line" />
              <line x1="0" x2={w} y1={(PAD + BASE) / 2} y2={(PAD + BASE) / 2} class="grid-line" />
              <line x1="0" x2={w} y1={BASE} y2={BASE} class="base-line" />
              {#each c.series as s (s.label)}
                <path d={areaPath(s.vals, w, c.yMax)} fill={s.color} opacity="0.1" />
                <path
                  d={linePath(s.vals, w, c.yMax)}
                  fill="none"
                  stroke={s.color}
                  stroke-width="2"
                  stroke-linejoin="round"
                  stroke-linecap="round"
                />
              {/each}
              {#if hoverIdx != null}
                <line x1={x(hoverIdx, w)} x2={x(hoverIdx, w)} y1={PAD} y2={BASE} class="crosshair" />
                {#each c.series as s (s.label)}
                  {#if s.vals[hoverIdx] != null}
                    <circle
                      cx={x(hoverIdx, w)}
                      cy={y(s.vals[hoverIdx]!, c.yMax)}
                      r="4"
                      fill={s.color}
                      class="marker"
                    />
                  {/if}
                {/each}
              {/if}
            </svg>
          {/if}
        </div>
      </div>
    {/each}
  </div>
  <div class="foot small muted">
    <span>
      {hoverIdx != null
        ? `at ${new Date(times[hoverIdx]).toLocaleTimeString()}`
        : `${windowLabel} · sampled every ~10s`}
    </span>
    {#if samples.at(-1)?.pids}<span>{samples.at(-1)!.pids} processes</span>{/if}
  </div>
{/if}

<style>
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 12px;
    margin-top: 8px;
  }
  .chart {
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 8px 10px 6px;
    min-width: 0;
  }
  .head {
    display: flex;
    justify-content: space-between;
    gap: 8px;
    white-space: nowrap;
  }
  .vals {
    display: flex;
    gap: 10px;
    font-variant-numeric: tabular-nums;
  }
  .key {
    display: inline-block;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    margin-right: 4px;
    vertical-align: baseline;
  }
  .plot {
    position: relative;
    margin-top: 4px;
  }
  .plot svg {
    display: block;
  }
  .ymax {
    position: absolute;
    top: 0;
    left: 2px;
    font-size: 10px;
    line-height: 1;
    pointer-events: none;
  }
  .grid-line {
    stroke: var(--border);
    stroke-width: 1;
    opacity: 0.5;
  }
  .base-line {
    stroke: var(--border);
    stroke-width: 1;
  }
  .crosshair {
    stroke: var(--muted);
    stroke-width: 1;
    opacity: 0.6;
  }
  .marker {
    stroke: var(--panel);
    stroke-width: 2;
  }
  .foot {
    display: flex;
    justify-content: space-between;
    margin-top: 6px;
  }
</style>
