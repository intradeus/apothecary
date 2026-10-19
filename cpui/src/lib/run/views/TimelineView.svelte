<script lang="ts">
	// Timeline (DESIGN §6): every lifecycle event, with the log of a finished
	// attempt and captured files one click away.
	import { api } from '$lib/api';
	import { fmtAbs, fmtTime } from '$lib/format';
	import { TRANSCRIPT_TYPES, describe, hasLog } from '../derive';
	import type { RunStore } from '../store.svelte';

	let { rs }: { rs: RunStore } = $props();

	const rows = $derived(
		rs.events.filter((e) => !TRANSCRIPT_TYPES.has(e.type)).map((e) => ({ e, d: describe(e) }))
	);
	const MARK = { ok: '✓', bad: '✕', warn: '!', info: '•', '': '·' } as const;

	let logSeq = $state<number | null>(null);
	let logText = $state('');

	async function showLog(seq: number, stage: string) {
		if (logSeq === seq) {
			logSeq = null;
			return;
		}
		logSeq = seq;
		logText = 'loading…';
		try {
			logText = (await api.runs.stageLog(rs.runId, stage)) || '(empty log)';
		} catch (e) {
			logText = `no log: ${e instanceof Error ? e.message : e}`;
		}
	}
</script>

<div class="tl">
	{#if rows.length === 0}
		<div class="none"><span class="d pulse"></span>Connecting…</div>
	{/if}
	{#each rows as { e, d } (e.seq)}
		<div class="r">
			<span class="t" title={fmtAbs(e.ts)}>{fmtTime(e.ts)}</span>
			<span class="m {d.tone || 'mut'}">{MARK[d.tone]}</span>
			<span class="x">{d.text}</span>
			<span class="a">
				{#if hasLog(e, d.stage, rs.events, rs.terminal)}
					<button class="linklike" onclick={() => showLog(e.seq, d.stage!)}
						>{logSeq === e.seq ? 'hide log' : 'log'}</button
					>
				{/if}
				{#if d.snapshot}
					<a href={api.files.snapshotUrl(d.snapshot)} target="_blank" rel="noreferrer">open ↗</a>
				{/if}
			</span>
		</div>
		{#if logSeq === e.seq}
			<pre class="log">{logText}</pre>
		{/if}
	{/each}
</div>

<style>
	.tl {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: 10px 12px;
	}
	.none {
		color: var(--mut);
		font-size: 12px;
		padding: 8px;
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.d {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--az);
	}
	.r {
		display: grid;
		grid-template-columns: 66px 22px minmax(0, 1fr) auto;
		gap: 8px;
		padding: 5px 8px;
		align-items: center;
		border-radius: 8px;
	}
	.r:hover {
		background: var(--p2);
	}
	.t {
		font-family: var(--mono);
		font-size: 10.5px;
		color: var(--fnt);
	}
	.m {
		width: 18px;
		height: 18px;
		border-radius: 50%;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 10px;
		font-weight: 700;
	}
	.m.ok {
		background: var(--grnBg);
		color: var(--grn);
	}
	.m.bad {
		background: var(--rosBg);
		color: var(--ros);
	}
	.m.warn {
		background: var(--ambBg);
		color: var(--amb);
	}
	.m.info {
		background: var(--azBg);
		color: var(--az);
	}
	.m.mut {
		background: var(--p2);
		color: var(--mut);
	}
	.x {
		font-size: 12.5px;
		overflow-wrap: anywhere;
	}
	.a {
		display: flex;
		gap: 10px;
		font-size: 11.5px;
		font-weight: 500;
		white-space: nowrap;
	}
	.a .linklike,
	.a a {
		color: var(--brand);
		font-size: 11.5px;
		font-weight: 500;
	}
	.log {
		margin: 4px 8px 8px 104px;
		max-height: 320px;
	}
</style>
