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
		const t = setInterval(() => active && !document.hidden && load(), 30_000);
		return () => clearInterval(t);
	});
</script>

{#each mrs.filter((m) => m.state === 'open' || m.error) as mr (mr.repo + mr.branch)}
	<span class="mrs" title={mr.error ?? `${mr.branch}${mr.target ? ' → ' + mr.target : ''}`}>
		{#if mrs.length > 1}<span class="mono">{mr.repo}</span>{/if}
		{#if mr.error}
			<span class="fnt">forge unreachable</span>
		{:else if mr.mergeable === 'conflicts'}
			<span class="bad">conflicts</span>
		{:else if mr.mergeable === 'clean'}
			<span class="ok">mergeable</span>
		{:else}
			<span class="fnt">checking…</span>
		{/if}
		{#if mr.pendingThreads > 0}
			<span class="warn"
				>· {mr.pendingThreads} thread{mr.pendingThreads === 1 ? '' : 's'}{#if mr.lastCommentAt}
					· {ago(mr.lastCommentAt.toISOString())}{/if}</span
			>
		{/if}
	</span>
{/each}
{#each mrs.filter((m) => m.state === 'merged') as mr (mr.repo + mr.branch)}
	<span class="mrs" title={`${mr.branch} merged`}
		>{#if mrs.length > 1}<span class="mono">{mr.repo}</span>{/if}<span class="ok">merged</span
		></span
	>
{/each}

<style>
	.mrs {
		display: inline-flex;
		gap: 4px;
		align-items: baseline;
		font-size: 11px;
		font-weight: 500;
		white-space: nowrap;
	}
	.mono {
		font-size: 10.5px;
		color: var(--fnt);
	}
	.ok {
		color: var(--grn);
	}
	.bad {
		color: var(--ros);
	}
	.warn {
		color: var(--amb);
	}
</style>
