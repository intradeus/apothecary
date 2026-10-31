<script lang="ts">
	// The flow's chains (FLOWLANG §1): the main chain the run started with,
	// the watchers — what opens each one, a signal under its start policy or
	// you, how often it ran, whether it is live or parked, which signals hold
	// right now — and the outcomes that end the run. Any watcher can be
	// started by hand while no chain holds the run's one cursor.
	import { onMount } from 'svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import type { MergeRequestLive, RunPhase } from '$lib/domain';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import MrStatus from './MrStatus.svelte';

	let {
		runId,
		idle,
		ended = false
	}: {
		runId: string;
		idle: boolean;
		/** The run is over: what ran, without anything to start. */
		ended?: boolean;
	} = $props();
	let mrs = $state<MergeRequestLive[]>([]);
	let phases = $state<RunPhase[]>([]);
	let titles = $state<Record<string, string>>({});
	let busy = $state('');
	let error = $state('');
	let cause = $state('');
	// The subject picker: which phase's Run button opened it, anchored there.
	let pick = $state('');
	let pickOpen = $state(false);
	let anchor = $state<HTMLElement | null>(null);

	const open = $derived(mrs.filter((m) => m.state === 'open'));

	async function load() {
		try {
			[mrs, phases] = await Promise.all([api.runs.mrStatus(runId), api.runs.phases(runId)]);
		} catch (e) {
			cause = t('run.phases.repoError');
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function start(phase: string, subject = '') {
		pickOpen = false;
		busy = phase;
		error = '';
		try {
			await api.runs.startPhase(runId, phase, subject);
			toast(t('run.phases.phaseStarted', { phase }));
		} catch (e) {
			cause = t('run.phases.phaseFailed', { phase });
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = '';
			load();
		}
	}

	function run(p: RunPhase, ev: MouseEvent) {
		if (open.length > 1) {
			pick = p.id;
			anchor = ev.currentTarget as HTMLElement;
			pickOpen = true;
			return;
		}
		start(p.id);
	}

	/** "on conflicts with its target · asks you first" — the wait point in a few words. */
	const opens = (p: RunPhase) => {
		if (p.kind === 'main') return t('run.phases.main');
		if (p.signal === 'manual') return t('run.phases.byHand');
		const signal = titles[p.signal] ?? p.signal;
		if (p.kind === 'outcome') {
			// An outcome opens once and ends the run: done, or abandoned.
			const ends =
				p.then === 'abort'
					? t('run.phases.abandonsOn', { signal })
					: t('run.phases.endsOn', { signal });
			return `${ends} · ${t(`run.phases.start.${p.start}`)}`;
		}
		const quiet = p.quietForS
			? t('run.phases.quiet', { minutes: Math.round(p.quietForS / 60) })
			: '';
		return `${t('run.phases.on', { signal })}${quiet} · ${t(`run.phases.start.${p.start}`)}`;
	};
	/** "2/∞ runs" — or, for the main chain and an outcome, that it happens once. */
	const usage = (p: RunPhase) =>
		p.kind !== 'watcher'
			? t('run.phases.once')
			: t('run.phases.usage', { used: p.total, max: p.max === -1 ? '∞' : p.max });
	const pending = (p: RunPhase) => p.holds.filter((h) => !h.seen);
	const spent = (p: RunPhase) =>
		p.max !== -1 &&
		(open.length ? open.every((m) => (p.runs[m.repo] ?? 0) >= p.max) : (p.runs[''] ?? 0) >= p.max);
	// One cursor: a chain parked at a wait point still holds it.
	const free = $derived(idle && !phases.some((p) => p.live));
	const disabledWhy = (p: RunPhase) =>
		p.live
			? t('run.phases.live')
			: spent(p)
				? t('run.phases.spent')
				: free
					? t('run.phases.startNow')
					: t('run.phases.otherRunning');

	onMount(() => {
		load();
		api.system
			.signals()
			.then((list) => (titles = Object.fromEntries(list.map((s) => [s.slug, s.title]))))
			.catch(() => {});
		const timer = setInterval(() => !document.hidden && load(), 30_000);
		return () => clearInterval(timer);
	});
</script>

{#if phases.length}
	<div class="rc card-in">
		<div class="hd">
			<span class="t display">{t('run.phases.title')}</span>
			<span class="sub">{t('run.phases.sub')}</span>
		</div>
		{#each open as mr (mr.repo + mr.branch)}
			<div class="mrh">
				<a href={mr.url} target="_blank" rel="noreferrer" class="mono linklike brand"
					>{mr.repo === 'repo' ? mr.branch : `${mr.repo}: ${mr.branch}`} {I.external}</a
				>
				<MrStatus {mr} long />
			</div>
		{/each}
		<div class="list">
			{#each phases as p (p.id)}
				{@const waits = pending(p)}
				<div class="phase" class:islive={!!p.live}>
					<div class="who">
						<span class="mono pid" title={p.kind === 'main' ? t('run.phases.mainTitle') : undefined}
							>{p.id}</span
						>
						{#if p.kind === 'outcome'}<span
								class="pill sm t-mut"
								title={t('run.phases.outcomeTitle')}>{t('run.phases.outcomePill')}</span
							>{/if}
						{#if waits.length}<span class="pill sm t-hot" title={waits.map((h) => h.why).join('\n')}
								>{t('run.phases.pending')}</span
							>{/if}
					</div>
					<span class="mut ell"
						>{opens(p)} · {usage(p)}{#if p.live}
							· <span class="tx"
								>{p.live.parkedAt
									? t('run.phases.parkedAt', { stage: p.live.parkedAt })
									: t('run.phases.running')}</span
							>{/if}</span
					>
					{#if ended || p.kind === 'main'}
						<!-- the run is over, or the main chain: it runs once, when the run starts -->
						<span></span>
					{:else if p.kind === 'outcome'}
						<!-- An outcome happens on the repository; starting one by hand would
						     end the run from a button beside the ones that do not. -->
						<span class="fnt small" title={t('run.phases.outcomeTitle')}
							>{t('run.phases.onTheRepo')}</span
						>
					{:else}
						<button
							class="btn-sm"
							aria-busy={busy === p.id}
							disabled={!free || spent(p) || busy !== ''}
							title={disabledWhy(p)}
							onclick={(ev) => run(p, ev)}>{t('run.phases.runNow')}</button
						>
					{/if}
				</div>
			{/each}
		</div>
		{#if error}<div class="ec">
				<ErrorCard {cause} detail={error} onretry={load} ondismiss={() => (error = '')} />
			</div>{/if}
	</div>
	<Popover {anchor} bind:open={pickOpen} placement="below-end">
		<div class="pickbox">
			<span class="mut fnt">{t('run.phases.runOn')}</span>
			{#each open as mr (mr.repo + mr.branch)}
				<button class="btn-sm" onclick={() => start(pick, mr.repo)}
					>{mr.repo === 'repo' ? mr.branch : `${mr.repo}: ${mr.branch}`}</button
				>
			{/each}
		</div>
	</Popover>
{/if}

<style>
	.rc {
		display: flex;
		flex-direction: column;
	}
	.hd {
		padding: 10px 14px 8px;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.t {
		font-size: var(--fs-section);
	}
	.sub {
		font-size: var(--fs-label);
		color: var(--mut);
		text-wrap: pretty;
	}
	.mrh {
		border-top: 1px solid var(--ln);
		padding: 8px 14px;
		display: flex;
		gap: 10px;
		align-items: baseline;
		flex-wrap: wrap;
		font-size: var(--fs-label);
	}
	.list {
		border-top: 1px solid var(--ln);
		padding: 6px 14px 8px;
		display: flex;
		flex-direction: column;
		gap: 5px;
	}
	.phase {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr) auto;
		gap: 10px;
		align-items: center;
		font-size: var(--fs-label);
	}
	.phase.islive .pid {
		color: var(--link);
	}
	.who {
		display: flex;
		gap: 6px;
		align-items: center;
	}
	.pid {
		font-size: var(--fs-help);
	}
	.pickbox {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 8px;
		min-width: 200px;
	}
	.ec {
		padding: 0 14px 10px;
	}
</style>
