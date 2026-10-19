<script lang="ts">
	// One merge request as the forge sees it right now: does it merge
	// cleanly, is it merged, are reviewers waiting. The header shows it
	// short; the review card spells it out (`long`).
	import type { MergeRequestLive } from '$lib/domain';
	import { ago } from '$lib/format';

	let {
		mr,
		repo = false,
		long = false
	}: {
		mr: MergeRequestLive;
		/** Prefix the repo name (multi-repo runs). */
		repo?: boolean;
		long?: boolean;
	} = $props();

	const threads = $derived(
		mr.pendingThreads > 0
			? `${long ? '' : '· '}${mr.pendingThreads}${long ? ' unresolved' : ''} thread${mr.pendingThreads === 1 ? '' : 's'}${
					mr.lastCommentAt ? ` · ${long ? 'last ' : ''}${ago(mr.lastCommentAt.toISOString())}` : ''
				}`
			: ''
	);
</script>

<span
	class="mrs"
	class:long
	title={mr.error ??
		`${mr.branch}${mr.target ? ' → ' + mr.target : ''}${mr.state === 'merged' ? ' merged' : ''}`}
>
	{#if repo}<span class="mono fnt">{mr.repo}</span>{/if}
	{#if mr.error}
		<span class="fnt">forge unreachable</span>
	{:else if mr.state === 'merged'}
		<span class="hue-grn">merged</span>
	{:else if mr.mergeable === 'conflicts'}
		<span class="hue-ros">{long ? `conflicts with ${mr.target ?? 'its target'}` : 'conflicts'}</span
		>
	{:else if mr.mergeable === 'clean'}
		<span class="hue-grn">mergeable</span>
	{:else}
		<span class="fnt">{long ? 'mergeability pending' : 'checking…'}</span>
	{/if}
	{#if threads && mr.state !== 'merged'}<span class="hue-amb">{threads}</span>{/if}
</span>

<style>
	.mrs {
		display: inline-flex;
		gap: 4px 6px;
		align-items: baseline;
		font-size: var(--fs-meta);
		font-weight: 500;
		white-space: nowrap;
	}
	.long {
		gap: 4px 10px;
		flex-wrap: wrap;
		font-size: inherit;
		white-space: normal;
	}
	.mono {
		font-size: var(--fs-id);
	}
</style>
