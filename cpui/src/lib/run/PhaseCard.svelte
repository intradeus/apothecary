<script lang="ts">
	// One chain of the run's flow as a card (FLOWLANG §1): the main chain, a
	// watcher or an outcome. What opens it, how it starts, how often it ran,
	// where it stands, what its signal holds for right now — and, for a
	// watcher, Run now with the reason it can or cannot start in plain view.
	// Its tone is the flow editor's chain marker (DESIGN §7.8).
	import { t } from '$lib/i18n/index.svelte';
	import type { RunPhase } from '$lib/domain';
	import TransportIcon from '$lib/ui/TransportIcon.svelte';

	let {
		phase,
		titles,
		ended,
		free,
		spent,
		busy,
		locked,
		onrun
	}: {
		phase: RunPhase;
		/** Signal slug → its title, once read. */
		titles: Record<string, string>;
		/** The run is over: what ran, without anything to start. */
		ended: boolean;
		/** No chain holds the run's one cursor. */
		free: boolean;
		/** It used up its runs. */
		spent: boolean;
		/** This one is starting. */
		busy: boolean;
		/** A start is in flight, this one's or another's. */
		locked: boolean;
		onrun: (ev: MouseEvent) => void;
	} = $props();

	const tone = $derived(
		phase.kind === 'main'
			? 'brand'
			: phase.kind === 'watcher'
				? 'accent'
				: phase.then === 'abort'
					? 'danger'
					: 'success'
	);
	const waits = $derived(phase.holds.filter((h) => !h.seen));
	const signal = $derived(titles[phase.signal] ?? phase.signal);
	// The quiet time to the second, as the flow wrote it: 90 s is "1m 30s", not "2m".
	function span(s: number): string {
		const parts: [number, string][] = [
			[Math.floor(s / 3600), 'h'],
			[Math.floor((s % 3600) / 60), 'm'],
			[s % 60, 's']
		];
		return parts
			.filter(([n]) => n > 0)
			.map(([n, u]) => `${n}${u}`)
			.join(' ');
	}
	const byHand = $derived(phase.kind !== 'main' && phase.signal === 'manual');
	const opens = $derived(
		phase.kind === 'main'
			? t('run.phases.main')
			: byHand
				? t('run.phases.byHand')
				: phase.quietForS
					? signal + t('run.phases.quiet', { duration: span(phase.quietForS) })
					: signal
	);
	const runs = $derived(
		phase.kind === 'watcher'
			? t('run.phases.usage', { used: phase.total, max: phase.max === -1 ? '∞' : phase.max })
			: t('run.phases.once')
	);
	// An outcome has no Run now (below), so its manual policy cannot point at one.
	const starts = $derived(
		phase.kind === 'outcome' && phase.start === 'manual'
			? t('run.phases.outcomeManual')
			: t(`run.phases.start.${phase.start}`)
	);
	const status = $derived(
		phase.live
			? {
					hue: phase.live.parkedAt ? 'tint-warning' : 'tint-running',
					word: phase.live.parkedAt
						? t('run.phases.parkedAt', { stage: phase.live.parkedAt })
						: t('run.phases.running'),
					pulse: !phase.live.parkedAt
				}
			: waits.length
				? { hue: 'tint-hot', word: t('run.phases.pending'), pulse: false }
				: spent
					? { hue: 'tint-muted', word: t('run.phases.maxReached'), pulse: false }
					: { hue: 'tint-muted', word: t('run.phases.idle'), pulse: false }
	);
	const disabled = $derived(!free || spent || locked);
	const why = $derived(
		phase.live
			? t('run.phases.live')
			: spent
				? t('run.phases.spent')
				: !free
					? t('run.phases.otherRunning')
					: locked
						? t('run.phases.starting')
						: t('run.phases.startNow')
	);
</script>

<article class="ph card-in tone-{tone}" class:live={!!phase.live} aria-label={phase.id}>
	<div class="head">
		<span class="id mono">{phase.id}</span>
		{#if phase.kind === 'outcome'}<span
				class="pill sm tint-{tone}"
				title={t('run.phases.outcomeTitle')}
				>{phase.then === 'abort' ? t('run.phases.endsAbort') : t('run.phases.endsDone')}</span
			>{/if}
		<span class="spacer"></span>
		<span class="pill sm {status.hue}"
			><span class="dot" class:pulse={status.pulse}></span>{status.word}</span
		>
	</div>

	{#if phase.description}<p class="desc">{phase.description}</p>{/if}

	<dl class="kv facts">
		<dt>{t('run.phases.k.opens')}</dt>
		<dd>{opens}</dd>
		{#if phase.kind !== 'main' && !byHand}
			<dt>{t('run.phases.k.starts')}</dt>
			<dd>{starts}</dd>
		{/if}
		<dt>{t('run.phases.k.runs')}</dt>
		<dd>{runs}</dd>
	</dl>

	{#if waits.length}
		<div class="holds">
			<span class="caption">{t('run.phases.holds')}</span>
			<ul>
				{#each waits as h (h.subject + h.key)}<li>{h.why}</li>{/each}
			</ul>
		</div>
	{/if}

	{#if phase.kind === 'main'}
		<div class="foot"><span class="help">{t('run.phases.mainFoot')}</span></div>
	{:else if ended}
		<!-- the run is over: what ran, without anything to start -->
	{:else if phase.kind === 'outcome'}
		<!-- An outcome happens on the repository; starting one by hand would
		     end the run from a button beside the ones that do not. -->
		<div class="foot">
			<span class="help" title={t('run.phases.outcomeTitle')}>{t('run.phases.onTheRepo')}</span>
		</div>
	{:else}
		<div class="foot">
			<button
				class="btn-md"
				class:btn-primary={!disabled && waits.length > 0}
				aria-busy={busy}
				{disabled}
				title={why}
				onclick={onrun}><TransportIcon kind="play" /> {t('run.phases.runNow')}</button
			>
			<span class="help">{why}</span>
		</div>
	{/if}
</article>

<style>
	.ph {
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
		padding: 10px 12px 12px;
		border-top: var(--node-accent) solid var(--tone);
	}
	.ph.live {
		border-color: var(--running-line);
		border-top-color: var(--tone);
		box-shadow: 0 0 var(--glow-md) var(--running-glow);
	}
	.head {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
		min-width: 0;
	}
	.id {
		font-weight: 600;
		font-size: var(--fs-ui);
		overflow-wrap: anywhere;
	}
	.desc {
		margin: 0;
		color: var(--text-muted);
		font-size: var(--fs-label);
		text-wrap: pretty;
	}
	.facts {
		--k: 72px;
	}
	.holds {
		display: flex;
		flex-direction: column;
		gap: 3px;
		padding: 6px 10px;
		border-radius: var(--r-row);
		background: var(--attention-bg);
		border: 1px solid var(--attention-line);
	}
	.holds ul {
		margin: 0;
		padding-left: 16px;
		font-size: var(--fs-label);
	}
	.foot {
		display: flex;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
		padding-top: 8px;
		border-top: 1px solid var(--line);
	}
	.foot .help {
		flex: 1 1 160px;
		min-width: 0;
	}
</style>
