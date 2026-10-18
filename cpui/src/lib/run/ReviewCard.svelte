<script lang="ts">
	// The run between phases (PLAN6): each merge request as the forge sees it,
	// and the reaction phases that may act on it — started automatically by
	// their triggers, or right now by hand.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { MergeRequestLive, RunPhase } from '$lib/domain';
	import { ago } from '$lib/format';

	let { runId, idle }: { runId: string; idle: boolean } = $props();
	let mrs = $state<MergeRequestLive[]>([]);
	let phases = $state<RunPhase[]>([]);
	let busy = $state('');
	let error = $state('');

	async function load() {
		try {
			[mrs, phases] = await Promise.all([api.runs.mrStatus(runId), api.runs.phases(runId)]);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function start(phase: string, repo: string) {
		busy = phase + repo;
		error = '';
		try {
			await api.runs.startPhase(runId, phase, repo);
		} catch (e) {
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
		const t = setInterval(load, 30_000);
		return () => clearInterval(t);
	});
</script>

{#if phases.length}
	<div class="card">
		<h3 style="margin-top:0">In review</h3>
		<p class="small muted" style="margin-top:0">
			The run ends when its merge requests merge (or one is closed). Until then these phases react
			to what happens on them — one at a time.
		</p>
		{#each mrs.filter((m) => m.state === 'open') as mr (mr.repo + mr.branch)}
			<div class="mr">
				<div class="row small">
					<a href={mr.url} target="_blank" rel="noreferrer" class="mono"
						>{mr.repo === 'repo' ? mr.branch : `${mr.repo}: ${mr.branch}`} ↗</a
					>
					{#if mr.error}<span class="muted">forge unreachable</span>
					{:else if mr.mergeable === 'conflicts'}<span class="bad"
							>conflicts with {mr.target ?? 'its target'}</span
						>
					{:else if mr.mergeable === 'clean'}<span class="ok">mergeable</span>
					{:else}<span class="muted">mergeability pending</span>{/if}
					{#if mr.pendingThreads > 0}<span class="warn"
							>{mr.pendingThreads} unresolved thread{mr.pendingThreads === 1
								? ''
								: 's'}{#if mr.lastCommentAt}
								· last {ago(mr.lastCommentAt.toISOString())}{/if}</span
						>{/if}
				</div>
				{#each phases as p (p.id)}
					{@const used = p.runs[mr.repo] ?? 0}
					{@const spent = p.max !== -1 && used >= p.max}
					<div class="row small phase">
						<span class="mono">{p.id}</span>
						<span class="muted"
							>{trigger(p)}{p.confirm ? ', after you confirm' : ''} · {used}/{p.max === -1
								? '∞'
								: p.max} runs</span
						>
						<button
							class="small"
							disabled={!idle || spent || busy !== ''}
							title={spent
								? 'This phase reached its max for this merge request'
								: idle
									? 'Start it now, whether or not its trigger holds'
									: 'Another phase is running'}
							onclick={() => start(p.id, mr.repo === 'repo' ? '' : mr.repo)}
							>{busy === p.id + (mr.repo === 'repo' ? '' : mr.repo)
								? 'Starting…'
								: 'Run now'}</button
						>
					</div>
				{/each}
			</div>
		{/each}
		{#if error}<div class="error small">{error}</div>{/if}
	</div>
{/if}

<style>
	.mr {
		border-top: 1px solid var(--border);
		padding: 8px 0;
	}
	.phase {
		margin-left: 12px;
		margin-top: 4px;
	}
	.ok {
		color: var(--green);
	}
	.bad {
		color: var(--red);
	}
	.warn {
		color: var(--amber);
	}
</style>
