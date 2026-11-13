<script lang="ts">
	// A unit's dossier: the side pane the war room opens on a selected unit.
	// What the run is, who is flying it, and its live telemetry, reloaded
	// every ~10 s (the engine's sample window) while it is open.
	import { api } from '$lib/api';
	import { ago } from '$lib/core/format';
	import { errorMessage } from '$lib/core/errors';
	import { to } from '$lib/core/urls';
	import { I } from '$lib/design/icons';
	import type { ResourceSample, SwarmUnit } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import ResourceGraphs from '$lib/run/ResourceGraphs.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';

	let {
		unit,
		nameOf,
		onclose
	}: { unit: SwarmUnit; nameOf: (projectId: string) => string; onclose: () => void } = $props();

	const RELOAD_MS = 10_000;

	let samples = $state<ResourceSample[] | null>(null);
	let error = $state('');

	const runId = $derived(unit.runId);
	const crew = $derived(unit.crew);

	$effect(() => {
		const id = runId;
		let live = true;
		samples = null;
		error = '';
		const load = async () => {
			try {
				const got = await api.runs.resources(id);
				if (!live) return;
				samples = got;
				error = '';
			} catch (e) {
				if (live) error = errorMessage(e);
			}
		};
		void load();
		const timer = setInterval(load, RELOAD_MS);
		return () => {
			live = false;
			clearInterval(timer);
		};
	});
</script>

<svelte:window
	onkeydown={(e) => {
		if (e.key === 'Escape') onclose();
	}}
/>

<aside class="dossier" aria-label={t('swarm.dossier.label')}>
	<header class="head">
		<div class="title-block">
			<h2 class="title">{unit.title}</h2>
			<StatusChip state={unit.state} size="sm" />
		</div>
		<button class="btn-icon" aria-label={t('swarm.dossier.close')} onclick={onclose}
			>{I.close}</button
		>
	</header>

	<dl class="kv">
		<dt>{t('swarm.dossier.workshop')}</dt>
		<dd>{nameOf(unit.projectId)}</dd>
		<dt>{t('swarm.dossier.flow')}</dt>
		<dd class="mono">{unit.flowName}</dd>
		{#if unit.currentStage}
			<dt>{t('swarm.dossier.stage')}</dt>
			<dd class="mono">{unit.currentStage}</dd>
		{/if}
		{#if unit.phase}
			<dt>{t('swarm.dossier.phase')}</dt>
			<dd class="mono">{unit.phase}</dd>
		{/if}
		{#if unit.envStatus}
			<dt>{t('swarm.dossier.env')}</dt>
			<dd class="mono">{unit.envStatus}</dd>
		{/if}
		<dt>{t('swarm.dossier.pings')}</dt>
		<dd class:hot={unit.openPings > 0}>{unit.openPings}</dd>
		<dt>{t('swarm.dossier.held')}</dt>
		<dd>{unit.held ? t('swarm.dossier.yes') : t('swarm.dossier.no')}</dd>
	</dl>

	<section class="block">
		<h3>{t('swarm.dossier.crew')}</h3>
		{#if crew}
			<dl class="kv">
				<dt>{t('swarm.dossier.harness')}</dt>
				<dd class="mono">{crew.harness}</dd>
				<dt>{t('swarm.dossier.agent')}</dt>
				<dd class="mono">{crew.agent}</dd>
				<dt>{t('swarm.dossier.iteration')}</dt>
				<dd>{crew.iteration}</dd>
				<dt>{t('swarm.dossier.since')}</dt>
				<dd>{ago(crew.since)}</dd>
				<dt>{t('swarm.dossier.turn')}</dt>
				<dd>{crew.turnActive ? t('swarm.dossier.turnActive') : t('swarm.dossier.turnIdle')}</dd>
				{#if crew.tool}
					<dt>{t('swarm.dossier.tool')}</dt>
					<dd class="mono">{crew.tool}</dd>
				{/if}
			</dl>
			<h3>{t('swarm.dossier.drones')}</h3>
			{#if crew.subagents.length}
				<ul class="drones">
					{#each crew.subagents as s (s.toolCallId)}
						<li>
							<span class="truncate">{s.title}</span><span class="count">×{s.count}</span>
						</li>
					{/each}
				</ul>
			{:else}
				<p class="quiet">{t('swarm.dossier.noDrones')}</p>
			{/if}
		{:else}
			<p class="quiet">{t('swarm.dossier.noCrew')}</p>
		{/if}
	</section>

	<section class="block">
		<h3>{t('swarm.dossier.telemetry')}</h3>
		{#if samples}
			<ResourceGraphs {samples} />
		{:else if error}
			<p class="error">{t('swarm.dossier.loadError', { error })}</p>
		{:else}
			<Loading />
		{/if}
	</section>

	<a class="btn btn-sm open" href={to.run(unit.runId)}>{t('swarm.dossier.open')} {I.forward}</a>
</aside>

<style>
	.dossier {
		display: flex;
		flex-direction: column;
		gap: 14px;
		min-height: 0;
		padding: 12px 14px;
		overflow: auto;
		border-left: 1px solid var(--term-button-line);
		background: var(--term-bg);
		animation: dossier-in var(--dur-spring) var(--spring) both;
	}
	.head {
		display: flex;
		align-items: flex-start;
		gap: 8px;
	}
	.title-block {
		display: flex;
		flex: 1;
		flex-direction: column;
		align-items: flex-start;
		gap: 6px;
		min-width: 0;
	}
	.title {
		margin: 0;
		font-family: var(--font-display);
		font-size: var(--fs-section);
		overflow-wrap: anywhere;
	}
	h3 {
		margin: 0 0 6px;
		color: var(--term-muted);
		font-size: var(--fs-meta);
		font-variant-caps: all-small-caps;
		letter-spacing: 0.08em;
	}
	.block {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.hot {
		color: var(--attention);
		font-weight: 600;
	}
	.drones {
		display: flex;
		flex-direction: column;
		gap: 3px;
		margin: 0;
		padding: 0;
		list-style: none;
		font-size: var(--fs-help);
	}
	.drones li {
		display: flex;
		gap: 8px;
		padding-left: 8px;
		border-left: 2px solid var(--accent);
	}
	.count {
		color: var(--accent);
		font-family: var(--font-code);
	}
	.quiet {
		margin: 0;
		color: var(--term-faint);
		font-size: var(--fs-help);
	}
	.error {
		margin: 0;
		color: var(--danger);
		font-size: var(--fs-help);
	}
	.open {
		align-self: flex-start;
	}

	@keyframes dossier-in {
		from {
			opacity: 0;
			transform: translateX(24px);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.dossier {
			animation: none;
		}
	}
</style>
