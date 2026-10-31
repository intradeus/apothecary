<!-- A ping by id (DESIGN 7.5). A pending ping with files opens the review
     workbench; any other pending ping opens the inbox with it selected; an
     answered one shows what was answered — and, when it showed files, the
     review as submitted over the files "as reviewed", from their raise-time
     snapshots, long after the workspace is gone (DESIGN-FILES §9). -->
<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { to } from '$lib/core/urls';
	import { api } from '$lib/api';
	import { fleet } from '$lib/app/fleet.svelte';
	import { ago, fmtAbs, shortId } from '$lib/core/format';
	import { t } from '$lib/i18n/index.svelte';
	import {
		answerMeta,
		answerSummary,
		curatable,
		isDocumentPing,
		primaryFile
	} from '$lib/core/ping';
	import type { Ping } from '$lib/domain';
	import DocReview from '$lib/review/DocReview.svelte';
	import ReviewHistory from '$lib/review/ReviewHistory.svelte';
	import CurationPage from '$lib/curation/CurationPage.svelte';
	import CurationHistory from '$lib/curation/CurationHistory.svelte';
	import Inbox from '$lib/inbox/Inbox.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import PingPill from '$lib/ui/PingPill.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/design/icons';

	let ping = $state<Ping | null>(null);
	let gone = $state(false);

	// Keyed on the id: moving between pings reuses this component.
	$effect(() => {
		const id = page.params.pingId!;
		ping = null;
		gone = false;
		api.pings
			.get(id)
			.then((p) => {
				if (page.params.pingId === id) ping = p;
			})
			.catch(() => {
				if (page.params.pingId === id) gone = true;
			});
	});

	const run = $derived(ping ? fleet.runById.get(ping.runId) : undefined);
	const crumbs = $derived([
		{ label: t('nav.desk'), href: to.desk() },
		{ label: t('page.inbox.title'), href: to.inbox() },
		{ label: ping ? shortId(ping.id, 'p_') : '…', mono: true }
	]);
	const verdict = $derived(ping ? answerMeta(ping) : answerMeta({}));
	// The title's accent names the file under review — or how many.
	const reviewing = $derived(
		!ping
			? ''
			: ping.files.length > 1
				? t('page.ping.nFiles', { count: ping.files.length })
				: (primaryFile(ping).split('/').pop() ?? '')
	);
	const showsFiles = $derived(!!ping && isDocumentPing(ping));
	// A gate over a review document is curated, not read: the page keys on
	// the shown file's port type alone.
	const curated = $derived(ping ? curatable(ping) : undefined);
</script>

<svelte:head><title>{t('page.ping.docTitle')}</title></svelte:head>

{#if gone}
	<div class="page glass">
		<EmptyState title={t('page.ping.gone')} text={t('page.ping.goneText')}>
			<a class="btn btn-primary" href={to.inbox()}>{t('page.ping.backToInbox')}</a>
		</EmptyState>
	</div>
{:else if !ping}
	<PageHeader {crumbs} title={t('page.ping.review')} />
	<div class="page glass"><Skeleton pane lines={8} /></div>
{:else if ping.status === 'pending' && !isDocumentPing(ping)}
	<Inbox initial={ping.id} />
{:else}
	<PageHeader {crumbs} title={t('page.ping.review')} accent={reviewing || undefined}>
		{#snippet chip()}<PingPill type={ping!.type} />{/snippet}
		{#snippet meta()}
			{#if run}<a href={to.run(run.id)}>{run.title}</a>{:else}<a href={to.run(ping!.runId)}
					>{shortId(ping!.runId)}</a
				>{/if}
			{#if ping!.stageId}<span class="mono">{ping!.stageId}</span>{/if}
			<span title={fmtAbs(ping!.raisedAt)}
				>{t('page.ping.raised', { ago: ago(ping!.raisedAt) })}</span
			>
			{#if ping!.status === 'pending'}<span class="hue-attention">{t('page.ping.waiting')}</span
				>{/if}
		{/snippet}
		{#snippet actions()}
			{#if ping!.link}
				<!-- A gate shows a link beside its files: the merge request, a preview. -->
				<a class="btn" href={ping!.link} target="_blank" rel="noreferrer" title={ping!.link}
					>{t('page.ping.openLink')} {I.external}</a
				>
			{/if}
			<a class="btn" href={to.run(ping!.runId)}>{t('page.ping.openRun')} {I.forward}</a>
		{/snippet}
	</PageHeader>

	{#if ping.status === 'pending' && curated}
		<CurationPage
			{ping}
			onanswered={() => {
				const runId = ping!.runId;
				toast(t('page.ping.reviewSent'), {
					action: { label: t('page.ping.openRun'), run: () => void goto(to.run(runId)) }
				});
				void fleet.refresh();
				void goto(to.inbox());
			}}
		/>
	{:else if ping.status === 'pending'}
		<DocReview
			{ping}
			onanswered={() => {
				const runId = ping!.runId;
				toast(t('page.ping.reviewSent'), {
					action: { label: t('page.ping.openRun'), run: () => void goto(to.run(runId)) }
				});
				void fleet.refresh();
				void goto(to.inbox());
			}}
		/>
	{:else}
		<div class="page glass" class:withfiles={showsFiles && !curated}>
			<section
				class="glass pane wash tone-{verdict.hue} verdict"
				style:border-color="var(--{verdict.hue}Ln, var(--glass-line))"
			>
				<div class="bh">
					<span class="g hue-{verdict.hue}">{verdict.glyph}</span>
					<span class="w" class:faint={ping.status === 'withdrawn'}>{verdict.word}</span>
					{#if ping.status === 'withdrawn'}
						<span class="muted">{t('page.ping.moved')}</span>
					{:else}
						{#if ping.answer === 'accept'}
							<span class="tag">{answerSummary(ping)}</span>
						{/if}
						<span class="muted" title={ping.answeredAt ? fmtAbs(ping.answeredAt) : ''}
							>{ping.answeredAt ? ago(ping.answeredAt) : ''}</span
						>
					{/if}
				</div>
				<Markdown src={ping.question} quiet />
				{#if ping.feedback && (!showsFiles || curated)}
					<div class="fb">
						<div class="caption">{t('page.ping.feedbackSent')}</div>
						<Markdown src={ping.feedback} />
					</div>
				{/if}
			</section>
			{#if curated}<CurationHistory {ping} embedded />{/if}
		</div>
		{#if showsFiles && !curated}
			<ReviewHistory
				pingId={ping.id}
				runId={ping.runId}
				review={ping.review}
				summary={ping.feedback}
				attachmentIds={ping.answerAttachmentIds}
				raisedAt={ping.raisedAt}
			/>
		{/if}
	{/if}
{/if}

<style>
	/* The verdict: a hue-washed pane (.wash over an inset). */
	.verdict {
		padding: 14px 18px 16px;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.bh {
		display: flex;
		align-items: center;
		gap: 10px;
		font-size: var(--fs-label);
		flex-wrap: wrap;
	}
	.g {
		font-weight: 800;
		font-size: var(--fs-lead);
	}
	.w {
		font-family: var(--font-display);
		font-size: var(--fs-mark);
		line-height: 1.1;
	}
	.fb {
		border-left: 3px solid var(--brand-line);
		padding: 2px 0 2px 12px;
	}
	.fb .caption {
		margin-bottom: 4px;
	}
	/* Above a submitted review: the verdict only, the review fills the rest. */
	.withfiles {
		flex: none;
		max-height: 38vh;
		margin-bottom: 0;
	}
</style>
