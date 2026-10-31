<script lang="ts">
	// One shown file, large: a text file as its line-tracked blocks (each
	// comment's stretch tinted, the hovered one highlighted to the exact text
	// selected), an image to look at — fit to the width, a click for actual
	// size — and anything else as a download. Mounted once per file (the page
	// keys it on the path), so its blocks are always that file's; the scroll
	// position is kept per file across switches.
	//
	// A comment sent without its anchor (a submitted review) is found again by
	// its quote within its lines; failing that its lines carry it.
	import { tick, type Snippet } from 'svelte';
	import { api } from '$lib/api';
	import { fmtBytes } from '$lib/core/format';
	import {
		isMarkdown,
		isWholeFile,
		locate,
		rangeAt,
		textOffset,
		type CommentAnchor,
		type DraftComment
	} from '$lib/review/reviewDoc';
	import type { LoadedDoc, ShownFile } from './files.svelte';
	import DocHead from './DocHead.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';

	let {
		file,
		doc,
		comments,
		active = null,
		pending = null,
		raisedAt,
		reviewed = false,
		scrolls,
		picker,
		actions
	}: {
		file: ShownFile;
		/** The read text, once it is (text files). */
		doc?: LoadedDoc;
		/** The comments on this file. */
		comments: DraftComment[];
		/** The comment to highlight (hovered, or just jumped to). */
		active?: string | null;
		/** The selection a comment is being written on. */
		pending?: CommentAnchor | null;
		raisedAt: string;
		reviewed?: boolean;
		scrolls?: Map<string, number>;
		/** Above the head: the file picker of a narrow frame. */
		picker?: Snippet;
		/** In the head: what can be done with the file. */
		actions?: Snippet;
	} = $props();

	const blocks = $derived(doc?.blocks ?? []);
	const url = $derived(file.snapshotId ? api.files.snapshotUrl(file.snapshotId) : '');

	let scrollEl = $state<HTMLDivElement | null>(null);
	let paperEl = $state<HTMLDivElement | null>(null);
	// Rendered blocks by index, for anchoring and highlighting. Deliberately
	// not reactive: they are read on demand (selection, scroll, paint).
	const blockEls: HTMLElement[] = [];
	const block = (node: HTMLElement, i: number) => {
		blockEls[i] = node;
		return { update: (j: number) => void (blockEls[j] = node) };
	};

	// Back where this file was left, once its content is there.
	let restored = false;
	$effect(() => {
		if (restored || !scrollEl || (file.kind === 'text' && !doc)) return;
		restored = true;
		const top = scrolls?.get(file.path) ?? 0;
		void tick().then(() => scrollEl && (scrollEl.scrollTop = top));
	});
	const onscroll = () => {
		if (restored && scrollEl) scrolls?.set(file.path, scrollEl.scrollTop);
	};

	// ---- anchors --------------------------------------------------------------

	// The anchors found again for comments sent without one, by id.
	let located = $state<Record<string, CommentAnchor | null>>({});
	$effect(() => {
		if (!blocks.length) return;
		const texts = blocks.map((_, i) => blockEls[i]?.textContent ?? '');
		const found: Record<string, CommentAnchor | null> = {};
		for (const c of comments) {
			if (!c.anchor && c.quote && !isWholeFile(c)) found[c.id] = locate(texts, blocks, c);
		}
		located = found;
	});
	const anchorOf = (c: DraftComment) => c.anchor ?? located[c.id] ?? null;

	// Block-level span of a comment: its anchor when there is one, else its
	// line overlap. A whole-file comment spans no block.
	function spans(c: DraftComment | undefined, i: number): boolean {
		if (!c || isWholeFile(c)) return false;
		const a = anchorOf(c);
		if (a) return i >= a.startBlock && i <= a.endBlock;
		return blocks[i].endLine >= c.startLine && blocks[i].startLine <= c.endLine;
	}
	const blockOf = (c: DraftComment) =>
		anchorOf(c)?.startBlock ?? blocks.findIndex((b) => b.endLine >= c.startLine);

	const activeComment = $derived(comments.find((c) => c.id === active));
	const wholeActive = $derived(!!activeComment && isWholeFile(activeComment));

	// ---- selection → anchor (the page asks) ------------------------------------

	function blockIndexOf(node: Node): number {
		const el = node.nodeType === Node.ELEMENT_NODE ? (node as Element) : node.parentElement;
		const b = el?.closest<HTMLElement>('.block');
		return b?.dataset.idx === undefined ? -1 : Number(b.dataset.idx);
	}

	/** Whether a selection lies in this file's text. */
	export function holds(range: Range): boolean {
		return !!paperEl?.contains(range.commonAncestorContainer);
	}

	/** Where a selection sits: the blocks and offsets it covers, its lines and
	 *  section. Null outside the text. */
	export function anchorFrom(range: Range) {
		const sb = blockIndexOf(range.startContainer);
		let eb = blockIndexOf(range.endContainer);
		if (sb < 0 || eb < sb || !blockEls[sb] || !blockEls[eb]) return null;
		const startOffset = textOffset(blockEls[sb], range.startContainer, range.startOffset);
		let endOffset = textOffset(blockEls[eb], range.endContainer, range.endOffset);
		// A drag released just past a block lands on the head of the next one:
		// credit the text actually covered, not the untouched block below it.
		while (eb > sb && endOffset === 0) {
			eb -= 1;
			endOffset = blockEls[eb].textContent?.length ?? 0;
		}
		return {
			anchor: { startBlock: sb, startOffset, endBlock: eb, endOffset },
			heading: blocks[sb].heading,
			startLine: blocks[sb].startLine,
			endLine: blocks[eb].endLine
		};
	}

	/** Where a comment sits on screen (to open its composer by it). */
	export function rectOf(c: DraftComment): DOMRect | null {
		if (isWholeFile(c)) return paperEl?.getBoundingClientRect() ?? null;
		const a = anchorOf(c);
		return (
			(a ? rangesFor(a)[0]?.getBoundingClientRect() : null) ??
			blockEls[blockOf(c)]?.getBoundingClientRect() ??
			null
		);
	}

	/** Scroll a comment into view: its text, or the file's head. */
	export function reveal(c: DraftComment) {
		if (isWholeFile(c)) scrollEl?.scrollTo({ top: 0, behavior: 'smooth' });
		else blockEls[blockOf(c)]?.scrollIntoView({ behavior: 'smooth', block: 'center' });
	}

	/** Scroll to a block (the outline). */
	export function jump(i: number) {
		blockEls[i]?.scrollIntoView({ behavior: 'smooth', block: 'start' });
	}

	/** The block each line comment starts in (the outline's counts). */
	export const commentBlocks = () => comments.filter((c) => !isWholeFile(c)).map((c) => blockOf(c));

	// ---- highlighting -------------------------------------------------------

	const PENDING = 'apo-doc-pending';
	const ACTIVE = 'apo-doc-active';
	type HighlightRegistry = { set(k: string, v: object): void; delete(k: string): void };
	type HighlightCtor = new (...ranges: Range[]) => object;

	function registry(): HighlightRegistry | null {
		const g = globalThis as unknown as {
			CSS?: { highlights?: HighlightRegistry };
			Highlight?: HighlightCtor;
		};
		return g.CSS?.highlights && typeof g.Highlight === 'function' ? g.CSS.highlights : null;
	}

	// One Range per spanned block: the anchor's offsets are per-block, and a
	// Highlight happily holds several.
	function rangesFor(a: CommentAnchor): Range[] {
		const out: Range[] = [];
		for (let i = a.startBlock; i <= a.endBlock; i++) {
			const el = blockEls[i];
			if (!el) continue;
			const len = el.textContent?.length ?? 0;
			const from = i === a.startBlock ? a.startOffset : 0;
			const to = i === a.endBlock ? a.endOffset : len;
			const r = rangeAt(el, Math.min(from, len), Math.min(to, len));
			if (r) out.push(r);
		}
		return out;
	}

	function paint(name: string, anchor: CommentAnchor | null | undefined) {
		const reg = registry();
		if (!reg) return; // no Custom Highlight API — the block tint carries it
		reg.delete(name);
		const rs = anchor ? rangesFor(anchor) : [];
		if (!rs.length) return;
		const Ctor = (globalThis as unknown as { Highlight: HighlightCtor }).Highlight;
		reg.set(name, new Ctor(...rs));
	}

	$effect(() => {
		void blocks.length;
		void located;
		paint(PENDING, pending);
		paint(ACTIVE, activeComment && !isWholeFile(activeComment) ? anchorOf(activeComment) : null);
		return () => {
			const reg = registry();
			reg?.delete(PENDING);
			reg?.delete(ACTIVE);
		};
	});

	// ---- images ---------------------------------------------------------------

	let zoomed = $state(false);
</script>

{@render picker?.()}
<DocHead path={file.path} {raisedAt} {reviewed}>{@render actions?.()}</DocHead>
<div class="doc-scroll" bind:this={scrollEl} {onscroll}>
	{#if !file.captured || !file.snapshotId}
		<div class="paper" class:flash={wholeActive}>
			<ErrorCard cause={t('doc.notCaptured', { path: file.path })} />
		</div>
	{:else if file.kind === 'image'}
		<div class="pic" class:zoomed class:flash={wholeActive} bind:this={paperEl}>
			<button
				class="zoom"
				title={zoomed ? t('doc.zoomFit') : t('doc.zoomActual')}
				aria-label={zoomed ? t('doc.zoomFit') : t('doc.zoomActual')}
				aria-pressed={zoomed}
				onclick={() => (zoomed = !zoomed)}><img src={url} alt={file.path} /></button
			>
		</div>
	{:else if file.kind === 'other'}
		<div class="paper other" class:flash={wholeActive} bind:this={paperEl}>
			<span class="big" aria-hidden="true">{I.binary}</span>
			<p>{t('doc.notShown')}</p>
			<a class="btn" href={api.files.snapshotUrl(file.snapshotId, true)} download
				>{t('doc.download')}{file.size !== undefined ? ` · ${fmtBytes(file.size)}` : ''}</a
			>
		</div>
	{:else if !doc}
		<div class="paper"><Skeleton lines={10} /></div>
	{:else if doc.error}
		<div class="paper"><ErrorCard cause={doc.error} /></div>
	{:else if blocks.length === 0}
		<div class="paper faint" class:flash={wholeActive}>{t('doc.empty')}</div>
	{:else}
		<div
			class="paper"
			class:listing={!isMarkdown(file.path)}
			class:flash={wholeActive}
			bind:this={paperEl}
		>
			{#each blocks as b, i (i)}
				<div
					class="block"
					data-idx={i}
					use:block={i}
					class:commented={comments.some((c) => spans(c, i))}
					class:active={spans(activeComment, i)}
					class:pending={!!pending && i >= pending.startBlock && i <= pending.endBlock}
				>
					<Markdown src={b.raw} doc />
				</div>
			{/each}
		</div>
	{/if}
</div>

<style>
	.paper,
	.pic {
		transition: box-shadow var(--dur-fade) var(--ease-out);
	}
	/* A whole-file comment, hovered or jumped to: the file itself is lit. */
	.flash {
		box-shadow: 0 0 0 2px var(--brand-line);
	}
	.block {
		position: relative;
		border-radius: var(--r-tag);
		padding: 0 8px;
		margin: 0 -8px 2px;
		transition: background var(--dur-fade) var(--ease-out);
	}
	/* A file shown as written: its runs of lines read as one listing. */
	.listing .block {
		margin-bottom: 0;
	}
	.listing .block :global(pre) {
		margin: 0;
		border-radius: 0;
	}
	.listing .block:first-child :global(pre) {
		border-radius: var(--r-tag) var(--r-tag) 0 0;
	}
	.listing .block:last-child :global(pre) {
		border-radius: 0 0 var(--r-tag) var(--r-tag);
	}
	.block.commented {
		box-shadow: inset 3px 0 0 var(--brand);
	}
	.block.active,
	.block.pending {
		background: var(--brand-bg);
	}

	/* Exact-selection highlights. ::highlight() isn't attached to an element,
     so it can't be scoped — hence :global. Falls back to the block tint above
     where the Custom Highlight API is missing. */
	:global(::highlight(apo-doc-pending)) {
		background: color-mix(in oklch, var(--brand) 32%, transparent);
	}
	:global(::highlight(apo-doc-active)) {
		background: color-mix(in oklch, var(--brand) 26%, transparent);
	}

	/* An image: fit to the width; a click shows it at its own size. */
	.pic {
		border-radius: var(--r-card);
		width: fit-content;
		max-width: 100%;
		margin: 0 auto;
	}
	.pic.zoomed {
		max-width: none;
	}
	.zoom {
		display: block;
		padding: 0;
		border: 0;
		background: none;
		box-shadow: none;
		cursor: zoom-in;
		border-radius: var(--r-card);
	}
	/* A picture, not a control: no hover fill, no press sink. */
	.zoom:hover {
		background: none;
	}
	.zoom:active {
		transform: none;
	}
	.zoom img {
		display: block;
		max-width: 100%;
		height: auto;
		border-radius: var(--r-card);
		background: var(--fill-2);
	}
	.zoomed .zoom {
		cursor: zoom-out;
	}
	.zoomed .zoom img {
		max-width: none;
	}

	.other {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 10px;
		padding: 40px 20px;
		text-align: center;
		color: var(--text-muted);
	}
	.other p {
		margin: 0;
	}
	.big {
		font-size: var(--fs-mark);
		color: var(--text-faint);
	}

	@container review (max-width: 700px) {
		.doc-scroll {
			padding: 12px 14px 24px;
		}
	}
</style>
