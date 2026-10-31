<script lang="ts">
	// A run of consecutive tool calls (DESIGN 4.10 "Tool group"). Up to three
	// list every call; longer ones fold behind a summary with kind chips. A
	// live group (the agent is working through it now) never folds.
	import type { AgentToolCall } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import { editLines, lineCounts } from '$lib/run/diff';
	import { TOOL_HUE, hueOf } from '$lib/design/hues';
	import { I } from '$lib/design/icons';
	import CodeView from '$lib/ui/CodeView.svelte';
	import StatusMark, { type MarkTone } from '$lib/ui/StatusMark.svelte';

	let { calls, live = false }: { calls: AgentToolCall[]; live?: boolean } = $props();

	let open = $state(false);
	let expanded = $state<Record<string, boolean>>({});

	/** The kind's word (DESIGN 3.7); its hue comes from TOOL_HUE. */
	const word = (k: string): string => {
		switch (k) {
			case 'read':
				return t('run.tool.read');
			case 'edit':
				return t('run.tool.edit');
			case 'search':
				return t('run.tool.search');
			case 'execute':
			case 'exec':
				return t('run.tool.exec');
			case 'delete':
				return t('run.tool.delete');
			case 'move':
				return t('run.tool.move');
			case 'fetch':
				return t('run.tool.fetch');
			case 'think':
				return t('run.tool.think');
			default:
				return k && k !== 'other' ? k : t('run.tool.other');
		}
	};
	const statusWord = (s: string): string => {
		switch (s) {
			case 'completed':
				return t('run.tool.status.completed');
			case 'failed':
				return t('run.tool.status.failed');
			case 'in_progress':
				return t('run.tool.status.inProgress');
			case 'pending':
				return t('run.tool.status.pending');
			default:
				return s.replace('_', ' ');
		}
	};
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
			<span class="b">{I.caret} {t('run.tool.calls', { count: calls.length })}</span>
			<span class="chips">
				{#each byKind as k (k.l)}<span class="pill sm flat sq tint-{k.hue}">{k.l} {k.n}</span
					>{/each}
			</span>
			{#if failed}<span class="bad hue-danger">{t('run.tool.failedCount', { count: failed })}</span
				>{/if}
		</button>
	</div>
{:else}
	<div class="group card-in" class:live>
		{#if !live && calls.length > 3}
			<button class="rowbtn head muted" onclick={() => (open = false)}
				><span class="b">{I.menu} {t('run.tool.calls', { count: calls.length })}</span></button
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
					<StatusMark tone={tone(c.status)} size="sm" title={statusWord(c.status)} />
					<span class="k hue-{hueOf(TOOL_HUE, c.kind)}">{word(c.kind)}</span>
					<span class="path mono truncate" title={c.title}>{title(c)}</span>
					{#if n}<span class="mono nums"
							><span class="hue-success">+{n.add}</span>
							<span class="hue-danger">−{n.del}</span></span
						>{/if}
					{#if c.status === 'in_progress'}<span class="ip hue-running"
							>{t('run.tool.status.inProgress')}</span
						>{/if}
				</button>
				{#if expanded[c.toolCallId]}
					<div class="detail">
						{#if c.title && c.title !== title(c)}<div class="caption">{c.title}</div>{/if}
						{#if c.input}
							<div class="caption">{t('run.tool.input')}</div>
							<pre class="log">{c.input}</pre>
						{/if}
						{#each c.diffs as d, i (i)}
							{@const dl = editLines(d)}
							<div class="caption mono">{d.path}</div>
							<CodeView
								lines={dl.slice(0, MAX_LINES)}
								maxHeight={320}
								truncated={dl.length > MAX_LINES
									? t('run.tool.showing', { shown: MAX_LINES, total: dl.length })
									: undefined}
							/>
						{/each}
						{#if c.output}
							<div class="caption">{t('run.tool.output')}</div>
							<pre class="log">{c.output}</pre>
						{/if}
						{#if !c.input && !c.output && !c.diffs.length}
							<div class="caption">{t('run.tool.noDetails')}</div>
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
		border-color: var(--running-line);
	}
	.call + .call,
	.head.muted + .call {
		border-top: 1px solid var(--line);
	}
	.head {
		padding: 7px 12px;
		gap: 10px;
		border-radius: 0;
		font-size: var(--fs-label);
	}
	.head.muted {
		color: var(--text-muted);
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
	.caption {
		padding: 0 4px;
	}
	pre.log {
		max-height: 260px;
		font-size: var(--fs-meta);
	}
</style>
