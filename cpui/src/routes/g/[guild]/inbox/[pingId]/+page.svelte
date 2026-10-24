<!-- A ping by id (DESIGN 7.5). A pending ping with a file opens the document
     review workbench; any other pending ping opens the inbox with it
     selected; an answered one shows what was answered — and the document
     "as reviewed", from its raise-time snapshot, long after the workspace is
     gone (DESIGN-FILES §9). -->
<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { currentGuild, to } from '$lib/guild';
	import { api } from '$lib/api';
	import { fleet } from '$lib/fleet.svelte';
	import { ago, fmtAbs, shortId } from '$lib/format';
	import { t } from '$lib/i18n/index.svelte';
	import { answerMeta, isDocumentPing } from '$lib/ping';
	import { asMarkdown } from '$lib/docreview';
	import type { Ping, PingFile } from '$lib/domain';
	import DocReview from '$lib/DocReview.svelte';
	import Inbox from '$lib/inbox/Inbox.svelte';
	import DocHead from '$lib/inbox/DocHead.svelte';
	import AgentFiles from '$lib/inbox/AgentFiles.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import PingPill from '$lib/ui/PingPill.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';

	let ping = $state<Ping | null>(null);
	let gone = $state(false);

	let reviewed = $state<{ path: string; text: string } | null>(null);
	let answeredFiles = $state<PingFile[]>([]);

	async function loadAnswered(pid: string) {
		try {
			const { files } = await api.pings.payload(pid);
			answeredFiles = files;
			const primary = files[0];
			if (primary?.captured && primary.snapshotId) {
				const res = await fetch(api.files.snapshotUrl(primary.snapshotId));
				if (res.ok) {
					reviewed = { path: primary.path, text: await res.text() };
				}
			}
		} catch {
			/* no files — the answer alone renders */
		}
	}

	// Keyed on the id: moving between pings reuses this component.
	$effect(() => {
		const id = page.params.pingId!;
		ping = null;
		gone = false;
		reviewed = null;
		answeredFiles = [];
		api.pings
			.get(id)
			.then(async (p) => {
				if (page.params.pingId !== id) return;
				ping = p;
				if (p.status !== 'pending') await loadAnswered(p.id);
			})
			.catch(() => {
				if (page.params.pingId === id) gone = true;
			});
	});

	const run = $derived(ping ? fleet.runById.get(ping.runId) : undefined);
	const crumbs = $derived([
		{ label: currentGuild().name, href: to.desk() },
		{ label: t('page.inbox.title'), href: to.inbox() },
		{ label: ping ? shortId(ping.id, 'p_') : '…', mono: true }
	]);
	const verdict = $derived(ping ? answerMeta(ping) : answerMeta({}));
	// The title's accent names the file under review, when there is one.
	const reviewing = $derived(
		(ping?.primaryRef ?? ping?.files[0] ?? reviewed?.path ?? '').split('/').pop() ?? ''
	);
</script>

<svelte:head><title>{t('page.ping.docTitle')}</title></svelte:head>

{#if gone}
	<div class="page">
		<EmptyState title={t('page.ping.gone')} text={t('page.ping.goneText')}>
			<a class="btn btn-primary" href={to.inbox()}>{t('page.ping.backToInbox')}</a>
		</EmptyState>
	</div>
{:else if !ping}
	<PageHeader {crumbs} title={t('page.ping.review')} />
	<div class="page"><Skeleton pane lines={8} /></div>
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
			{#if ping!.status === 'pending'}<span class="hue-amb">{t('page.ping.waiting')}</span>{/if}
		{/snippet}
		{#snippet actions()}
			<a class="btn" href={to.run(ping!.runId)}>{t('page.ping.openRun')} {I.forward}</a>
		{/snippet}
	</PageHeader>

	{#if ping.status === 'pending'}
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
		<div class="page ans">
			<section
				class="c pane wash h-{verdict.hue} verdict"
				style:border-color="var(--{verdict.hue}Ln, var(--cLn))"
			>
				<div class="bh">
					<span class="g hue-{verdict.hue}">{verdict.glyph}</span>
					<span class="w" class:fnt={ping.status === 'withdrawn'}>{verdict.word}</span>
					{#if ping.status === 'withdrawn'}
						<span class="mut">{t('page.ping.moved')}</span>
					{:else}
						{#if ping.answer && ping.answer !== 'approve' && ping.answer !== 'reject'}
							<span class="tag">{ping.answer}</span>
						{/if}
						<span class="mut" title={ping.answeredAt ? fmtAbs(ping.answeredAt) : ''}
							>{ping.answeredAt ? ago(ping.answeredAt) : ''}</span
						>
					{/if}
				</div>
				<Markdown src={ping.question} quiet />
				{#if ping.feedback}
					<div class="fb">
						<div class="lbl">{t('page.ping.feedbackSent')}</div>
						<Markdown src={ping.feedback} />
					</div>
				{/if}
			</section>

			{#if reviewed}
				<section class="c pane">
					<DocHead path={reviewed.path} raisedAt={ping.raisedAt} reviewed />
					<div class="paper doc">
						<Markdown src={asMarkdown(reviewed.text, reviewed.path)} doc />
					</div>
				</section>
			{/if}
			<AgentFiles files={answeredFiles.slice(1)} label={t('page.ping.alsoAttached')} />
		</div>
	{/if}
{/if}

<style>
	.ans {
		padding-top: 2px;
	}
	.ans > :global(*) {
		max-width: 920px;
		width: 100%;
	}
	/* The verdict: a hue-washed pane (.wash over content glass). */
	.verdict {
		--wash-base: var(--cBg);
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
		font-family: var(--serif);
		font-size: var(--fs-mark);
		line-height: 1.1;
	}
	.fb {
		border-left: 3px solid var(--brandLn);
		padding: 2px 0 2px 12px;
	}
	.fb .lbl {
		margin-bottom: 4px;
	}
	.doc {
		padding: 18px 22px 28px;
	}
</style>
