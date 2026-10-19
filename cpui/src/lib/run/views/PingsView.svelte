<script lang="ts">
	// Pings (DESIGN §6): every ping this run raised, newest first. A pending
	// one opens into the card that answers it; answered ones say how.
	import PingCard from '$lib/PingCard.svelte';
	import PingPill from '$lib/ui/PingPill.svelte';
	import { ago, fmtAbs, fmtClock } from '$lib/format';
	import { isAgentPing } from '$lib/ping';
	import { to } from '$lib/guild';
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
			pending ? `${pending} pending` : '',
			answered ? `${answered} answered` : '',
			withdrawn ? `${withdrawn} withdrawn` : ''
		]
			.filter(Boolean)
			.join(' · ')
	);
	let open = $state<string | null>(null);
</script>

<div class="view pv">
	<div class="top">
		<span>Pings raised by this run</span>
		<span>{summary || 'none yet'}</span>
	</div>
	{#if list.length === 0}
		<div class="empty-line">
			No pings. Gates and the agent's own questions land here — and in the inbox — when the run
			needs you.
		</div>
	{/if}
	{#each list as p (p.id)}
		{#if p.status === 'pending'}
			{#key p.id}
				<PingCard
					ping={p}
					busy={rs.answering}
					onanswer={(a, note, files) => rs.answer(p.id, a, note, files)}
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
							· {isAgentPing(p.type) ? 'unwanted' : 'wanted'}</span
						>
					</span>
					<span class="st" class:fnt={p.status === 'withdrawn'}>
						{#if p.status === 'answered'}{p.answer === 'text' ? 'answered' : p.answer}{p.answeredAt
								? ` · ${ago(p.answeredAt)}`
								: ''}
						{:else}withdrawn{/if}
					</span>
				</button>
				{#if open === p.id}
					<div class="body">
						<Markdown src={p.question} />
						{#if p.feedback}<div class="fb"><b>You:</b> {p.feedback}</div>{/if}
						<a class="brand ib" href={to.ping(p.id)}>Open in inbox {I.forward}</a>
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
