<script lang="ts">
	// One merge request as its repository host sees it right now: does it merge
	// cleanly — or would the host merge it now — is it merged, what its CI
	// says, is it approved, are reviewers waiting. The header shows it short;
	// the phases card spells it out (`long`).
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
	const ci = $derived(mr.state === 'open' ? mr.checks : undefined);
	const failed = $derived(ci?.items.filter((c) => c.state === 'failure') ?? []);
	const ciLink = $derived(failed[0]?.url ?? ci?.items.find((c) => c.url)?.url);
	const ciText = $derived.by(() => {
		if (!ci || !ci.state || ci.state === 'neutral') return '';
		if (ci.state === 'failure')
			return long
				? t('run.mrStatus.ciFailedLong', { checks: failed.map((c) => c.name).join(', ') })
				: t('run.mrStatus.ciFailed');
		return t(ci.state === 'success' ? 'run.mrStatus.ciPassed' : 'run.mrStatus.ciRunning');
	});
	const ciHue = $derived(
		ci?.state === 'failure' ? 'hue-ros' : ci?.state === 'success' ? 'hue-grn' : 'fnt'
	);
	const approved = $derived(mr.state === 'open' && mr.approval?.approved ? mr.approval : undefined);
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
	{:else if mr.mergeableNow}
		<span class="hue-grn">{t('run.mrStatus.readyToMerge')}</span>
	{:else if mr.mergeable === 'clean'}
		<span class="hue-grn">{t('run.mrStatus.mergeable')}</span>
	{:else}
		<span class="fnt">{long ? t('run.mrStatus.pendingLong') : t('run.mrStatus.checking')}</span>
	{/if}
	{#if mr.draft && mr.state === 'open'}<span class="fnt">{t('run.mrStatus.draft')}</span>{/if}
	{#if ciText}
		{#if long && ciLink}<a
				class="{ciHue} linklike"
				href={ciLink}
				target="_blank"
				rel="noreferrer"
				title={ci?.items.map((c) => `${c.name}: ${c.state}`).join('\n')}>{ciText}</a
			>{:else}<span class={ciHue} title={ci?.items.map((c) => `${c.name}: ${c.state}`).join('\n')}
				>{ciText}</span
			>{/if}
	{/if}
	{#if approved}<span class="hue-grn" title={approved.by.join(', ')}
			>{long && approved.by.length
				? t('run.mrStatus.approvedBy', { who: approved.by.join(', ') })
				: t('run.mrStatus.approved')}</span
		>{/if}
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
