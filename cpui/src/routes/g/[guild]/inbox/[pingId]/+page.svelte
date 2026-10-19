<!-- A ping by id (DESIGN 7.5). A pending ping with a document opens the plan
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
	import { ago, fmtAbs, fmtClock, shortId } from '$lib/format';
	import { lore, isLore } from '$lib/lore';
	import { isDocumentPing } from '$lib/ping';
	import type { Ping, PingFile } from '$lib/domain';
	import PlanReview from '$lib/PlanReview.svelte';
	import { mdToHtml, planBlocks } from '$lib/planreview';
	import Inbox from '$lib/inbox/Inbox.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import PingPill from '$lib/ui/PingPill.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';

	let ping = $state<Ping | null>(null);
	let gone = $state(false);

	let reviewed = $state<{ path: string; html: string } | null>(null);
	let answeredFiles = $state<PingFile[]>([]);

	async function loadAnswered(pid: string) {
		try {
			const { files } = await api.pings.payload(pid);
			answeredFiles = files;
			const primary = files[0];
			if (primary?.captured && primary.snapshotId) {
				const res = await fetch(api.files.snapshotUrl(primary.snapshotId));
				if (res.ok) {
					const text = await res.text();
					reviewed = {
						path: primary.path,
						html: planBlocks(text)
							.map((b) => b.html)
							.join('')
					};
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
		{ label: 'Inbox', href: to.inbox() },
		{ label: ping ? shortId(ping.id, 'p_') : '…', mono: true }
	]);
	const verdict = $derived(
		ping?.answer === 'approve'
			? { glyph: '✓', word: 'Approved', tone: 'grn' }
			: ping?.answer === 'reject'
				? { glyph: '↩', word: 'Changes requested', tone: 'amb' }
				: { glyph: '✓', word: 'Answered', tone: 'grn' }
	);
</script>

<svelte:head><title>Review · Inbox</title></svelte:head>

{#if gone}
	<div class="page">
		<EmptyState title="No such ping" text="It may have been withdrawn, or the link is wrong.">
			<a class="btn btn-primary" href={to.inbox()}>Back to the inbox</a>
		</EmptyState>
	</div>
{:else if !ping}
	<PageHeader {crumbs} title="Review" />
	<div class="page"><div class="c sk"><Skeleton lines={8} /></div></div>
{:else if ping.status === 'pending' && !isDocumentPing(ping)}
	<Inbox initial={ping.id} />
{:else}
	<PageHeader {crumbs} title="Review" accent={isLore() ? lore('plan').toLowerCase() : 'the plan'}>
		{#snippet chip()}<PingPill type={ping!.type} />{/snippet}
		{#snippet meta()}
			{#if run}<a href={to.run(run.id)}>{run.title}</a>{:else}<a href={to.run(ping!.runId)}
					>{shortId(ping!.runId)}</a
				>{/if}
			{#if ping!.stageId}<span class="mono">{ping!.stageId}</span>{/if}
			<span title={fmtAbs(ping!.raisedAt)}>raised {ago(ping!.raisedAt)}</span>
			{#if ping!.status === 'pending'}<span class="hue-amb">waiting on you</span>{/if}
		{/snippet}
		{#snippet actions()}
			<a class="btn" href={to.run(ping!.runId)}>Open run →</a>
		{/snippet}
	</PageHeader>

	{#if ping.status === 'pending'}
		<PlanReview
			{ping}
			onanswered={() => {
				void fleet.refresh();
				void goto(to.inbox());
			}}
		/>
	{:else}
		<div class="page ans">
			<section class="banner c t-{verdict.tone}">
				<div class="bh">
					{#if ping.status === 'withdrawn'}
						<span class="g fnt">–</span><span class="w fnt">Withdrawn</span>
						<span class="mut">the run moved on before anyone answered</span>
					{:else}
						<span class="g hue-{verdict.tone}">{verdict.glyph}</span>
						<span class="w">{verdict.word}</span>
						{#if ping.answer && ping.answer !== 'approve' && ping.answer !== 'reject'}
							<span class="tag">{ping.answer}</span>
						{/if}
						<span class="mut" title={ping.answeredAt ? fmtAbs(ping.answeredAt) : ''}
							>{ping.answeredAt ? ago(ping.answeredAt) : ''}</span
						>
					{/if}
				</div>
				<div class="md q">{@html mdToHtml(ping.question)}</div>
				{#if ping.feedback}
					<div class="fb">
						<div class="lbl">Feedback sent</div>
						<div class="md">{@html mdToHtml(ping.feedback)}</div>
					</div>
				{/if}
			</section>

			{#if reviewed}
				<section class="doc c">
					<div class="dh">
						<span class="mono ell">{reviewed.path}</span>
						<span class="spacer"></span>
						<span class="cap" title={fmtAbs(ping.raisedAt)}
							>as reviewed · snapshot from {fmtClock(ping.raisedAt)}</span
						>
					</div>
					<div class="paper md">{@html reviewed.html}</div>
				</section>
			{/if}
			{#if answeredFiles.length > 1}
				<div class="also">
					<span class="mut small">Also attached by the agent</span>
					{#each answeredFiles.slice(1) as f (f.path)}
						{#if f.captured && f.snapshotId}
							<a
								class="xf"
								href={api.files.snapshotUrl(f.snapshotId)}
								target="_blank"
								rel="noreferrer"><span class="mut">▤</span><span class="mono ell">{f.path}</span></a
							>
						{:else}
							<span class="xf missing" title="not captured when the ping was raised"
								><span class="mono ell">{f.path}</span><span>not captured</span></span
							>
						{/if}
					{/each}
				</div>
			{/if}
		</div>
	{/if}
{/if}

<style>
	.sk {
		border-radius: 16px;
		padding: 16px;
	}
	.ans {
		padding-top: 2px;
		align-items: stretch;
	}
	.ans > * {
		max-width: 920px;
		width: 100%;
	}
	.banner {
		border-radius: 16px;
		padding: 14px 18px 16px;
		display: flex;
		flex-direction: column;
		gap: 10px;
		flex: none;
	}
	.banner.t-grn {
		background:
			radial-gradient(ellipse 60% 140% at 0% 0%, var(--grnBg), transparent 70%), var(--cBg);
		border-color: var(--grnLn);
	}
	.banner.t-amb {
		background:
			radial-gradient(ellipse 60% 140% at 0% 0%, var(--ambBg), transparent 70%), var(--cBg);
		border-color: var(--ambLn);
	}
	.bh {
		display: flex;
		align-items: center;
		gap: 10px;
		font-size: 12px;
		flex-wrap: wrap;
	}
	.g {
		font-weight: 800;
		font-size: 15px;
	}
	.w {
		font-family: var(--serif);
		font-size: 21px;
		line-height: 1.1;
	}
	.q {
		color: var(--mut);
		font-size: 13px;
	}
	.fb {
		border-left: 3px solid var(--brandLn);
		padding: 2px 0 2px 12px;
	}
	.lbl {
		font-size: 11px;
		color: var(--mut);
		margin-bottom: 4px;
	}
	.doc {
		border-radius: 16px;
		overflow: hidden;
		flex: none;
	}
	.dh {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 10px 16px;
		border-bottom: 1px solid var(--cLn);
	}
	.dh .mono {
		color: var(--mut);
	}
	.cap {
		font-size: 11px;
		color: var(--fnt);
		white-space: nowrap;
	}
	.spacer {
		flex: 1;
	}
	.paper {
		max-width: 72ch;
		margin: 0 auto;
		padding: 18px 22px 28px;
	}
	.paper :global(h1),
	.paper :global(h2) {
		font-family: var(--serif);
		font-style: italic;
		font-weight: 400;
		font-size: 22px;
	}
	.also {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
		align-items: center;
	}
	.small {
		font-size: 12px;
	}
	.xf {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		max-width: 280px;
		padding: 3px 9px;
		border-radius: 9px;
		background: var(--p2);
		border: 1px solid var(--ln);
		font-size: 11.5px;
		color: var(--tx);
	}
	.xf.missing {
		background: transparent;
		border-style: dashed;
		border-color: var(--ln2);
		color: var(--fnt);
	}
</style>
