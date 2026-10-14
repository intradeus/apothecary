<script lang="ts">
	// The run's conversation with its agent: what it was asked, what it said
	// and did (tool calls, plans), its questions, and your messages to it —
	// live while it works. Send queues a message for the end of the agent's
	// turn; Send now interrupts the turn so it reads the message at once.
	import { api } from '$lib/api';
	import type { RunEvent } from '$lib/domain';
	import { mdToHtml } from '$lib/planreview';
	import { buildTranscript, promptLabel, type LiveState } from '$lib/transcript';
	import ToolCalls from './ToolCalls.svelte';
	import { tick } from 'svelte';

	let {
		runId,
		events,
		live,
		active
	}: {
		runId: string;
		events: RunEvent[];
		live: LiveState;
		/** The run can still take messages (not finished). */
		active: boolean;
	} = $props();

	const items = $derived(buildTranscript(events, live));

	let draft = $state('');
	let sending = $state(false);
	let error = $state('');
	let scroller = $state<HTMLDivElement | null>(null);
	let stick = $state(true);

	// Follow the conversation while the reader is at the bottom; leave them
	// alone once they scroll up to read.
	function onScroll() {
		if (!scroller) return;
		stick = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 60;
	}
	$effect(() => {
		void items.length;
		void live;
		if (stick && scroller) {
			tick().then(() => scroller && (scroller.scrollTop = scroller.scrollHeight));
		}
	});

	async function send(interrupt: boolean) {
		const text = draft.trim();
		if (!text || sending) return;
		sending = true;
		error = '';
		try {
			await api.runs.sendMessage(runId, text, interrupt);
			draft = '';
			stick = true;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			sending = false;
		}
	}

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			send(e.metaKey || e.ctrlKey);
		}
	}

	const planIcon = (s: string) => (s === 'completed' ? '☑' : s === 'in_progress' ? '◐' : '☐');
</script>

<div class="chat">
	<div class="scroll" bind:this={scroller} onscroll={onScroll}>
		{#if items.length === 0}
			<div class="muted small empty">
				No conversation yet. When an agent stage starts, its prompt, replies and tool calls appear
				here as they happen.
			</div>
		{/if}
		{#each items as it (it.key)}
			{#if it.kind === 'stage'}
				<div class="stage">
					<span class="mono">{it.stageId}</span>
					<span class="muted">attempt {it.iteration}</span>
					{#if it.agent}<span class="muted">· {it.agent}</span>{/if}
					{#if it.model}<span class="muted mono">· {it.model}</span>{/if}
				</div>
			{:else if it.kind === 'status'}
				<div class="status {it.tone}">{it.text}</div>
			{:else if it.kind === 'prompt'}
				<details class="prompt">
					<summary>
						<span class="muted">↳ {promptLabel(it.promptKind)}</span>
						<span class="preview muted">{it.text.split('\n').find((l) => l.trim()) ?? ''}</span>
					</summary>
					<pre class="mono small">{it.text}{it.truncated ? '\n… [truncated]' : ''}</pre>
				</details>
			{:else if it.kind === 'message'}
				{#if it.thought}
					<details class="thought" open={it.live}>
						<summary class="muted small">Thinking{it.live ? '…' : ''}</summary>
						<div class="small muted thought-text">{it.text}</div>
					</details>
				{:else}
					<div class="agent md" class:live={it.live}>
						{@html mdToHtml(it.text)}{#if it.live}<span class="caret"></span>{/if}
					</div>
				{/if}
			{:else if it.kind === 'tools'}
				<ToolCalls calls={it.calls} live={it.live} />
			{:else if it.kind === 'plan'}
				<div class="plan">
					<div class="small muted">Plan</div>
					{#each it.entries as e, i (i)}
						<div class="small entry {e.status}">
							<span>{planIcon(e.status)}</span>
							<span>{e.content}</span>
						</div>
					{/each}
				</div>
			{:else if it.kind === 'notice'}
				<div class="notice small">{it.text}</div>
			{:else if it.kind === 'user'}
				<div class="user-row">
					<div class="user">
						<div style="white-space:pre-wrap">{it.text}</div>
						<div class="meta">
							{#if it.interrupt}<span>interrupt ·</span>{/if}
							{it.delivered ? 'delivered' : 'queued — the agent reads it when its turn ends'}
						</div>
					</div>
				</div>
			{:else if it.kind === 'question'}
				<div class="question" class:open={!it.answer}>
					<div class="small muted">The agent asked</div>
					<div class="md">{@html mdToHtml(it.question)}</div>
					{#if it.options.length && !it.answer}
						<div class="small muted">options: {it.options.join(' · ')}</div>
					{/if}
					{#if it.answer !== undefined}
						<div class="answer small"><strong>You:</strong> {it.answer}</div>
					{:else}
						<div class="small" style="color:var(--amber)">Waiting for your answer — above.</div>
					{/if}
				</div>
			{/if}
		{/each}
	</div>

	{#if active}
		<div class="composer">
			<textarea
				bind:value={draft}
				rows="2"
				placeholder="Message the agent — Enter queues it for the end of its turn, ⌘/Ctrl+Enter interrupts. Shift+Enter for a new line."
				onkeydown={onKey}
				disabled={sending}></textarea>
			<div class="actions">
				<button
					onclick={() => send(false)}
					disabled={sending || !draft.trim()}
					title="Delivered when the agent finishes its current turn"
				>
					Send
				</button>
				<button
					class="primary"
					onclick={() => send(true)}
					disabled={sending || !draft.trim()}
					title="Cancel the agent's turn now and have it read this"
				>
					Send now
				</button>
			</div>
		</div>
		{#if error}<div class="error small">{error}</div>{/if}
	{/if}
</div>

<style>
	.chat {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.scroll {
		max-height: 68vh;
		min-height: 200px;
		overflow-y: auto;
		padding-right: 6px;
	}
	.empty {
		padding: 24px 4px;
	}
	.stage {
		display: flex;
		gap: 6px;
		align-items: center;
		margin: 14px 0 6px;
		padding-bottom: 4px;
		border-bottom: 1px solid var(--border);
		font-size: 12px;
	}
	.status {
		font-size: 12px;
		margin: 6px 0;
	}
	.status.ok {
		color: var(--green);
	}
	.status.bad {
		color: var(--red);
	}
	.status.warn {
		color: var(--amber);
	}
	.status.info {
		color: var(--blue);
	}
	.prompt summary,
	.thought summary {
		cursor: pointer;
		font-size: 12px;
		display: flex;
		gap: 8px;
		overflow: hidden;
	}
	.prompt .preview {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		opacity: 0.7;
	}
	.prompt pre {
		max-height: 320px;
		overflow: auto;
		white-space: pre-wrap;
		background: var(--bg);
		border: 1px solid var(--border);
		border-radius: 6px;
		padding: 8px;
	}
	.thought-text {
		white-space: pre-wrap;
		font-style: italic;
		margin: 4px 0 6px 12px;
	}
	.agent {
		margin: 6px 0;
		line-height: 1.55;
	}
	.agent :global(p) {
		margin: 4px 0;
	}
	.agent :global(pre) {
		background: var(--bg);
		border: 1px solid var(--border);
		border-radius: 6px;
		padding: 8px;
		overflow: auto;
	}
	.caret {
		display: inline-block;
		width: 7px;
		height: 14px;
		background: var(--blue);
		vertical-align: text-bottom;
		animation: blink 1s steps(2) infinite;
	}
	@keyframes blink {
		50% {
			opacity: 0;
		}
	}
	.plan {
		margin: 8px 0;
		padding: 8px 10px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--panel2);
	}
	.entry {
		display: flex;
		gap: 8px;
	}
	.entry.completed {
		color: var(--muted);
		text-decoration: line-through;
	}
	.entry.in_progress {
		color: var(--blue);
	}
	.notice {
		color: var(--muted);
		font-style: italic;
		margin: 6px 0;
	}
	.user-row {
		display: flex;
		justify-content: flex-end;
		margin: 8px 0;
	}
	.user {
		max-width: 75%;
		background: color-mix(in srgb, var(--accent) 22%, var(--panel));
		border: 1px solid color-mix(in srgb, var(--accent) 45%, transparent);
		border-radius: 10px 10px 2px 10px;
		padding: 8px 10px;
	}
	.user .meta {
		font-size: 11px;
		color: var(--muted);
		margin-top: 4px;
		text-align: right;
	}
	.question {
		margin: 8px 0;
		padding: 8px 10px;
		border-radius: 8px;
		border: 1px solid var(--border);
	}
	.question.open {
		border-color: var(--amber);
	}
	.answer {
		margin-top: 6px;
		white-space: pre-wrap;
	}
	.composer {
		display: flex;
		gap: 8px;
		align-items: flex-end;
	}
	.composer textarea {
		flex: 1;
		resize: vertical;
	}
	.actions {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
</style>
