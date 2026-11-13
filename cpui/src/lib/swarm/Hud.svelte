<script lang="ts">
	// The war room's HUD strip: the board's counters as a game would show
	// them (mono numerals, small-caps captions) and the deployment's slots as
	// energy cells.
	import { fmtBytes } from '$lib/core/format';
	import { t } from '$lib/i18n/index.svelte';
	import type { HudStats } from './stats';

	interface Slot {
		used: number;
		max: number;
	}

	let { stats, capacity }: { stats: HudStats; capacity: { runs: Slot; envs: Slot } } = $props();

	/** The most cells a meter draws; a bigger cap scales onto them. */
	const CELLS = 16;

	const unknown = $derived(stats.sampled === 0);
	const ram = $derived(
		stats.memLimit > 0
			? `${fmtBytes(stats.memUsed)} / ${fmtBytes(stats.memLimit)}`
			: fmtBytes(stats.memUsed)
	);

	/** Which of a meter's cells are lit. */
	function cells({ used, max }: Slot): boolean[] {
		const n = Math.min(max, CELLS);
		const lit = max <= CELLS ? used : Math.round((used / max) * n);
		return Array.from({ length: n }, (_, i) => i < lit);
	}
</script>

{#snippet meter(label: string, slot: Slot)}
	<div class="meter">
		<span class="caption-hud">{label}</span>
		{#if slot.max > 0}
			<span
				class="cells"
				class:full={slot.used >= slot.max}
				role="meter"
				aria-label={label}
				aria-valuemin={0}
				aria-valuemax={slot.max}
				aria-valuenow={Math.min(slot.used, slot.max)}
			>
				{#each cells(slot) as on, i (i)}<span class="cell" class:on></span>{/each}
			</span>
			<span class="value">{slot.used}/{slot.max}</span>
		{:else}
			<span class="value">{slot.used}</span>
		{/if}
	</div>
{/snippet}

<section class="hud" aria-label={t('swarm.hud.label')}>
	<dl class="counters">
		<div class="counter tone-success">
			<dt>{t('swarm.hud.online')}</dt>
			<dd>{stats.online}<span class="of">/{stats.units}</span></dd>
		</div>
		<div class="counter tone-running">
			<dt>{t('swarm.hud.cores')}</dt>
			<dd>{unknown ? t('swarm.hud.unknown') : stats.cores.toFixed(1)}</dd>
		</div>
		<div class="counter tone-warning">
			<dt>{t('swarm.hud.ram')}</dt>
			<dd>{unknown ? t('swarm.hud.unknown') : ram}</dd>
		</div>
		<div class="counter tone-brand">
			<dt>{t('swarm.hud.agents')}</dt>
			<dd>{stats.agents}</dd>
		</div>
		<div class="counter tone-accent">
			<dt>{t('swarm.hud.drones')}</dt>
			<dd>{stats.drones}</dd>
		</div>
		<div class="counter alerts {stats.alerts > 0 ? 'tone-danger hot' : 'tone-muted'}">
			<dt>{t('swarm.hud.alerts')}</dt>
			<dd>{stats.alerts}</dd>
		</div>
	</dl>
	<div class="meters">
		{@render meter(t('swarm.hud.runs'), capacity.runs)}
		{@render meter(t('swarm.hud.envs'), capacity.envs)}
	</div>
</section>

<style>
	.hud {
		display: flex;
		flex-wrap: wrap;
		align-items: stretch;
		gap: 10px 18px;
	}
	.counters {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		margin: 0;
	}
	.counter {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 84px;
		padding: 6px 10px 7px;
		border: 1px solid var(--term-line);
		border-left: 2px solid var(--tone);
		border-radius: var(--r-xs);
		background: var(--term-button-bg);
	}
	dt,
	.caption-hud {
		color: var(--term-muted);
		font-size: var(--fs-meta);
		font-variant-caps: all-small-caps;
		letter-spacing: 0.08em;
		white-space: nowrap;
	}
	dd {
		margin: 0;
		color: var(--tone);
		font-family: var(--font-code);
		font-size: var(--fs-lead);
		font-variant-numeric: tabular-nums;
		font-weight: 600;
		white-space: nowrap;
	}
	.of {
		color: var(--term-faint);
		font-size: var(--fs-help);
		font-weight: 400;
	}
	.hot dd {
		text-shadow: 0 0 var(--glow-sm) var(--tone);
	}
	.meters {
		display: flex;
		flex-direction: column;
		justify-content: center;
		gap: 6px;
	}
	.meter {
		display: grid;
		grid-template-columns: 84px auto auto;
		align-items: center;
		gap: 8px;
	}
	.cells {
		display: flex;
		gap: 2px;
	}
	.cell {
		width: 7px;
		height: 12px;
		border: 1px solid var(--term-button-line);
		border-radius: var(--r-xs);
		transform: skewX(-12deg);
	}
	.cell.on {
		border-color: var(--success);
		background: var(--success);
		box-shadow: 0 0 var(--glow-xs) var(--success-glow);
	}
	.full .cell.on {
		border-color: var(--warning);
		background: var(--warning);
		box-shadow: 0 0 var(--glow-xs) var(--warning-glow);
	}
	.value {
		color: var(--term-text);
		font-family: var(--font-code);
		font-size: var(--fs-help);
		font-variant-numeric: tabular-nums;
	}
</style>
