<script lang="ts">
	// A run of consecutive tool calls (DESIGN 4.10 "Tool group"). Up to three
	// list every call; longer ones fold behind a summary with kind chips. A
	// live group (the agent is working through it now) never folds.
	import type { AgentToolCall } from '$lib/domain';

	let { calls, live = false }: { calls: AgentToolCall[]; live?: boolean } = $props();

	let open = $state(false);
	let expanded = $state<Record<string, boolean>>({});

	// Kind → [label, hue] (DESIGN 3.7).
	const KIND: Record<string, [string, string]> = {
		read: ['Read', 'az'],
		edit: ['Edit', 'amb'],
		search: ['Search', 'vio'],
		execute: ['Exec', 'grn'],
		exec: ['Exec', 'grn'],
		delete: ['Delete', 'ros'],
		move: ['Move', 'tea'],
		fetch: ['Fetch', 'az'],
		think: ['Think', 'mut'],
		other: ['Tool', 'mut']
	};
	const kind = (k: string) => KIND[k] ?? [k || 'Tool', 'mut'];
	const folded = $derived(!live && calls.length > 3 && !open);

	const byKind = $derived.by(() => {
		const m = new Map<string, number>();
		for (const c of calls) {
			const l = kind(c.kind)[0].toLowerCase();
			m.set(l, (m.get(l) ?? 0) + 1);
		}
		return [...m].map(([l, n]) => ({
			l,
			n,
			hue: Object.values(KIND).find((x) => x[0].toLowerCase() === l)?.[1] ?? 'mut'
		}));
	});
	const failed = $derived(calls.filter((c) => c.status === 'failed').length);

	function title(c: AgentToolCall): string {
		if (c.locations[0]) return c.locations[0];
		return c.title || c.kind;
	}

	type DLine = { t: string; cls: string; n?: number };
	/** The agent's own edit as −old/+new lines (not git's view). */
	function diffLines(d: { oldText?: string; newText: string }): DLine[] {
		const out: DLine[] = [];
		const oldL = d.oldText ? d.oldText.split('\n') : [];
		const newL = d.newText.split('\n');
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
		oldL
			.slice(head, oldL.length - tail)
			.forEach((l, i) => out.push({ t: '−' + l, cls: 'del', n: head + i + 1 }));
		newL
			.slice(head, newL.length - tail)
			.forEach((l, i) => out.push({ t: '+' + l, cls: 'add', n: head + i + 1 }));
		if (tail > 0) out.push({ t: `… ${tail} unchanged line${tail > 1 ? 's' : ''}`, cls: 'ctx' });
		return out;
	}
	function counts(c: AgentToolCall) {
		let a = 0;
		let r = 0;
		for (const d of c.diffs)
			for (const l of diffLines(d)) {
				if (l.cls === 'add') a++;
				else if (l.cls === 'del') r++;
			}
		return { a, r };
	}
</script>

{#if folded}
	<button class="fold" onclick={() => (open = true)}>
		<span class="b">▸ {calls.length} tool calls</span>
		<span class="chips">
			{#each byKind as k (k.l)}<span class="kc t-{k.hue}">{k.l} {k.n}</span>{/each}
		</span>
		{#if failed}<span class="bad">{failed} failed</span>{/if}
	</button>
{:else}
	<div class="group" class:live>
		{#if !live && calls.length > 3}
			<button class="fold flat" onclick={() => (open = false)}
				><span class="b">▾ {calls.length} tool calls</span></button
			>
		{/if}
		{#each calls as c (c.toolCallId)}
			{@const k = kind(c.kind)}
			{@const n = c.diffs.length ? counts(c) : null}
			<div class="call">
				<button
					class="head"
					onclick={() => (expanded = { ...expanded, [c.toolCallId]: !expanded[c.toolCallId] })}
				>
					<span class="st {c.status}" title={c.status.replace('_', ' ')}>
						{#if c.status === 'completed'}✓{:else if c.status === 'failed'}✕{:else if c.status === 'in_progress'}<span
								class="pd pulse"
							></span>{:else}<span class="hollow"></span>{/if}
					</span>
					<span class="k hue-{k[1]}">{k[0]}</span>
					<span class="path" title={c.title}>{title(c)}</span>
					{#if n}<span class="nums"
							><span class="hue-grn">+{n.a}</span> <span class="hue-ros">−{n.r}</span></span
						>{/if}
					{#if c.status === 'in_progress'}<span class="ip">in progress</span>{/if}
				</button>
				{#if expanded[c.toolCallId]}
					<div class="detail">
						{#if c.title && c.title !== title(c)}<div class="lbl">{c.title}</div>{/if}
						{#if c.input}
							<div class="lbl">input</div>
							<pre>{c.input}</pre>
						{/if}
						{#each c.diffs as d (d.path)}
							{@const dl = diffLines(d)}
							<div class="lbl mono">{d.path}</div>
							<div class="diff">
								{#each dl.slice(0, 400) as l, i (i)}
									<div class="dl {l.cls}">
										<span class="ln">{l.n ?? ''}</span><span>{l.t}</span>
									</div>
								{/each}
								{#if dl.length > 400}<div class="trunc">
										truncated · showing 400 of {dl.length} lines
									</div>{/if}
							</div>
						{/each}
						{#if c.output}
							<div class="lbl">output</div>
							<pre>{c.output}</pre>
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
		all: unset;
		box-sizing: border-box;
		cursor: pointer;
		border-radius: 13px;
		background: var(--p2);
		border: 1px solid var(--ln);
		padding: 7px 12px;
		display: flex;
		gap: 10px;
		align-items: center;
		font-size: 12px;
	}
	.fold:hover {
		border-color: var(--ln2);
	}
	.fold.flat {
		background: transparent;
		border: 0;
		border-bottom: 1px solid var(--ln);
		border-radius: 0;
		color: var(--mut);
	}
	.b {
		font-weight: 600;
		white-space: nowrap;
	}
	.chips {
		display: flex;
		gap: 4px;
		font-size: 11px;
		flex-wrap: wrap;
	}
	.kc {
		padding: 0 7px;
		border-radius: 12px;
		border: 0;
	}
	.t-mut {
		background: var(--p3);
		color: var(--mut);
	}
	.t-az {
		background: var(--azBg);
		color: var(--az);
	}
	.t-amb {
		background: var(--ambBg);
		color: var(--amb);
	}
	.t-vio {
		background: var(--vioBg);
		color: var(--vio);
	}
	.t-grn {
		background: var(--grnBg);
		color: var(--grn);
	}
	.t-ros {
		background: var(--rosBg);
		color: var(--ros);
	}
	.t-tea {
		background: var(--teaBg);
		color: var(--tea);
	}
	.bad {
		margin-left: auto;
		color: var(--ros);
		font-weight: 600;
		white-space: nowrap;
	}
	.group {
		border-radius: 14px;
		background: var(--p2);
		border: 1px solid var(--ln);
		overflow: hidden;
		display: flex;
		flex-direction: column;
	}
	.group.live {
		border-color: var(--azLn);
	}
	.call + .call {
		border-top: 1px solid var(--ln);
	}
	.head {
		all: unset;
		box-sizing: border-box;
		cursor: pointer;
		width: 100%;
		padding: 7px 12px;
		display: flex;
		gap: 10px;
		align-items: center;
		font-size: 12px;
	}
	.head:hover {
		background: var(--p1);
	}
	.st {
		width: 12px;
		flex: none;
		display: flex;
		justify-content: center;
		font-weight: 700;
	}
	.st.completed {
		color: var(--grn);
	}
	.st.failed {
		color: var(--ros);
	}
	.pd {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--az);
		box-shadow: 0 0 8px var(--az);
	}
	.hollow {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		border: 1.5px solid var(--mut);
	}
	.k {
		font-weight: 600;
		flex: none;
	}
	.hue-mut {
		color: var(--mut);
	}
	.path {
		flex: 1;
		min-width: 0;
		font-family: var(--mono);
		font-size: 11.5px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.nums {
		font-family: var(--mono);
		font-size: 11px;
		white-space: nowrap;
	}
	.ip {
		color: var(--az);
		font-size: 11.5px;
		white-space: nowrap;
	}
	.detail {
		padding: 0 8px 8px;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.lbl {
		font-size: 11px;
		color: var(--mut);
		padding: 0 4px;
	}
	pre {
		margin: 0;
		padding: 6px 8px;
		max-height: 260px;
		overflow: auto;
		white-space: pre-wrap;
		word-break: break-word;
		font-family: var(--mono);
		font-size: 11px;
		background: var(--code);
		border-radius: 8px;
	}
	.diff {
		font-family: var(--mono);
		font-size: 11px;
		border-radius: 8px;
		overflow: auto;
		max-height: 320px;
		background: var(--code);
	}
	.dl {
		display: grid;
		grid-template-columns: 36px 1fr;
		white-space: pre;
	}
	.dl .ln {
		text-align: right;
		padding-right: 8px;
		opacity: 0.6;
	}
	.dl.add {
		background: var(--grnBg);
		color: var(--grn);
	}
	.dl.del {
		background: var(--rosBg);
		color: var(--ros);
	}
	.dl.ctx {
		color: var(--mut);
	}
	.trunc {
		padding: 4px 8px;
		color: var(--mut);
		font-family: var(--sans);
	}
</style>
