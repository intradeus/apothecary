<script lang="ts">
	// The run between phases (PLAN6): each merge request as the forge sees it,
	// and the reaction phases that may act on it — started automatically by
	// their triggers, or right now by hand.
	import { onMount } from 'svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import type { MergeRequestLive, RunPhase } from '$lib/domain';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import MrStatus from './MrStatus.svelte';

	let { runId, idle }: { runId: string; idle: boolean } = $props();
	let mrs = $state<MergeRequestLive[]>([]);
	let phases = $state<RunPhase[]>([]);
	let busy = $state('');
	let error = $state('');
	let cause = $state('');

	async function load() {
		try {
			[mrs, phases] = await Promise.all([api.runs.mrStatus(runId), api.runs.phases(runId)]);
		} catch (e) {
			cause = t('run.review.forgeError');
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function start(phase: string, repo: string) {
		busy = phase + repo;
		error = '';
		try {
			await api.runs.startPhase(runId, phase, repo);
			toast(t('run.review.phaseStarted', { phase }));
		} catch (e) {
			cause = t('run.review.phaseFailed', { phase });
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = '';
			load();
		}
	}

	const trigger = (p: RunPhase) =>
		p.signal === 'mr_conflicts'
			? t('run.review.trigger.conflicts')
			: p.signal === 'mr_review_settled'
				? t('run.review.trigger.quiet', { minutes: Math.round((p.quietForS ?? 0) / 60) })
				: p.signal === 'manual'
					? t('run.review.trigger.manual')
					: t('run.review.trigger.on', { signal: p.signal });

	onMount(() => {
		load();
		const timer = setInterval(() => !document.hidden && load(), 30_000);
		return () => clearInterval(timer);
	});
</script>

{#if phases.length}
	<div class="rc card-in">
		<div class="hd">
			<span class="t serif">{t('run.review.title')}</span>
			<span class="sub">{t('run.review.sub')}</span>
		</div>
		{#each mrs.filter((m) => m.state === 'open') as mr (mr.repo + mr.branch)}
			<div class="mr">
				<div class="mrh">
					<a href={mr.url} target="_blank" rel="noreferrer" class="mono linklike brand"
						>{mr.repo === 'repo' ? mr.branch : `${mr.repo}: ${mr.branch}`} {I.external}</a
					>
					<MrStatus {mr} long />
				</div>
				{#each phases as p (p.id)}
					{@const used = p.runs[mr.repo] ?? 0}
					{@const spent = p.max !== -1 && used >= p.max}
					<div class="phase">
						<span class="mono pid">{p.id}</span>
						<span class="mut ell"
							>{trigger(p)}{p.confirm ? t('run.review.afterConfirm') : ''} · {t(
								'run.review.usage',
								{
									used,
									max: p.max === -1 ? '∞' : p.max
								}
							)}</span
						>
						<button
							class="btn-sm"
							aria-busy={busy === p.id + (mr.repo === 'repo' ? '' : mr.repo)}
							disabled={!idle || spent || busy !== ''}
							title={spent
								? t('run.review.spent')
								: idle
									? t('run.review.startNow')
									: t('run.review.otherRunning')}
							onclick={() => start(p.id, mr.repo === 'repo' ? '' : mr.repo)}
							>{t('run.review.runNow')}</button
						>
					</div>
				{/each}
			</div>
		{/each}
		{#if error}<div class="ec">
				<ErrorCard {cause} detail={error} onretry={load} ondismiss={() => (error = '')} />
			</div>{/if}
	</div>
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
	.mr {
		border-top: 1px solid var(--ln);
		padding: 8px 14px;
		display: flex;
		flex-direction: column;
		gap: 5px;
	}
	.mrh {
		display: flex;
		gap: 10px;
		align-items: baseline;
		flex-wrap: wrap;
		font-size: var(--fs-label);
	}
	.phase {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr) auto;
		gap: 10px;
		align-items: center;
		font-size: var(--fs-label);
		padding-left: 10px;
	}
	.pid {
		font-size: var(--fs-help);
	}
	.ec {
		padding: 0 14px 10px;
	}
</style>
