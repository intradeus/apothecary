<script lang="ts">
	// A curation as it was answered, read-only: the document the gate showed
	// (its raise-time snapshot, decoded by the engine) with the person's
	// decisions over it — what was posted, what was dropped, what was
	// reworded (and how it was written) — and the tally. Shown on the ping's
	// page under its verdict, and inside a run's pings view.
	import { onMount, untrack } from 'svelte';
	import { api } from '$lib/api';
	import type { CurationView } from '$lib/domain';
	import { curatable, type PingLike } from '$lib/ping';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import StatusMark from '$lib/ui/StatusMark.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import ThreadCard from './ThreadCard.svelte';
	import {
		appliedTally,
		decisionOf,
		itemsOf,
		kindHue,
		kindLabel,
		shortThread,
		threadOf,
		whereOf
	} from './curation';

	let { ping, embedded = false }: { ping: PingLike; embedded?: boolean } = $props();

	const target = untrack(() => curatable(ping)) ?? { path: '', type: '' };
	const isReply = target.type === 'review_replies';
	let view = $state<CurationView | null>(null);
	let error = $state('');
	let loading = $state(true);

	onMount(async () => {
		try {
			view = await api.pings.curation(ping.id);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	});

	const doc = $derived(view?.documents.find((d) => d.path === target.path));
	const items = $derived(doc ? itemsOf(doc) : []);
	const threads = $derived(view?.threads?.threads ?? []);
	const c = $derived(ping.curated?.[target.path]);
	const tl = $derived(appliedTally(items, c));
	const rejected = $derived(ping.answer === 'reject');
	const summaryPosted = $derived(c?.summary ?? doc?.review?.summary ?? '');
</script>

<div class="ch" class:embedded>
	{#if loading}
		<Skeleton lines={4} />
	{:else if error || !doc}
		<ErrorCard cause={t('cur.loadFailed')} detail={error || doc?.error} />
	{:else}
		<div class="tl">
			<b
				>{isReply
					? t('cur.hist.tallyReplies', { kept: tl.kept, total: tl.total })
					: t('cur.hist.tallyNotes', { kept: tl.kept, total: tl.total })}</b
			>
			{#if tl.dropped}<span>· {t('cur.hist.dropped', { count: tl.dropped })}</span>{/if}
			{#if tl.reworded}<span>· {t('cur.hist.reworded', { count: tl.reworded })}</span>{/if}
			{#if tl.summaryReworded}<span>· {t('cur.hist.summaryReworded')}</span>{/if}
			{#if !c}<span class="fnt">· {t('cur.hist.asIs')}</span>{/if}
			{#if rejected}<span class="hue-amb">· {t('cur.hist.rejected')}</span>{/if}
		</div>
		{#if doc.review && summaryPosted}
			<div class="sum">
				<div class="lbl">
					{t('cur.summary')}{#if c?.summary !== undefined}
						<span class="pill sm t-amb">{t('cur.reworded')}</span>{/if}
				</div>
				<Markdown src={summaryPosted} />
				{#if c?.summary !== undefined && doc.review.summary}
					<details>
						<summary class="disclosure">{t('cur.hist.asWritten')}</summary>
						<Markdown src={doc.review.summary} quiet />
					</details>
				{/if}
			</div>
		{/if}
		{#each items as it (it.key)}
			{@const d = decisionOf(c, it.key)}
			{@const kept = !d || d.keep}
			{@const reworded = !!d && d.keep && (!!d.body || d.resolve !== undefined)}
			{@const where = whereOf(it)}
			{@const thread = threadOf(threads, it.thread)}
			<div class="hi card-in" class:off={!kept}>
				<div class="ih">
					<StatusMark
						tone={kept ? 'ok' : 'mut'}
						size="sm"
						title={kept ? t('cur.hist.kept') : t('cur.hist.notKept')}
					/>
					{#if it.kind}<span class="pill sm t-{kindHue(it.kind)}">{kindLabel(it.kind)}</span>{/if}
					{#if where}<span class="mono where ell" title={where}>{where}</span>{/if}
					{#if it.thread}<span class="pill sm t-brand" title={it.thread}
							>{isReply ? t('cur.answers') : t('cur.continues')} {shortThread(it.thread)}</span
						>{/if}
					{#if reworded}<span class="pill sm t-amb">{t('cur.reworded')}</span>{/if}
					<span class="spacer"></span>
					<span class="fnt st"
						>{kept
							? isReply
								? t('cur.hist.sent')
								: t('cur.hist.kept')
							: t('cur.hist.notKept')}</span
					>
				</div>
				{#if thread && isReply}<ThreadCard {thread} compact />{/if}
				<div class="txt" class:struck={!kept}>{d?.body ?? it.text}</div>
				{#if d?.body}
					<details>
						<summary class="disclosure">{t('cur.hist.asWritten')}</summary>
						<div class="txt fnt">{it.text}</div>
					</details>
				{/if}
				{#if it.canResolve && kept && (d?.resolve ?? it.resolve)}
					<span class="help">{t('cur.resolve')}</span>
				{/if}
			</div>
		{/each}
	{/if}
</div>

<style>
	.ch {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 0 22px 24px;
		max-width: 920px;
		width: 100%;
		align-self: center;
	}
	.ch.embedded {
		padding: 0;
	}
	.tl {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		font-size: var(--fs-label);
		color: var(--mut);
	}
	.tl b {
		color: var(--tx);
	}
	.sum {
		border-left: 3px solid var(--brandLn);
		padding: 2px 0 2px 12px;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.sum .lbl {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.hi {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 7px 10px 9px;
	}
	.hi.off {
		opacity: 0.6;
	}
	.ih {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
		flex-wrap: wrap;
		row-gap: 4px;
		font-size: var(--fs-label);
	}
	.where {
		font-size: var(--fs-id);
		color: var(--mut);
	}
	.st {
		font-size: var(--fs-meta);
	}
	.txt {
		white-space: pre-wrap;
		font-size: var(--fs-ui);
	}
	.txt.struck {
		text-decoration: line-through;
		color: var(--mut);
	}
	details {
		font-size: var(--fs-label);
	}
	details summary {
		color: var(--mut);
	}
	details .txt {
		margin-top: 4px;
	}
	.help {
		margin: 0;
	}
</style>
