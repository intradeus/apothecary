<script lang="ts">
	// The open markdown file's headings, each with the comments under it;
	// a click scrolls the file there.
	import type { DocBlock } from '$lib/docreview';
	import { t } from '$lib/i18n/index.svelte';

	let {
		blocks,
		commentBlocks,
		onjump
	}: {
		blocks: DocBlock[];
		/** The block each of the file's line comments starts in. */
		commentBlocks: number[];
		onjump: (block: number) => void;
	} = $props();

	const outline = $derived(
		blocks
			.map((b, i) => {
				const m = /^(#{1,6})\s+(.+?)\s*#*\s*$/m.exec(b.raw.trim().split('\n')[0]);
				return m ? { i, level: m[1].length, text: m[2].replace(/[*_`]/g, '') } : null;
			})
			.filter((x): x is { i: number; level: number; text: string } => !!x)
	);
	const minLevel = $derived(Math.min(...outline.map((o) => o.level), 6));
	const commentsUnder = (blockIdx: number) => {
		const next = outline.find((o) => o.i > blockIdx)?.i ?? Infinity;
		return commentBlocks.filter((b) => b >= blockIdx && b < next).length;
	};
</script>

{#if outline.length}
	<div class="ol" role="navigation" aria-label={t('doc.outlineLabel')}>
		{#each outline as o (o.i)}
			{@const n = commentsUnder(o.i)}
			<button
				class="rowbtn oi"
				style:padding-left="{10 + (o.level - minLevel) * 12}px"
				onclick={() => onjump(o.i)}
				title={o.text}
				><span class="ell grow">{o.text}</span>{#if n}<span class="pill sm mono t-brand">{n}</span
					>{/if}</button
			>
		{/each}
	</div>
{:else}
	<div class="empty-line">{t('doc.noHeadings')}</div>
{/if}

<style>
	.ol {
		overflow: auto;
		padding: 6px;
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-height: 0;
	}
	.oi {
		padding-top: 4px;
		padding-bottom: 4px;
		font-size: var(--fs-label);
		color: var(--mut);
	}
	.oi:hover {
		color: var(--tx);
	}
</style>
