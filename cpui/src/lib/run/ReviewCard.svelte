<script lang="ts">
	// The run between phases (PLAN6): each merge request as the forge sees it,
	// and the reaction phases that may act on it — started automatically by
	// their triggers, or right now by hand.
	import { onMount } from 'svelte';
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
			cause = 'The forge could not be read.';
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function start(phase: string, repo: string) {
		busy = phase + repo;
		error = '';
		try {
			await api.runs.startPhase(runId, phase, repo);
			toast(`Phase ${phase} started`);
		} catch (e) {
			cause = `Phase ${phase} could not start.`;
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = '';
			load();
		}
	}

	const trigger = (p: RunPhase) =>
		p.signal === 'mr_conflicts'
			? 'when it conflicts with its target'
			: p.signal === 'mr_review_settled'
				? `when review comments go quiet for ${Math.round((p.quietForS ?? 0) / 60)}m`
				: `on ${p.signal}`;

	onMount(() => {
		load();
		const t = setInterval(() => !document.hidden && load(), 30_000);
		return () => clearInterval(t);
	});
</script>

{#if phases.length}
	<div class="rc card-in">
		<div class="hd">
			<span class="t serif">In review</span>
			<span class="sub"
				>The run ends when its merge requests merge (or one is closed). Until then these phases
				react to what happens on them — one at a time.</span
			>
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
							>{trigger(p)}{p.confirm ? ', after you confirm' : ''} · {used}/{p.max === -1
								? '∞'
								: p.max} runs</span
						>
						<button
							class="btn-sm"
							aria-busy={busy === p.id + (mr.repo === 'repo' ? '' : mr.repo)}
							disabled={!idle || spent || busy !== ''}
							title={spent
								? 'This phase reached its max for this merge request'
								: idle
									? 'Start it now, whether or not its trigger holds'
									: 'Another phase is running'}
							onclick={() => start(p.id, mr.repo === 'repo' ? '' : mr.repo)}>Run now</button
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
