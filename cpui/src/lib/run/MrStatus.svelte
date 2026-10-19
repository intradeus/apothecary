<script lang="ts">
	// One merge request as the forge sees it right now: does it merge
	// cleanly, is it merged, are reviewers waiting. The header shows it
	// short; the review card spells it out (`long`).
	import type { MergeRequestLive } from '$lib/domain';
	import { ago } from '$lib/format';
	import { t } from '$lib/i18n/index.svelte';

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
			? `${long ? '' : '· '}${t(long ? 'run.mrStatus.unresolved' : 'run.mrStatus.threads', { count: mr.pendingThreads })}${
					mr.lastCommentAt
						? ` · ${t(long ? 'run.mrStatus.lastLong' : 'run.mrStatus.last', { ago: ago(mr.lastCommentAt.toISOString()) })}`
						: ''
				}`
			: ''
	);
</script>

<span
	class="mrs"
	class:long
	title={mr.error ??
		`${mr.branch}${mr.target ? ' → ' + mr.target : ''}${mr.state === 'merged' ? ` ${t('run.mrStatus.mergedSuffix')}` : ''}`}
>
	{#if repo}<span class="mono fnt">{mr.repo}</span>{/if}
	{#if mr.error}
		<span class="fnt">{t('run.mrStatus.unreachable')}</span>
	{:else if mr.state === 'merged'}
		<span class="hue-grn">{t('run.mrStatus.merged')}</span>
	{:else if mr.mergeable === 'conflicts'}
		<span class="hue-ros"
			>{long
				? t('run.mrStatus.conflictsWith', { target: mr.target ?? t('run.mrStatus.itsTarget') })
				: t('run.mrStatus.conflicts')}</span
		>
	{:else if mr.mergeable === 'clean'}
		<span class="hue-grn">{t('run.mrStatus.mergeable')}</span>
	{:else}
		<span class="fnt">{long ? t('run.mrStatus.pendingLong') : t('run.mrStatus.checking')}</span>
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
