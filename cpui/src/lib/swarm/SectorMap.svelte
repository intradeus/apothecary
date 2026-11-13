<script lang="ts">
	// The war room's map: one hex sector per workshop, the units inside it, a
	// grid and range rings for the backdrop and a radar sweep circling the
	// board. Layout is pure (hexLayout); this only draws it.
	import type { SwarmUnit } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import { layoutSwarm } from './hexLayout';
	import Unit from './Unit.svelte';

	let {
		units,
		nameOf,
		selected = null,
		onselect
	}: {
		units: readonly SwarmUnit[];
		nameOf: (projectId: string) => string;
		selected?: string | null;
		onselect: (runId: string) => void;
	} = $props();

	const uid = $props.id();
	const GRID = 38;
	/** Range rings out to the sweep's reach. */
	const RINGS = 4;
	/** The sweep's trailing wedge, degrees, drawn in slices that fade away
	 *  from the beam (SVG has no conic gradient). */
	const WEDGE = 40;
	const SLICES = 8;

	const layout = $derived(layoutSwarm(units, nameOf));
	const byId = $derived(new Map(units.map((u) => [u.runId, u])));
	const vb = $derived(layout.viewBox);
	const cx = $derived(vb.x + vb.width / 2);
	const cy = $derived(vb.y + vb.height / 2);
	const reach = $derived(Math.hypot(vb.width, vb.height) / 2);
	const slices = $derived.by(() => {
		const step = WEDGE / SLICES;
		const at = (deg: number) => {
			const a = (-deg * Math.PI) / 180;
			return `${reach * Math.cos(a)} ${reach * Math.sin(a)}`;
		};
		return Array.from({ length: SLICES }, (_, i) => ({
			d: `M0 0 L${at(i * step)} A${reach} ${reach} 0 0 0 ${at((i + 1) * step)} Z`,
			opacity: 1 - i / SLICES
		}));
	});
</script>

<svg
	class="map"
	viewBox="{vb.x} {vb.y} {vb.width} {vb.height}"
	preserveAspectRatio="xMidYMid meet"
	role="group"
	aria-label={t('swarm.map.label')}
>
	<defs>
		<pattern id="{uid}-grid" width={GRID} height={GRID} patternUnits="userSpaceOnUse">
			<path class="grid-line" d="M{GRID} 0 L0 0 L0 {GRID}" />
		</pattern>
	</defs>

	<g class="backdrop" aria-hidden="true">
		<rect x={vb.x} y={vb.y} width={vb.width} height={vb.height} fill="url(#{uid}-grid)" />
		<g transform="translate({cx} {cy})">
			{#each Array(RINGS) as _, i (i)}
				<circle class="range" r={(reach / RINGS) * (i + 1)} />
			{/each}
			<line class="cross" x1={-reach} x2={reach} />
			<line class="cross" y1={-reach} y2={reach} />
			<g class="sweep">
				<circle class="sweep-bound" r={reach} />
				{#each slices as s, i (i)}
					<path class="wedge" d={s.d} opacity={s.opacity} />
				{/each}
				<line class="beam" x2={reach} />
			</g>
		</g>
	</g>

	{#each layout.sectors as s (s.projectId)}
		<g class="sector" class:alt={s.index % 2 === 1}>
			<polygon points={s.corners.map((p) => `${p.x},${p.y}`).join(' ')} />
			<text class="sector-name" x={s.label.x} y={s.label.y}>{s.name}</text>
		</g>
	{/each}

	{#each layout.sectors as s (s.projectId)}
		{#each s.units as slot (slot.runId)}
			{@const unit = byId.get(slot.runId)}
			{#if unit}
				<Unit {unit} at={slot.at} selected={selected === slot.runId} {onselect} />
			{/if}
		{/each}
	{/each}
</svg>

<style>
	.map {
		display: block;
		width: 100%;
		height: 100%;
	}
	.grid-line {
		fill: none;
		stroke: var(--term-line);
		stroke-width: 1;
	}
	.range {
		fill: none;
		stroke: var(--term-button-line);
		stroke-width: 1;
		stroke-dasharray: 2 6;
		opacity: 0.6;
	}
	.cross {
		stroke: var(--term-line);
		stroke-width: 1;
	}
	.sweep {
		transform-box: fill-box;
		transform-origin: center;
		animation: map-sweep var(--loop-sweep) linear infinite;
	}
	.sweep-bound {
		fill: none;
		stroke: none;
	}
	.wedge {
		fill: var(--term-button-bg);
	}
	.beam {
		stroke: var(--term-selection);
		stroke-width: 1.5;
	}
	.sector polygon {
		fill: var(--accent-bg);
		stroke: var(--accent-line);
		stroke-width: 1.5;
		stroke-linejoin: round;
	}
	.sector.alt polygon {
		fill: var(--accent-2-bg);
		stroke: var(--accent-2-line);
	}
	.sector-name {
		fill: var(--accent);
		font-family: var(--font-code);
		font-size: var(--fs-id);
		font-variant-caps: all-small-caps;
		letter-spacing: 0.12em;
		text-anchor: middle;
		dominant-baseline: middle;
	}
	.sector.alt .sector-name {
		fill: var(--accent-2);
	}

	@keyframes map-sweep {
		to {
			transform: rotate(360deg);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.sweep {
			animation: none;
		}
	}
</style>
