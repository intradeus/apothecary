<script lang="ts">
	// One review thread on the merge request, as the engine read it when the
	// gate was raised: where it is, whose it is, its hunk, and its comments
	// oldest first — the ones newer than apothecary's last word marked new,
	// an earlier reply of apothecary's with what it said it did.
	import type { ReviewThread } from '$lib/domain';
	import { ago, fmtAbs } from '$lib/core/format';
	import Markdown from '$lib/ui/Markdown.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { kindHue, kindLabel, shortThread } from './curation';

	let { thread, compact = false }: { thread: ReviewThread; compact?: boolean } = $props();

	const where = $derived(
		thread.path
			? thread.line
				? `${thread.path}:${thread.line}`
				: thread.path
			: t('cur.conversation')
	);
</script>

<div class="th" class:compact>
	<div class="thh">
		<span class="mono truncate" title={where}>{where}</span>
		{#if thread.ours}<span class="pill sm tint-brand">{t('cur.thread.ours')}</span>{/if}
		{#if thread.kind}<span class="pill sm tint-{kindHue(thread.kind)}"
				>{kindLabel(thread.kind)}</span
			>{/if}
		<span class="pill sm {thread.resolved ? 'tint-success' : 'tint-warning'}"
			>{thread.resolved ? t('cur.thread.resolved') : t('cur.thread.open')}</span
		>
		<span class="spacer"></span>
		<span class="mono faint id" title={thread.id}>{shortThread(thread.id)}</span>
	</div>
	{#if thread.hunk}
		<details class="hunk">
			<summary class="disclosure">{t('cur.thread.hunk')}</summary>
			<pre class="log">{thread.hunk}</pre>
		</details>
	{/if}
	{#if thread.comments.length}
		<div class="cms">
			{#each thread.comments as c (c.id)}
				<div class="cm" class:new={c.new}>
					<div class="cmh">
						<b>{c.ours ? t('cur.thread.ours') : c.author}</b>
						{#if c.at}<span class="faint" title={fmtAbs(c.at)}>{ago(c.at)}</span>{/if}
						{#if c.did}<span class="pill sm tint-{kindHue(c.did)}">{kindLabel(c.did)}</span>{/if}
						{#if c.new}<span class="pill sm tint-warning">{t('cur.thread.new')}</span>{/if}
					</div>
					<Markdown src={c.body} quiet />
				</div>
			{/each}
		</div>
	{:else}
		<p class="help">{t('cur.thread.noComments')}</p>
	{/if}
</div>

<style>
	.th {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 6px 8px 8px 10px;
		border-left: 2px solid var(--line-strong);
		border-radius: 0 var(--r-tag) var(--r-tag) 0;
		background: var(--fill-2);
		min-width: 0;
	}
	.thh {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		font-size: var(--fs-label);
	}
	.thh .mono {
		font-size: var(--fs-id);
	}
	.id {
		font-size: var(--fs-meta);
	}
	.hunk {
		font-size: var(--fs-label);
	}
	.hunk summary {
		color: var(--text-muted);
	}
	.hunk pre {
		margin: 4px 0 0;
		max-height: 220px;
		overflow: auto;
		font-size: var(--fs-meta);
	}
	.cms {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.cm {
		padding: 2px 0 2px 8px;
		border-left: 2px solid transparent;
		font-size: var(--fs-ui);
	}
	.cm.new {
		border-left-color: var(--warning-line);
	}
	.cmh {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: var(--fs-label);
		margin-bottom: 2px;
	}
	.compact .hunk {
		display: none;
	}
	.help {
		margin: 0;
	}
</style>
