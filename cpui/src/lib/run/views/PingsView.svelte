<script lang="ts">
	// Pings (DESIGN §6): every ping this run raised, newest first. A pending
	// one opens into the card that answers it; answered ones say how.
	import PingCard from '$lib/PingCard.svelte';
	import PingPill from '$lib/ui/PingPill.svelte';
	import { ago, fmtAbs, fmtClock } from '$lib/format';
	import { isAgentPing } from '$lib/ping';
	import { to } from '$lib/guild';
	import { mdToHtml } from '$lib/planreview';
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

<div class="pv">
	<div class="top">
		<span>Pings raised by this run</span>
		<span>{summary || 'none yet'}</span>
	</div>
	{#if list.length === 0}
		<div class="none">
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
				<button class="row" onclick={() => (open = open === p.id ? null : p.id)}>
					<PingPill type={p.type} />
					<span class="mid">
						<span class="q" class:struck={p.status === 'withdrawn'}>{p.question}</span>
						<span class="meta"
							>{p.stageId ? `${p.stageId} · ` : ''}<span title={fmtAbs(p.raisedAt)}
								>{fmtClock(p.raisedAt)}</span
							>
							· {isAgentPing(p.type) ? 'unwanted' : 'wanted'}</span
						>
					</span>
					<span class="st {p.status}">
						{#if p.status === 'answered'}{p.answer === 'text' ? 'answered' : p.answer}{p.answeredAt
								? ` · ${ago(p.answeredAt)}`
								: ''}
						{:else}withdrawn{/if}
					</span>
				</button>
				{#if open === p.id}
					<div class="body">
						<div class="md">{@html mdToHtml(p.question)}</div>
						{#if p.feedback}<div class="fb"><b>You:</b> {p.feedback}</div>{/if}
						<a class="ib" href={to.ping(p.id)}>Open in inbox →</a>
					</div>
				{/if}
			</div>
		{/if}
	{/each}
</div>

<style>
	.pv {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: 12px 14px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.top {
		display: flex;
		justify-content: space-between;
		font-size: 11.5px;
		color: var(--fnt);
	}
	.none {
		color: var(--mut);
		font-size: 12.5px;
		padding: 12px 0;
		max-width: 60ch;
	}
	.pr {
		border-radius: 14px;
		overflow: hidden;
	}
	.row {
		all: unset;
		box-sizing: border-box;
		cursor: pointer;
		width: 100%;
		display: grid;
		grid-template-columns: auto minmax(0, 1fr) auto;
		gap: 12px;
		align-items: center;
		padding: 10px 12px;
	}
	.row:hover {
		background: var(--p1);
	}
	.mid {
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.q {
		font-size: 13px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.q.struck {
		text-decoration: line-through;
		color: var(--fnt);
	}
	.meta {
		font-size: 11.5px;
		color: var(--mut);
	}
	.st {
		font-size: 12px;
		font-weight: 600;
		white-space: nowrap;
		color: var(--mut);
	}
	.st.withdrawn {
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
		font-size: 12.5px;
		white-space: pre-wrap;
	}
	.ib {
		display: inline-block;
		font-size: 12px;
		color: var(--brand);
		font-weight: 500;
	}
</style>
