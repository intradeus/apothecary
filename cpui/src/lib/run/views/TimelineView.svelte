<script lang="ts">
	// Timeline (DESIGN §6): every lifecycle event, with the log of a finished
	// attempt and captured files one click away.
	import { errorMessage } from '$lib/core/errors';
	import { t } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import { fmtAbs, fmtTime } from '$lib/core/format';
	import { I } from '$lib/design/icons';
	import Loading from '$lib/ui/Loading.svelte';
	import StatusMark from '$lib/ui/StatusMark.svelte';
	import { TRANSCRIPT_TYPES, describe, hasLog } from '../derive';
	import type { RunStore } from '../store.svelte';

	let { rs }: { rs: RunStore } = $props();

	const rows = $derived(
		rs.events.filter((e) => !TRANSCRIPT_TYPES.has(e.type)).map((e) => ({ e, d: describe(e) }))
	);

	let logSeq = $state<number | null>(null);
	let logText = $state('');

	async function showLog(seq: number, stage: string) {
		if (logSeq === seq) {
			logSeq = null;
			return;
		}
		logSeq = seq;
		logText = t('runview.timeline.loading_log');
		try {
			const log = await api.runs.stageLog(rs.runId, stage);
			if (logSeq === seq) logText = log || t('runview.timeline.empty_log');
		} catch (e) {
			if (logSeq !== seq) return;
			logText = t('runview.timeline.no_log', {
				message: errorMessage(e)
			});
		}
	}
</script>

<div class="view tl">
	{#if rows.length === 0}
		<Loading text={t('runview.connecting')} />
	{/if}
	{#each rows as { e, d } (e.seq)}
		<div class="r">
			<span class="t mono faint" title={fmtAbs(e.ts)}>{fmtTime(e.ts)}</span>
			<StatusMark tone={d.tone || 'muted'} />
			<span class="x">{d.text}</span>
			<span class="a">
				{#if hasLog(e, d.stage, rs.events, rs.terminal)}
					<button
						class="linklike brand"
						aria-expanded={logSeq === e.seq}
						onclick={() => showLog(e.seq, d.stage!)}
						>{logSeq === e.seq ? t('runview.timeline.hide_log') : t('runview.timeline.log')}</button
					>
				{/if}
				{#if d.snapshot}
					<a class="brand" href={api.files.snapshotUrl(d.snapshot)} target="_blank" rel="noreferrer"
						>{t('runview.timeline.open')} {I.external}</a
					>
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
		padding: 10px 12px;
	}
	.r {
		flex: none;
		display: grid;
		grid-template-columns: 66px 22px minmax(0, 1fr) auto;
		gap: 8px;
		padding: 5px 8px;
		align-items: center;
		border-radius: var(--r-sm);
	}
	.r:hover {
		background: var(--fill-2);
	}
	.t {
		font-size: var(--fs-id);
	}
	.x {
		font-size: var(--fs-ui);
		overflow-wrap: anywhere;
	}
	.a {
		display: flex;
		gap: 10px;
		font-size: var(--fs-help);
		font-weight: 500;
		white-space: nowrap;
	}
	.log {
		flex: none;
		margin: 4px 8px 8px 104px;
		max-height: 320px;
	}
</style>
