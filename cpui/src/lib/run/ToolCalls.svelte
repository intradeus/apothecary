<script lang="ts">
	// A run of consecutive tool calls (DESIGN 4.10 "Tool group"). Up to three
	// list every call; longer ones fold behind a summary with kind chips. A
	// live group (the agent is working through it now) never folds.
	import type { AgentToolCall } from '$lib/domain';
	import { editLines, lineCounts } from '$lib/diff';
	import { TOOL_HUE, hueOf } from '$lib/ui/hues';
	import { I } from '$lib/ui/icons';
	import CodeView from '$lib/ui/CodeView.svelte';
	import StatusMark, { type MarkTone } from '$lib/ui/StatusMark.svelte';

	let { calls, live = false }: { calls: AgentToolCall[]; live?: boolean } = $props();

	let open = $state(false);
	let expanded = $state<Record<string, boolean>>({});

	/** The kind's word (DESIGN 3.7); its hue comes from TOOL_HUE. */
	const WORD: Record<string, string> = {
		read: 'Read',
		edit: 'Edit',
		search: 'Search',
		execute: 'Exec',
		exec: 'Exec',
		delete: 'Delete',
		move: 'Move',
		fetch: 'Fetch',
		think: 'Think',
		other: 'Tool'
	};
	const word = (k: string) => WORD[k] ?? (k || 'Tool');
	const MAX_LINES = 400;
	const folded = $derived(!live && calls.length > 3 && !open);

	const byKind = $derived.by(() => {
		const m = new Map<string, { n: number; kind: string }>();
		for (const c of calls) {
			const l = word(c.kind).toLowerCase();
			const e = m.get(l);
			if (e) e.n++;
			else m.set(l, { n: 1, kind: c.kind });
		}
		return [...m].map(([l, e]) => ({ l, n: e.n, hue: hueOf(TOOL_HUE, e.kind) }));
	});
	const failed = $derived(calls.filter((c) => c.status === 'failed').length);

	const tone = (s: string): MarkTone =>
		s === 'completed' ? 'ok' : s === 'failed' ? 'bad' : s === 'in_progress' ? 'live' : 'pending';

	function title(c: AgentToolCall): string {
		if (c.locations[0]) return c.locations[0];
		return c.title || c.kind;
	}
	function counts(c: AgentToolCall) {
		let add = 0;
		let del = 0;
		for (const d of c.diffs) {
			const n = lineCounts(editLines(d));
			add += n.add;
			del += n.del;
		}
		return { add, del };
	}
</script>

{#if folded}
	<div class="card-in">
		<button class="rowbtn fold" onclick={() => (open = true)}>
			<span class="b">{I.caret} {calls.length} tool calls</span>
			<span class="chips">
				{#each byKind as k (k.l)}<span class="pill sm flat sq t-{k.hue}">{k.l} {k.n}</span>{/each}
			</span>
			{#if failed}<span class="bad hue-ros">{failed} failed</span>{/if}
		</button>
	</div>
{:else}
	<div class="group card-in" class:live>
		{#if !live && calls.length > 3}
			<button class="rowbtn head mut" onclick={() => (open = false)}
				><span class="b">{I.menu} {calls.length} tool calls</span></button
			>
		{/if}
		{#each calls as c (c.toolCallId)}
			{@const n = c.diffs.length ? counts(c) : null}
			<div class="call">
				<button
					class="rowbtn head"
					aria-expanded={!!expanded[c.toolCallId]}
					onclick={() => (expanded = { ...expanded, [c.toolCallId]: !expanded[c.toolCallId] })}
				>
					<StatusMark tone={tone(c.status)} size="sm" title={c.status.replace('_', ' ')} />
					<span class="k hue-{hueOf(TOOL_HUE, c.kind)}">{word(c.kind)}</span>
					<span class="path mono ell" title={c.title}>{title(c)}</span>
					{#if n}<span class="mono nums"
							><span class="hue-grn">+{n.add}</span> <span class="hue-ros">−{n.del}</span></span
						>{/if}
					{#if c.status === 'in_progress'}<span class="ip hue-az">in progress</span>{/if}
				</button>
				{#if expanded[c.toolCallId]}
					<div class="detail">
						{#if c.title && c.title !== title(c)}<div class="lbl">{c.title}</div>{/if}
						{#if c.input}
							<div class="lbl">input</div>
							<pre class="log">{c.input}</pre>
						{/if}
						{#each c.diffs as d (d.path)}
							{@const dl = editLines(d)}
							<div class="lbl mono">{d.path}</div>
							<CodeView
								lines={dl.slice(0, MAX_LINES)}
								maxHeight={320}
								truncated={dl.length > MAX_LINES
									? `showing ${MAX_LINES} of ${dl.length} lines`
									: undefined}
							/>
						{/each}
						{#if c.output}
							<div class="lbl">output</div>
							<pre class="log">{c.output}</pre>
						{/if}
						{#if !c.input && !c.output && !c.diffs.length}
							<div class="lbl">no details reported</div>
						{/if}
					</div>
				{/if}
			</div>
		{/each}
	</div>
{/if}

<style>
	.fold {
		padding: 7px 12px;
		gap: 10px;
		border-radius: inherit;
		font-size: var(--fs-label);
	}
	.b {
		font-weight: 600;
		white-space: nowrap;
	}
	.bad {
		margin-left: auto;
		font-weight: 600;
		white-space: nowrap;
	}
	.group {
		overflow: hidden;
		display: flex;
		flex-direction: column;
	}
	.group.live {
		border-color: var(--azLn);
	}
	.call + .call,
	.head.mut + .call {
		border-top: 1px solid var(--ln);
	}
	.head {
		padding: 7px 12px;
		gap: 10px;
		border-radius: 0;
		font-size: var(--fs-label);
	}
	.head.mut {
		color: var(--mut);
	}
	.k {
		font-weight: 600;
		flex: none;
	}
	.path {
		flex: 1;
	}
	.nums {
		font-size: var(--fs-meta);
		white-space: nowrap;
	}
	.ip {
		font-size: var(--fs-help);
		white-space: nowrap;
	}
	.detail {
		padding: 0 8px 8px;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.lbl {
		padding: 0 4px;
	}
	pre.log {
		max-height: 260px;
		font-size: var(--fs-meta);
	}
</style>
