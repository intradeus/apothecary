<script lang="ts">
	// A run of consecutive tool calls. Short runs list every call; long ones
	// fold behind a one-line summary so the conversation stays readable. A
	// live group (the agent is working through it now) is always open.
	import type { AgentToolCall } from '$lib/domain';

	let { calls, live = false }: { calls: AgentToolCall[]; live?: boolean } = $props();

	let open = $state(false);
	let expanded = $state<Record<string, boolean>>({});

	const icons: Record<string, string> = {
		read: '📖',
		edit: '✏️',
		delete: '🗑',
		move: '↔',
		search: '🔍',
		execute: '▶',
		think: '💭',
		fetch: '🌐'
	};
	const icon = (k: string) => icons[k] ?? '🔧';
	const folded = $derived(!live && calls.length > 3 && !open);

	const summary = $derived.by(() => {
		const byKind = new Map<string, number>();
		for (const c of calls) byKind.set(c.kind, (byKind.get(c.kind) ?? 0) + 1);
		return [...byKind].map(([k, n]) => `${icon(k)} ${n}`).join('  ');
	});
	const failed = $derived(calls.filter((c) => c.status === 'failed').length);

	function title(c: AgentToolCall): string {
		if (c.title) return c.title;
		return c.locations[0] ?? c.kind;
	}

	/** A diff as -old/+new lines (the agent's own edit, not git's view). */
	function diffLines(d: { oldText?: string; newText: string }): { t: string; cls: string }[] {
		const out: { t: string; cls: string }[] = [];
		const oldL = d.oldText ? d.oldText.split('\n') : [];
		const newL = d.newText.split('\n');
		// Trim the shared head and tail so a one-line edit shows as one line.
		let head = 0;
		while (head < oldL.length && head < newL.length && oldL[head] === newL[head]) head++;
		let tail = 0;
		while (
			tail < oldL.length - head &&
			tail < newL.length - head &&
			oldL[oldL.length - 1 - tail] === newL[newL.length - 1 - tail]
		)
			tail++;
		if (head > 0) out.push({ t: `… ${head} unchanged line${head > 1 ? 's' : ''}`, cls: 'ctx' });
		for (const l of oldL.slice(head, oldL.length - tail)) out.push({ t: '-' + l, cls: 'del' });
		for (const l of newL.slice(head, newL.length - tail)) out.push({ t: '+' + l, cls: 'add' });
		if (tail > 0) out.push({ t: `… ${tail} unchanged line${tail > 1 ? 's' : ''}`, cls: 'ctx' });
		return out.slice(0, 400);
	}
</script>

<div class="tools" class:live>
	{#if folded}
		<button class="fold" onclick={() => (open = true)}>
			▸ {calls.length} tool calls <span class="muted">{summary}</span>
			{#if failed}<span class="bad">· {failed} failed</span>{/if}
		</button>
	{:else}
		{#if !live && calls.length > 3}
			<button class="fold" onclick={() => (open = false)}>▾ {calls.length} tool calls</button>
		{/if}
		{#each calls as c (c.toolCallId)}
			<div class="call">
				<button
					class="head"
					onclick={() => (expanded = { ...expanded, [c.toolCallId]: !expanded[c.toolCallId] })}
				>
					<span class="status {c.status}" title={c.status}>
						{#if c.status === 'completed'}✓{:else if c.status === 'failed'}✗{:else}<span
								class="spin"
							></span>{/if}
					</span>
					<span class="icon">{icon(c.kind)}</span>
					<span class="mono title">{title(c)}</span>
				</button>
				{#if expanded[c.toolCallId]}
					<div class="detail">
						{#if c.locations.length}
							<div class="small muted mono">{c.locations.join('  ')}</div>
						{/if}
						{#if c.input}
							<div class="label">input</div>
							<pre class="mono small">{c.input}</pre>
						{/if}
						{#each c.diffs as d (d.path)}
							<div class="label mono">{d.path}</div>
							<pre class="mono small diff">{#each diffLines(d) as l, i (i)}<span class={l.cls}
										>{l.t}
</span>{/each}</pre>
						{/each}
						{#if c.output}
							<div class="label">output</div>
							<pre class="mono small">{c.output}</pre>
						{/if}
						{#if !c.input && !c.output && !c.diffs.length}
							<div class="small muted">no details reported</div>
						{/if}
					</div>
				{/if}
			</div>
		{/each}
	{/if}
</div>

<style>
	.tools {
		margin: 4px 0 4px 2px;
		padding-left: 10px;
		border-left: 2px solid var(--border);
	}
	.tools.live {
		border-left-color: var(--blue);
	}
	.fold,
	.head {
		background: none;
		border: none;
		color: var(--text);
		font: inherit;
		cursor: pointer;
		padding: 2px 0;
		text-align: left;
		display: flex;
		align-items: center;
		gap: 6px;
		width: 100%;
	}
	.fold {
		font-size: 12px;
		color: var(--muted);
	}
	.fold:hover,
	.head:hover .title {
		color: var(--text);
		text-decoration: underline;
	}
	.title {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--muted);
	}
	.status {
		width: 14px;
		text-align: center;
		font-size: 12px;
	}
	.status.completed {
		color: var(--green);
	}
	.status.failed,
	.bad {
		color: var(--red);
	}
	.spin {
		display: inline-block;
		width: 9px;
		height: 9px;
		border: 2px solid var(--blue);
		border-right-color: transparent;
		border-radius: 50%;
		animation: spin 0.9s linear infinite;
	}
	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
	.icon {
		font-size: 12px;
	}
	.detail {
		margin: 2px 0 8px 20px;
	}
	.label {
		font-size: 11px;
		color: var(--muted);
		margin-top: 6px;
	}
	pre {
		margin: 2px 0 0;
		padding: 6px 8px;
		max-height: 260px;
		overflow: auto;
		white-space: pre-wrap;
		word-break: break-word;
		background: var(--bg);
		border: 1px solid var(--border);
		border-radius: 6px;
	}
	.diff .add {
		color: var(--green);
	}
	.diff .del {
		color: var(--red);
	}
	.diff .ctx {
		color: var(--muted);
	}
</style>
