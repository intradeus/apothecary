<svelte:options namespace="svg" />

<script lang="ts">
	// One sandbox on the war room's map: a state ring in its run state's hue,
	// HP-style arcs for CPU (left) and memory (right), the pilot's badge while
	// an agent is on station, and its drones (subagents) circling it.
	// Everything stays within UNIT_REACH of its slot.
	import type { SwarmUnit } from '$lib/domain';
	import { stateLabel } from '$lib/core/format';
	import { t } from '$lib/i18n/index.svelte';
	import type { Point } from './hexLayout';
	import { UNIT_HUE, alerting, cpuFraction, drones, memFraction } from './stats';

	let {
		unit,
		at,
		selected = false,
		onselect
	}: {
		unit: SwarmUnit;
		at: Point;
		selected?: boolean;
		onselect: (runId: string) => void;
	} = $props();

	/** Radii, inside UNIT_REACH (30). */
	const RING = 13;
	const BAR = 18;
	const SELECT = 22;
	const ORBIT = 26;
	/** Each bar's sweep, in degrees. */
	const SPAN = 120;
	const LABEL_MAX = 12;

	const LIVE = new Set(['running', 'provisioning', 'waiting_on_human']);

	const hue = $derived(UNIT_HUE[unit.state]);
	const cpu = $derived(cpuFraction(unit.resources));
	const mem = $derived(memFraction(unit.resources));
	const droneCount = $derived(drones(unit));
	const alert = $derived(alerting(unit));
	const label = $derived(
		unit.title.length > LABEL_MAX ? `${unit.title.slice(0, LABEL_MAX - 1)}…` : unit.title
	);
	const initials = $derived(
		unit.crew ? `${unit.crew.harness.charAt(0)}${unit.crew.agent.charAt(0)}`.toUpperCase() : ''
	);
	const aria = $derived(
		[
			unit.title,
			stateLabel[unit.state],
			unit.crew ? t('swarm.unit.agent', { agent: unit.crew.agent }) : '',
			t('swarm.unit.drones', { count: droneCount }),
			alert ? t('swarm.unit.alert') : ''
		]
			.filter(Boolean)
			.join(', ')
	);

	/** A point on the circle of radius r at an angle (degrees), as "x y". */
	function pt(r: number, deg: number) {
		const a = (deg * Math.PI) / 180;
		return `${(r * Math.cos(a)).toFixed(2)} ${(r * Math.sin(a)).toFixed(2)}`;
	}
	/** An arc of radius r from one angle to another (degrees, clockwise when
	 *  `to` is larger). */
	const arc = (r: number, from: number, to: number) =>
		`M${pt(r, from)} A${r} ${r} 0 ${Math.abs(to - from) > 180 ? 1 : 0} ${to > from ? 1 : 0} ${pt(r, to)}`;

	// CPU fills the left side bottom-up, memory the right side bottom-up.
	const cpuTrack = arc(BAR, 180 - SPAN / 2, 180 + SPAN / 2);
	const memTrack = arc(BAR, SPAN / 2, -SPAN / 2);
	const cpuBar = $derived(cpu ? arc(BAR, 180 - SPAN / 2, 180 - SPAN / 2 + SPAN * cpu) : '');
	const memBar = $derived(mem ? arc(BAR, SPAN / 2, SPAN / 2 - SPAN * mem) : '');

	function key(e: KeyboardEvent) {
		if (e.key !== 'Enter' && e.key !== ' ') return;
		e.preventDefault();
		onselect(unit.runId);
	}
</script>

<g
	class="unit tone-{hue}"
	class:selected
	class:live={LIVE.has(unit.state)}
	class:held={unit.held}
	transform="translate({at.x} {at.y})"
	role="button"
	tabindex="0"
	aria-label={aria}
	aria-pressed={selected}
	onclick={() => onselect(unit.runId)}
	onkeydown={key}
>
	<circle class="select-ring" r={SELECT} />
	<circle class="halo" r={RING} />

	<path class="track" class:empty={cpu === undefined} d={cpuTrack} />
	{#if cpuBar}<path class="bar cpu" d={cpuBar} />{/if}
	<path class="track" class:empty={mem === undefined} d={memTrack} />
	{#if memBar}<path class="bar mem" d={memBar} />{/if}

	<circle class="core" r={RING - 4} />
	<circle class="ring" r={RING} />

	{#if unit.crew}
		<g class="pilot">
			<path class="chevron" d="M-3.5 -3 L0 -6.2 L3.5 -3" />
			<text class="initials" y="4">{initials}</text>
		</g>
	{/if}

	{#if alert}
		<g class="alert tone-{unit.state === 'failed' ? 'danger' : 'attention'}">
			<path d="M11 -18 L15 -14 L11 -10 L7 -14 Z" />
		</g>
	{/if}

	{#if droneCount > 0}
		<g class="orbit">
			<circle class="orbit-bound" r={ORBIT + 3} />
			<circle class="orbit-track" r={ORBIT} />
			{#each Array(droneCount) as _, i (i)}
				<path
					class="drone"
					transform="rotate({(360 / droneCount) * i}) translate({ORBIT} 0)"
					d="M0 -2.6 L2.2 1.8 L-2.2 1.8 Z"
				/>
			{/each}
		</g>
	{/if}

	<text class="name" y={ORBIT + 12}>{label}</text>
	{#if unit.crew}<text class="stage" y={ORBIT + 22}>{unit.crew.stageId}</text>{/if}
</g>

<style>
	.unit {
		cursor: pointer;
		outline: none;
	}
	.select-ring {
		fill: none;
		stroke: var(--tone);
		stroke-width: 1.5;
		opacity: 0;
		transition: opacity var(--dur-fade) var(--ease-out);
	}
	.unit:hover .select-ring {
		opacity: 0.35;
	}
	.unit:focus-visible .select-ring {
		opacity: 0.8;
		stroke-dasharray: 3 3;
	}
	.selected .select-ring {
		opacity: 1;
		stroke-dasharray: none;
		filter: drop-shadow(0 0 var(--glow-xs) var(--tone));
	}
	.ring {
		fill: none;
		stroke: var(--tone);
		stroke-width: 2.5;
	}
	.held .ring {
		stroke-dasharray: 4 2;
	}
	.core {
		fill: color-mix(in oklch, var(--tone) 22%, transparent);
	}
	.halo {
		fill: none;
		stroke: var(--tone);
		stroke-width: 1.5;
		opacity: 0;
		transform-box: fill-box;
		transform-origin: center;
	}
	.live .halo {
		animation: unit-pulse var(--loop-pulse-slow) var(--ease-out) infinite;
	}
	.track {
		fill: none;
		stroke: var(--term-line);
		stroke-width: 3;
		stroke-linecap: round;
	}
	.track.empty {
		stroke: var(--term-faint);
		stroke-width: 1;
		stroke-dasharray: 2 3;
		opacity: 0.6;
	}
	.bar {
		fill: none;
		stroke-width: 3;
		stroke-linecap: round;
	}
	.bar.cpu {
		stroke: var(--running);
	}
	.bar.mem {
		stroke: var(--warning);
	}
	.chevron {
		fill: none;
		stroke: var(--term-text);
		stroke-width: 1.4;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.initials {
		fill: var(--term-text);
		font-family: var(--font-code);
		font-size: var(--fs-badge);
		font-weight: 600;
		text-anchor: middle;
	}
	.alert path {
		fill: var(--tone);
		stroke: var(--term-black);
		stroke-width: 1;
		filter: drop-shadow(0 0 var(--glow-xs) var(--tone));
	}
	.orbit {
		transform-box: fill-box;
		transform-origin: center;
		animation: unit-orbit var(--loop-orbit) linear infinite;
	}
	.orbit-bound {
		fill: none;
		stroke: none;
	}
	.orbit-track {
		fill: none;
		stroke: var(--term-line);
		stroke-width: 1;
		stroke-dasharray: 1 4;
	}
	.drone {
		fill: var(--accent);
		filter: drop-shadow(0 0 var(--glow-xs) var(--accent));
	}
	.name,
	.stage {
		font-family: var(--font-code);
		font-size: var(--fs-badge);
		text-anchor: middle;
		pointer-events: none;
	}
	.name {
		fill: var(--term-muted);
	}
	.selected .name {
		fill: var(--term-text);
	}
	.stage {
		fill: var(--term-faint);
		font-variant-caps: all-small-caps;
		letter-spacing: var(--tr-small);
	}

	@keyframes unit-pulse {
		from {
			opacity: 0.7;
			transform: scale(1);
		}
		to {
			opacity: 0;
			transform: scale(1.6);
		}
	}
	@keyframes unit-orbit {
		to {
			transform: rotate(360deg);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.live .halo,
		.orbit {
			animation: none;
		}
	}
</style>
