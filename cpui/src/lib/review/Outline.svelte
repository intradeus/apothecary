<script lang="ts">
	// The open markdown file's headings, each with the comments under it;
	// a click scrolls the file there.
	import type { Token, Tokens } from 'marked';
	import { marked } from '$lib/core/markdown';
	import type { DocBlock } from '$lib/review/reviewDoc';
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

	/** A heading's words: its markup goes, a name's underscores stay. */
	const words = (toks: Token[]): string =>
		(toks as Tokens.Generic[])
			.map((k) => (k.tokens ? words(k.tokens) : (k.text ?? k.raw)))
			.join('');

	// Read as docBlocks reads the file, so whatever is a heading there is one here.
	const outline = $derived(
		blocks
			.map((b, i) => {
				const h = marked.lexer(b.raw)[0];
				return h?.type === 'heading' ? { i, level: h.depth, text: words(h.tokens ?? []) } : null;
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
				><span class="truncate grow">{o.text}</span>{#if n}<span class="pill sm mono tint-brand"
						>{n}</span
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
		color: var(--text-muted);
	}
	.oi:hover {
		color: var(--text);
	}
</style>
