<script lang="ts" module>
	/** A message you sent that the run has not echoed back yet. */
	export interface Sending {
		text: string;
		interrupt: boolean;
	}
</script>

<script lang="ts">
	// The run's conversation (DESIGN 4.10): stage dividers, status lines, the
	// prompt, agent messages (streaming with a caret), thinking, tool groups,
	// the plan, your messages and the agent's questions.
	//
	// New items arrive with a small fade and rise (transform and opacity
	// only, so nothing below them jumps); the history already there when the
	// view opens, and the SSE replay that follows, land without it.
	import { promptLabel, type ChatItem } from '$lib/transcript';
	import { fmtClock, fmtTime } from '$lib/format';
	import { TONE_HUE } from '$lib/ui/hues';
	import { I } from '$lib/ui/icons';
	import { ms } from '$lib/ui/motion';
	import { token } from '$lib/ui/tokens';
	import PingPill from '$lib/ui/PingPill.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import StatusMark, { type MarkTone } from '$lib/ui/StatusMark.svelte';
	import ToolCalls from './ToolCalls.svelte';

	let {
		items,
		sending = [],
		over = false,
		onlog,
		onanswer
	}: {
		items: ChatItem[];
		/** Messages you sent that the run has not echoed back yet. */
		sending?: Sending[];
		/** The run is finished: an unanswered question was withdrawn with it. */
		over?: boolean;
		/** Open a stage's log. */
		onlog: (stage: string) => void;
		/** Jump to where a pending question is answered. */
		onanswer: () => void;
	} = $props();

	const planDone = (es: { status: string }[]) => es.filter((e) => e.status === 'completed').length;
	const planTone = (s: string): MarkTone =>
		s === 'completed' ? 'ok' : s === 'in_progress' ? 'live' : 'pending';

	const userTexts = $derived(new Set(items.flatMap((it) => (it.kind === 'user' ? [it.text] : []))));
	const unechoed = $derived(sending.filter((s) => !userTexts.has(s.text)));

	// Arm the arrival motion once the list has been quiet for a moment: the
	// initial history (and the replay burst behind it) settles first.
	let armed = false;
	$effect(() => {
		void items.length;
		if (armed) return;
		const t = setTimeout(() => (armed = true), 700);
		return () => clearTimeout(t);
	});

	/** Does this item get the arrival motion? Not when it replaces something
	 *  already on screen: a persisted message or tool call superseding its
	 *  live twin, a plan moving to its latest spot, your own message echoing
	 *  the bubble you sent. */
	const arrives = (it: ChatItem) =>
		!(
			it.kind === 'plan' ||
			((it.kind === 'message' || it.kind === 'tools') && !it.live) ||
			(it.kind === 'user' && sending.some((s) => s.text === it.text))
		);

	function arrive(node: HTMLElement, on: boolean) {
		if (!armed || !on) return;
		node.animate([{ opacity: 0 }, { opacity: 1 }], {
			duration: ms('--dur-fade', 160),
			easing: 'ease-out'
		});
		node.animate([{ transform: 'translateY(8px)' }, { transform: 'none' }], {
			duration: ms('--dur-spring', 450),
			easing: token('--spring') || 'ease-out'
		});
	}
</script>

{#each items as it (it.key)}
	{@const a = arrives(it)}
	{#if it.kind === 'stage'}
		<div class="divider" use:arrive={a}>
			<span class="hl l"></span>
			<span class="sname serif">{it.stageId}</span>
			<span class="smeta ell"
				>attempt {it.iteration}{#if it.agent}&nbsp;· {it.agent}{/if}{#if it.model}&nbsp;· {it.model}{/if}{#if !it.agentic}&nbsp;·
					stage{/if} · <span class="mono">{fmtTime(it.ts)}</span></span
			>
			<span class="hl r"></span>
		</div>
	{:else if it.kind === 'status'}
		<div class="status hue-{TONE_HUE[it.tone]}" use:arrive={a}>
			<span>{it.glyph}</span>
			<span
				>{it.text}{#if it.meta}<span class="mut">{' · '}{it.meta}</span>{/if}</span
			>
		</div>
	{:else if it.kind === 'failure'}
		<div use:arrive={a}>
			<ErrorCard
				cause={it.title}
				detail={it.error}
				action="View log"
				onretry={() => onlog(it.stageId)}
			/>
		</div>
	{:else if it.kind === 'prompt'}
		<details class="card-in prompt" use:arrive={a}>
			<summary class="disclosure">
				<span class="b">{promptLabel(it.promptKind)}</span>
				<span class="pv mut ell">{it.text.split('\n').find((l) => l.trim()) ?? ''}</span>
			</summary>
			<pre>{it.text}{it.truncated ? '\n… [truncated]' : ''}</pre>
		</details>
	{:else if it.kind === 'message'}
		{#if it.thought}
			<details class="thought" open={it.live} use:arrive={a}>
				<summary
					>{#if it.live}<span class="ldot sm pulse hue-az"></span>{/if}Thinking {I.menu}</summary
				>
				<div class="tt serif">{it.text}</div>
			</details>
		{:else}
			<div class="agent" use:arrive={a}>
				<Markdown src={it.text} />{#if it.live}<span class="caret"></span>{/if}
			</div>
		{/if}
	{:else if it.kind === 'tools'}
		<div use:arrive={a}><ToolCalls calls={it.calls} live={it.live} /></div>
	{:else if it.kind === 'plan'}
		<div class="plan card-in">
			<div class="ph">
				<span class="pt serif">The recipe</span>
				<span class="pm">{planDone(it.entries)} of {it.entries.length}</span>
			</div>
			<div class="pl">
				{#each it.entries as e, i (i)}
					<div class="pe {e.status}">
						<StatusMark tone={planTone(e.status)} size="sm" />
						{#if e.status === 'completed'}<s>{e.content}</s>{:else}<span>{e.content}</span>{/if}
					</div>
				{/each}
			</div>
		</div>
	{:else if it.kind === 'notice'}
		<div class="notice mut" use:arrive={a}>{it.text}</div>
	{:else if it.kind === 'user'}
		<div class="user" use:arrive={a}>
			<div class="bubble" class:queued={!it.delivered}>{it.text}</div>
			<div class="um">
				{#if it.interrupt}<span class="hue-amb">interrupt · </span>{/if}
				{#if it.delivered}you · delivered {fmtClock(it.deliveredAt ?? it.ts)}{:else}queued — read
					when its turn ends{/if}
			</div>
		</div>
	{:else if it.kind === 'question'}
		<div class="q card-in" class:open={it.answer === undefined && !over} use:arrive={a}>
			<div class="qh">
				<PingPill type={it.type} /><span class="mut small">The agent asked</span>
			</div>
			<Markdown src={it.question} />
			{#if it.options.length && it.answer === undefined}
				<div class="small mut">options: {it.options.join(' · ')}</div>
			{/if}
			{#if it.answer !== undefined}
				<div class="ans small"><b>You:</b> {it.answer}</div>
			{:else if over}
				<div class="small fnt">Withdrawn — the run ended before an answer.</div>
			{:else}
				<button class="linklike wait" onclick={onanswer}
					><span class="ldot pulse"></span>Waiting for your answer {I.follow}</button
				>
			{/if}
		</div>
	{/if}
{/each}

{#each unechoed as s, i (i + s.text)}
	<div class="user" use:arrive={true}>
		<div class="bubble queued">{s.text}</div>
		<div class="um">
			{#if s.interrupt}<span class="hue-amb">interrupt · </span>{/if}sending…
		</div>
	</div>
{/each}

<style>
	.divider {
		display: flex;
		align-items: center;
		gap: 10px;
		margin-top: 4px;
	}
	.hl {
		flex: 1;
		height: 1px;
		min-width: 12px;
	}
	.hl.l {
		background: linear-gradient(90deg, transparent, var(--ln2));
	}
	.hl.r {
		background: linear-gradient(90deg, var(--ln2), transparent);
	}
	.sname {
		font-size: var(--fs-section);
		line-height: 1.1;
	}
	.smeta {
		font-size: var(--fs-help);
		color: var(--mut);
	}
	.smeta .mono {
		font-size: var(--fs-meta);
	}
	.status {
		font-size: var(--fs-label);
		display: flex;
		gap: 6px;
	}
	.prompt > summary {
		padding: 7px 12px;
		font-size: var(--fs-label);
		gap: 10px;
		min-width: 0;
	}
	.b {
		white-space: nowrap;
		font-weight: 600;
	}
	.prompt pre {
		margin: 0;
		padding: 8px 12px;
		border-top: 1px solid var(--ln);
		color: var(--mut);
		font-size: var(--fs-help);
		white-space: pre-wrap;
		word-break: break-word;
		max-height: 360px;
		overflow: auto;
	}
	.thought {
		padding: 8px 12px;
		border-radius: var(--r-card);
		background: linear-gradient(90deg, var(--azBg), transparent 70%);
	}
	.thought > summary {
		display: flex;
		gap: 7px;
		align-items: center;
		font-size: var(--fs-help);
		font-weight: 600;
		color: var(--az);
	}
	.tt {
		padding-top: 4px;
		color: var(--mut);
		font-size: var(--fs-title);
		line-height: 1.35;
		white-space: pre-wrap;
	}
	.agent {
		max-width: var(--measure);
		text-wrap: pretty;
	}
	.plan {
		display: flex;
		flex-direction: column;
	}
	.ph {
		padding: 8px 14px 4px;
		display: flex;
		justify-content: space-between;
		align-items: baseline;
	}
	.pt {
		font-size: var(--fs-section);
	}
	.pm {
		font-size: var(--fs-meta);
		color: var(--mut);
	}
	.pl {
		padding: 4px 14px 10px;
		display: flex;
		flex-direction: column;
		gap: 5px;
		font-size: var(--fs-ui);
	}
	.pe {
		display: flex;
		gap: 9px;
		align-items: center;
	}
	.pe.completed {
		color: var(--mut);
	}
	.pe.in_progress {
		color: var(--az);
		font-weight: 600;
	}
	.notice {
		font-style: italic;
		font-size: var(--fs-label);
	}
	.user {
		align-self: flex-end;
		max-width: 70%;
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		gap: 3px;
	}
	.bubble {
		padding: 8px 13px;
		border-radius: var(--r-row) var(--r-card) var(--r-xs) var(--r-card);
		background: var(--brandBg);
		border: 1px solid var(--brandLn);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.bubble.queued {
		background: transparent;
		border-style: dashed;
		color: var(--mut);
	}
	.um {
		font-size: var(--fs-id);
		color: var(--fnt);
	}
	.q {
		padding: 10px 14px;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.q.open {
		border-color: var(--ambLn);
	}
	.qh {
		display: flex;
		gap: 8px;
		align-items: center;
	}
	.ans {
		white-space: pre-wrap;
	}
	.wait {
		align-self: flex-start;
		color: var(--amb);
		font-weight: 600;
		font-size: var(--fs-label);
		gap: 8px;
	}
</style>
