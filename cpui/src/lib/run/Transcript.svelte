<script lang="ts">
	// The run's conversation (DESIGN 4.10): stage dividers, status lines, the
	// prompt, agent messages (streaming with a caret), thinking, tool groups,
	// the plan, your messages and the agent's questions.
	import { mdToHtml } from '$lib/planreview';
	import { promptLabel, type ChatItem } from '$lib/transcript';
	import { fmtClock, fmtTime } from '$lib/format';
	import { lore } from '$lib/lore';
	import PingPill from '$lib/ui/PingPill.svelte';
	import ToolCalls from './ToolCalls.svelte';

	let {
		items,
		over = false,
		onlog,
		onanswer
	}: {
		items: ChatItem[];
		/** The run is finished: an unanswered question was withdrawn with it. */
		over?: boolean;
		/** Open a stage's log. */
		onlog: (stage: string) => void;
		/** Jump to where a pending question is answered. */
		onanswer: () => void;
	} = $props();

	const planDone = (es: { status: string }[]) => es.filter((e) => e.status === 'completed').length;
</script>

{#each items as it (it.key)}
	{#if it.kind === 'stage'}
		<div class="divider">
			<span class="hl l"></span>
			<span class="sname">{it.stageId}</span>
			<span class="smeta"
				>attempt {it.iteration}{#if it.agent}&nbsp;· {it.agent}{/if}{#if it.model}&nbsp;· {it.model}{/if}{#if !it.agentic}&nbsp;·
					stage{/if} · <span class="mono">{fmtTime(it.ts)}</span></span
			>
			<span class="hl r"></span>
		</div>
	{:else if it.kind === 'status'}
		<div class="status {it.tone}">
			<span>{it.glyph}</span>
			<span
				>{it.text}{#if it.meta}<span class="sm">{' · '}{it.meta}</span>{/if}</span
			>
		</div>
	{:else if it.kind === 'failure'}
		<div class="fail">
			<b>✕ {it.title}</b>
			{#if it.error}<span class="err">{it.error}</span>{/if}
			<div class="acts">
				<button class="btn-sm" onclick={() => onlog(it.stageId)}>View log</button>
			</div>
		</div>
	{:else if it.kind === 'prompt'}
		<details class="card-in prompt">
			<summary>
				<span class="b"><span class="caretglyph">▸</span> {promptLabel(it.promptKind)}</span>
				<span class="pv">{it.text.split('\n').find((l) => l.trim()) ?? ''}</span>
			</summary>
			<pre>{it.text}{it.truncated ? '\n… [truncated]' : ''}</pre>
		</details>
	{:else if it.kind === 'message'}
		{#if it.thought}
			<details class="thought" open={it.live}>
				<summary
					>{#if it.live}<span class="td pulse"></span>{/if}Thinking{it.live ? '' : ''} ▾</summary
				>
				<div class="tt">{it.text}</div>
			</details>
		{:else}
			<div class="agent md">
				{@html mdToHtml(it.text)}{#if it.live}<span class="caret"></span>{/if}
			</div>
		{/if}
	{:else if it.kind === 'tools'}
		<ToolCalls calls={it.calls} live={it.live} />
	{:else if it.kind === 'plan'}
		<div class="plan card-in">
			<div class="ph">
				<span class="pt">{lore('plan')}</span>
				<span class="pm">{planDone(it.entries)} of {it.entries.length}</span>
			</div>
			<div class="pl">
				{#each it.entries as e, i (i)}
					<div class="pe {e.status}">
						<span class="pc">
							{#if e.status === 'completed'}✓{:else if e.status === 'in_progress'}<span
									class="pd pulse"
								></span>{/if}
						</span>
						{#if e.status === 'completed'}<s>{e.content}</s>{:else}<span>{e.content}</span>{/if}
					</div>
				{/each}
			</div>
		</div>
	{:else if it.kind === 'notice'}
		<div class="notice">{it.text}</div>
	{:else if it.kind === 'user'}
		<div class="user">
			<div class="bubble" class:queued={!it.delivered}>{it.text}</div>
			<div class="um">
				{#if it.interrupt}<span class="hue-amb">interrupt · </span>{/if}
				{#if it.delivered}you · delivered {fmtClock(it.deliveredAt ?? it.ts)}{:else}queued — read
					when its turn ends{/if}
			</div>
		</div>
	{:else if it.kind === 'question'}
		<div class="q card-in" class:open={it.answer === undefined && !over}>
			<div class="qh">
				<PingPill type={it.type} /><span class="mut small">The agent asked</span>
			</div>
			<div class="md">{@html mdToHtml(it.question)}</div>
			{#if it.options.length && it.answer === undefined}
				<div class="small mut">options: {it.options.join(' · ')}</div>
			{/if}
			{#if it.answer !== undefined}
				<div class="ans small"><b>You:</b> {it.answer}</div>
			{:else if over}
				<div class="small fnt">Withdrawn — the run ended before an answer.</div>
			{:else}
				<button class="linklike wait" onclick={onanswer}
					><span class="wd pulse"></span>Waiting for your answer ↓</button
				>
			{/if}
		</div>
	{/if}
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
		font-family: var(--serif);
		font-style: italic;
		font-size: 19px;
		line-height: 1.1;
	}
	.smeta {
		font-size: 11.5px;
		color: var(--mut);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}
	.smeta .mono {
		font-size: 11px;
	}
	.status {
		font-size: 12px;
		display: flex;
		gap: 6px;
		color: var(--mut);
	}
	.status.ok {
		color: var(--grn);
	}
	.status.bad {
		color: var(--ros);
	}
	.status.warn {
		color: var(--amb);
	}
	.status.info {
		color: var(--az);
	}
	.sm {
		color: var(--mut);
	}
	.fail {
		border-radius: 14px;
		background: var(--rosBg);
		border: 1px solid var(--rosLn);
		padding: 11px 14px;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.fail b {
		color: var(--ros);
		font-weight: 600;
	}
	.fail .err {
		font-family: var(--mono);
		font-size: 11.5px;
		white-space: pre-wrap;
		word-break: break-word;
		max-height: 140px;
		overflow: auto;
	}
	.acts {
		display: flex;
		gap: 6px;
		margin-top: 4px;
	}
	.prompt > summary {
		padding: 7px 12px;
		display: flex;
		gap: 10px;
		align-items: center;
		font-size: 12px;
		min-width: 0;
	}
	.b {
		white-space: nowrap;
		font-weight: 600;
	}
	.pv {
		color: var(--mut);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}
	.prompt pre {
		margin: 0;
		padding: 8px 12px;
		border-top: 1px solid var(--ln);
		color: var(--mut);
		font-size: 11.5px;
		white-space: pre-wrap;
		word-break: break-word;
		max-height: 360px;
		overflow: auto;
	}
	.thought {
		padding: 8px 12px;
		border-radius: 13px;
		background: linear-gradient(90deg, var(--azBg), transparent 70%);
	}
	.thought > summary {
		display: flex;
		gap: 7px;
		align-items: center;
		font-size: 11.5px;
		font-weight: 600;
		color: var(--az);
	}
	.td {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--az);
		box-shadow: 0 0 8px var(--az);
	}
	.tt {
		padding-top: 4px;
		color: var(--mut);
		font-family: var(--serif);
		font-style: italic;
		font-size: 16px;
		line-height: 1.35;
		white-space: pre-wrap;
	}
	.agent {
		max-width: 70ch;
		text-wrap: pretty;
	}
	.plan {
		border-radius: 14px;
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
		font-family: var(--serif);
		font-size: 18px;
	}
	.pm {
		font-size: 11px;
		color: var(--mut);
	}
	.pl {
		padding: 4px 14px 10px;
		display: flex;
		flex-direction: column;
		gap: 5px;
		font-size: 12.5px;
	}
	.pe {
		display: flex;
		gap: 9px;
		align-items: center;
	}
	.pc {
		width: 15px;
		height: 15px;
		flex: none;
		border-radius: 50%;
		border: 1.5px solid var(--ln2);
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 9px;
		font-weight: 800;
	}
	.pe.completed {
		color: var(--mut);
	}
	.pe.completed .pc {
		background: var(--grn);
		border-color: var(--grn);
		color: #0b1a12;
	}
	.pe.in_progress {
		color: var(--az);
		font-weight: 600;
	}
	.pe.in_progress .pc {
		border-color: var(--az);
	}
	.pd {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--az);
		box-shadow: 0 0 6px var(--az);
	}
	.notice {
		color: var(--mut);
		font-style: italic;
		font-size: 12px;
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
		border-radius: 10px 14px 4px 14px;
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
		font-size: 10.5px;
		color: var(--fnt);
	}
	.q {
		border-radius: 14px;
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
		font-size: 12px;
		gap: 8px;
	}
	.wd {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--amb);
		box-shadow: 0 0 8px var(--amb);
	}
</style>
