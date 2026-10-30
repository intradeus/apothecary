<script lang="ts">
	// Pings (DESIGN §6): every ping this run raised, newest first. A pending
	// one opens into the card that answers it; answered ones say how — one
	// that showed files, with the review as it was submitted.
	import { t } from '$lib/i18n/index.svelte';
	import PingCard from '$lib/PingCard.svelte';
	import PingPill from '$lib/ui/PingPill.svelte';
	import { ago, fmtAbs, fmtClock } from '$lib/format';
	import { answerMeta, curatable, isAgentPing, isDocumentPing } from '$lib/ping';
	import ReviewHistory from '$lib/review/ReviewHistory.svelte';
	import CurationHistory from '$lib/curation/CurationHistory.svelte';
	import { to } from '$lib/urls';
	import Markdown from '$lib/ui/Markdown.svelte';
	import { I } from '$lib/ui/icons';
	import type { RunStore } from '../store.svelte';

	let { rs }: { rs: RunStore } = $props();

	const list = $derived([...rs.pings].reverse());
	const pending = $derived(rs.pings.filter((p) => p.status === 'pending').length);
	const answered = $derived(rs.pings.filter((p) => p.status === 'answered').length);
	const withdrawn = $derived(rs.pings.filter((p) => p.status === 'withdrawn').length);
	const summary = $derived(
		[
			pending ? t('runview.pings.pending', { count: pending }) : '',
			answered ? t('runview.pings.answered', { count: answered }) : '',
			withdrawn ? t('runview.pings.withdrawn', { count: withdrawn }) : ''
		]
			.filter(Boolean)
			.join(' · ')
	);
	let open = $state<string | null>(null);
</script>

<div class="view pv">
	<div class="top">
		<span>{t('runview.pings.raised')}</span>
		<span>{summary || t('runview.pings.none_yet')}</span>
	</div>
	{#if list.length === 0}
		<div class="empty-line">
			{t('runview.pings.empty')}
		</div>
	{/if}
	{#each list as p (p.id)}
		{#if p.status === 'pending'}
			{#key p.id}
				<PingCard
					ping={p}
					busy={rs.answering}
					onanswer={(a, note, files, content) => rs.answer(p.id, a, note, files, content)}
				/>
			{/key}
		{:else}
			<div class="pr card-in">
				<button
					class="rowbtn row"
					aria-expanded={open === p.id}
					onclick={() => (open = open === p.id ? null : p.id)}
				>
					<PingPill type={p.type} />
					<span class="mid">
						<span class="q ell" class:struck={p.status === 'withdrawn'}>{p.question}</span>
						<span class="meta"
							>{p.stageId ? `${p.stageId} · ` : ''}<span title={fmtAbs(p.raisedAt)}
								>{fmtClock(p.raisedAt)}</span
							>
							· {isAgentPing(p.type)
								? t('runview.pings.unwanted')
								: t('runview.pings.wanted')}</span
						>
					</span>
					<span class="st" class:fnt={p.status === 'withdrawn'}>
						{#if p.status === 'answered'}{p.answerKind === 'form'
								? answerMeta(p).word
								: p.answer}{p.answeredAt ? ` · ${ago(p.answeredAt)}` : ''}
						{:else}{t('runview.pings.st_withdrawn')}{/if}
					</span>
				</button>
				{#if open === p.id}
					<div class="body">
						<Markdown src={p.question} />
						{#if p.status === 'answered' && curatable(p)}
							{#if p.feedback}<div class="fb">
									<b>{t('runview.pings.you')}</b>
									{p.feedback}
								</div>{/if}
							<CurationHistory ping={p} embedded />
						{:else if p.status === 'answered' && isDocumentPing(p)}
							<ReviewHistory
								embedded
								keys={false}
								pingId={p.id}
								runId={rs.runId}
								review={p.review}
								summary={p.feedback}
								attachmentIds={p.attachmentIds}
								attachments={rs.attachments}
								raisedAt={p.raisedAt}
							/>
						{:else if p.feedback}<div class="fb">
								<b>{t('runview.pings.you')}</b>
								{p.feedback}
							</div>{/if}
						<a class="brand ib" href={to.ping(p.id)}>{t('runview.pings.open_inbox')} {I.forward}</a>
					</div>
				{/if}
			</div>
		{/if}
	{/each}
</div>

<style>
	.pv {
		padding: 12px 14px;
		gap: 8px;
	}
	.pv > :global(*) {
		flex: none;
	}
	.top {
		display: flex;
		justify-content: space-between;
		font-size: var(--fs-help);
		color: var(--fnt);
	}
	.pv .empty-line {
		padding-left: 0;
	}
	.pr {
		overflow: hidden;
	}
	.row {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr) auto;
		gap: 12px;
		padding: 10px 12px;
		border-radius: 0;
	}
	.mid {
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.q {
		font-size: var(--fs-body);
	}
	.q.struck {
		text-decoration: line-through;
		color: var(--fnt);
	}
	.meta {
		font-size: var(--fs-help);
		color: var(--mut);
	}
	.st {
		font-size: var(--fs-label);
		font-weight: 600;
		white-space: nowrap;
		color: var(--mut);
	}
	.st.fnt {
		color: var(--fnt);
		font-weight: 500;
	}
	.body {
		padding: 10px 14px 12px;
		border-top: 1px solid var(--ln);
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.fb {
		font-size: var(--fs-ui);
		white-space: pre-wrap;
	}
	.ib {
		font-size: var(--fs-label);
		font-weight: 500;
	}
</style>
