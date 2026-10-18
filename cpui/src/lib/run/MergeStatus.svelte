<script lang="ts">
	// What the forge says about the run's merge requests right now: does each
	// one merge cleanly, and are reviewers waiting on the factory. Polled while
	// the run is alive; an observation, not part of the run's record.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { MergeRequestLive } from '$lib/domain';
	import { ago } from '$lib/format';

	let { runId, active }: { runId: string; active: boolean } = $props();
	let mrs = $state<MergeRequestLive[]>([]);

	async function load() {
		try {
			mrs = await api.runs.mrStatus(runId);
		} catch {
			// The header must not break on a forge hiccup; the next poll retries.
		}
	}

	onMount(() => {
		load();
		const t = setInterval(() => active && load(), 30_000);
		return () => clearInterval(t);
	});
</script>

{#each mrs.filter((m) => m.state === 'open' || m.error) as mr (mr.repo + mr.branch)}
	<span class="mrs small" title={mr.error ?? `${mr.branch}${mr.target ? ' → ' + mr.target : ''}`}>
		{#if mrs.length > 1}<span class="mono">{mr.repo}</span>{/if}
		{#if mr.error}
			<span class="muted">forge unreachable</span>
		{:else if mr.mergeable === 'conflicts'}
			<span class="bad">conflicts with {mr.target ?? 'its target'}</span>
		{:else if mr.mergeable === 'clean'}
			<span class="ok">mergeable</span>
		{:else}
			<span class="muted">mergeability pending</span>
		{/if}
		{#if mr.pendingThreads > 0}
			· <span class="warn"
				>{mr.pendingThreads} unresolved thread{mr.pendingThreads === 1 ? '' : 's'}</span
			>{#if mr.lastCommentAt}<span class="muted">
					· last {ago(mr.lastCommentAt.toISOString())}</span
				>{/if}
		{/if}
	</span>
{/each}

<style>
	.mrs {
		display: inline-flex;
		gap: 4px;
		align-items: baseline;
		margin-left: 8px;
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
