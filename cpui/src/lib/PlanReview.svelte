<script lang="ts">
	import { api } from '$lib/api';
	import type { Ping, PingFile } from '$lib/domain';
	import AttachFiles from '$lib/AttachFiles.svelte';
	import {
		byPosition,
		clearDraft,
		composeFeedback,
		loadDraft,
		planBlocks,
		rangeAt,
		saveDraft,
		textOffset,
		type CommentAnchor,
		type PlanBlock,
		type ReviewComment
	} from '$lib/planreview';
	import { fmtClock, fmtAbs } from '$lib/format';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import { mdToHtml } from '$lib/planreview';
	import { onMount } from 'svelte';

	let { ping, onanswered }: { ping: Ping; onanswered: () => void } = $props();

	// Gates answer approve/reject; ask_human artifact reviews (PLAN4 M16)
	// answer with the composed review text — or a declared option with the
	// review as its note.
	const isGate = $derived(ping.answerKind === 'decision');

	let blocks = $state<PlanBlock[]>([]);
	let path = $state('');
	let error = $state('');
	let loading = $state(true);
	let comments = $state<ReviewComment[]>([]);
	let busy = $state(false);
	let files = $state<File[]>([]); // the human's answer attachments
	let extraFiles = $state<PingFile[]>([]); // agent-attached extras, beyond the primary doc

	// The composer pops up where a selection was released. It is deliberately
	// sticky: only Add/Save or Close dismiss it — clicking away (to scroll, to
	// re-read the plan, to pick a file) must never throw away what was typed.
	interface Composer {
		anchor: CommentAnchor | null;
		quote: string;
		heading: string;
		startLine: number;
		endLine: number;
		text: string;
		files: File[];
		editing: number | null; // index of the comment being edited, else null
		x: number;
		y: number;
	}
	let composer = $state<Composer | null>(null);
	let composerEl = $state<HTMLTextAreaElement | null>(null);
	let hovered = $state<number | null>(null);
	let planEl = $state<HTMLDivElement | null>(null);
	const blockEls: HTMLElement[] = [];

	onMount(async () => {
		comments = loadDraft(ping.id);
		// ONE fetch: the ping's files, primary first, each with its raise-time
		// snapshot. The reviewed document's content comes from the snapshot —
		// what you approve is what was captured, not what the workspace holds
		// by the time you look (DESIGN-FILES §9).
		try {
			const { files: fs } = await api.pings.payload(ping.id);
			const primary = fs[0];
			if (!primary) {
				error = 'this ping has no attached files';
			} else if (!primary.captured || !primary.snapshotId) {
				error = `${primary.path} was not captured when the ping was raised`;
			} else {
				path = primary.path;
				const res = await fetch(api.files.snapshotUrl(primary.snapshotId));
				if (!res.ok) throw new Error(`document unavailable (${res.status})`);
				blocks = planBlocks(await res.text());
			}
			extraFiles = fs.slice(1);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	});

	// ---- selection → anchor -------------------------------------------------

	function blockIndexOf(node: Node): number {
		const el = node.nodeType === Node.ELEMENT_NODE ? (node as Element) : node.parentElement;
		const b = el?.closest<HTMLElement>('.block');
		return b?.dataset.idx === undefined ? -1 : Number(b.dataset.idx);
	}

	function anchorFrom(range: Range) {
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

	const POPUP_W = 340;
	function place(rect: { left: number; bottom: number }): { x: number; y: number } {
		return {
			x: Math.max(8, Math.min(rect.left, window.innerWidth - POPUP_W - 8)),
			y: Math.max(8, Math.min(rect.bottom + 8, window.innerHeight - 180))
		};
	}

	function captureSelection() {
		const sel = window.getSelection();
		if (!sel || sel.isCollapsed || sel.rangeCount === 0 || !planEl) return;
		const range = sel.getRangeAt(0);
		if (!planEl.contains(range.commonAncestorContainer)) return;
		const quote = sel.toString().trim();
		if (quote.length < 2) return;
		// Never clobber a composer that already holds work.
		if (composer && (composer.text.trim() || composer.files.length || composer.editing !== null))
			return;
		const a = anchorFrom(range);
		if (!a) return;
		composer = {
			...a,
			quote,
			text: '',
			files: [],
			editing: null,
			...place(range.getBoundingClientRect())
		};
	}

	// Listeners live on the document (a drag can be released outside the plan)
	// and are attached imperatively so the plan stays a plain, non-interactive
	// region as far as a11y is concerned.
	$effect(() => {
		const onUp = () => setTimeout(captureSelection, 0); // let the selection settle
		const onKeyUp = (e: KeyboardEvent) => {
			if (e.shiftKey || e.key === 'Shift') captureSelection();
		};
		document.addEventListener('mouseup', onUp);
		document.addEventListener('touchend', onUp);
		document.addEventListener('keyup', onKeyUp);
		return () => {
			document.removeEventListener('mouseup', onUp);
			document.removeEventListener('touchend', onUp);
			document.removeEventListener('keyup', onKeyUp);
		};
	});

	$effect(() => {
		if (composer) composerEl?.focus();
	});

	// ---- highlighting -------------------------------------------------------

	const PENDING = 'apo-plan-pending';
	const ACTIVE = 'apo-plan-active';
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
		void composer; // repaint on open/close/edit, not on every keystroke
		void hovered;
		void blocks.length;
		void comments.length;
		paint(PENDING, composer?.anchor);
		paint(ACTIVE, hovered === null ? null : comments[hovered]?.anchor);
		return () => {
			const reg = registry();
			reg?.delete(PENDING);
			reg?.delete(ACTIVE);
		};
	});

	// Block-level span of a comment: the anchor when we have one, else the line
	// overlap (drafts saved before comments were anchored to exact text).
	function spans(c: ReviewComment | undefined, i: number): boolean {
		if (!c) return false;
		if (c.anchor) return i >= c.anchor.startBlock && i <= c.anchor.endBlock;
		return blocks[i].endLine >= c.startLine && blocks[i].startLine <= c.endLine;
	}
	const blockOf = (c: ReviewComment) =>
		c.anchor?.startBlock ?? blocks.findIndex((b) => b.endLine >= c.startLine);

	// ---- comments -----------------------------------------------------------

	function commitComment() {
		if (!composer || !composer.text.trim()) return;
		const c: ReviewComment = {
			quote: composer.quote,
			heading: composer.heading,
			startLine: composer.startLine,
			endLine: composer.endLine,
			text: composer.text.trim(),
			...(composer.anchor ? { anchor: composer.anchor } : {}),
			...(composer.files.length ? { files: composer.files } : {})
		};
		if (composer.editing !== null) comments[composer.editing] = c;
		else comments.push(c);
		comments.sort(byPosition);
		saveDraft(ping.id, comments);
		closeComposer();
	}

	function closeComposer() {
		composer = null;
		window.getSelection()?.removeAllRanges();
	}

	function editComment(i: number) {
		const c = comments[i];
		const rect =
			(c.anchor ? rangesFor(c.anchor)[0]?.getBoundingClientRect() : null) ??
			blockEls[blockOf(c)]?.getBoundingClientRect();
		composer = {
			anchor: c.anchor ?? null,
			quote: c.quote,
			heading: c.heading,
			startLine: c.startLine,
			endLine: c.endLine,
			text: c.text,
			files: [...(c.files ?? [])],
			editing: i,
			...place(rect ?? { left: 40, bottom: 80 })
		};
	}

	async function removeComment(i: number) {
		const c = comments[i];
		if (
			!(await confirmDialog({
				title: 'Delete this comment?',
				body: 'The comment is removed from your <b>review draft</b>. Nothing has been sent to the run yet.',
				action: 'Delete comment'
			}))
		)
			return;
		i = comments.indexOf(c);
		if (i < 0) return;
		comments.splice(i, 1);
		saveDraft(ping.id, comments);
		if (hovered === i) hovered = null;
		if (composer?.editing === i) closeComposer();
		else if (composer && composer.editing !== null && composer.editing > i) composer.editing -= 1;
	}

	function reveal(c: ReviewComment) {
		blockEls[blockOf(c)]?.scrollIntoView({ behavior: 'smooth', block: 'center' });
	}

	function onComposerKey(e: KeyboardEvent) {
		if (e.key === 'Escape') {
			e.preventDefault();
			closeComposer();
		} else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
			e.preventDefault();
			commitComment();
		}
	}

	const trunc = (s: string, n: number) => (s.length > n ? s.slice(0, n) + '…' : s);

	// ---- submit -------------------------------------------------------------

	const feedback = () =>
		composeFeedback(
			comments,
			files.map((f) => f.name)
		);

	async function submit(answer: string, text?: string) {
		busy = true;
		error = '';
		try {
			// Per-comment attachments ride along with the review-level ones — all
			// land in the workspace, and the feedback prompt names them per comment.
			const all = [...comments.flatMap((c) => c.files ?? []), ...files];
			const ids = all.length ? (await api.files.upload(all)).map((a) => a.id) : undefined;
			await api.pings.answer(ping.id, answer, text, ids);
			clearDraft(ping.id);
			onanswered();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
		}
	}

	async function requestChanges() {
		if (comments.length === 0) return;
		await submit('reject', feedback());
	}

	async function sendReview() {
		if (comments.length === 0) return;
		await submit(feedback());
	}

	async function chooseOption(opt: string) {
		await submit(opt, comments.length > 0 ? feedback() : undefined);
	}

	async function approve() {
		if (
			comments.length > 0 &&
			!(await confirmDialog({
				title: 'Approve and discard your comments?',
				body: `You have <b>${comments.length} unsent comment${comments.length === 1 ? '' : 's'}</b>. Approving lets the <b>run</b> continue as is and discards them. To send them, use Request changes instead.`,
				action: 'Approve anyway',
				tone: 'warn'
			}))
		)
			return;
		await submit('approve');
	}

	// ---- outline -------------------------------------------------------------

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
		return comments.filter((c) => {
			const b = blockOf(c);
			return b >= blockIdx && b < next;
		}).length;
	};
	const jump = (i: number) => blockEls[i]?.scrollIntoView({ behavior: 'smooth', block: 'start' });

	// "Draft saved" flashes after each change to the draft.
	let savedAt = $state<number | null>(null);
	$effect(() => {
		void comments.length;
		void comments.map((c) => c.text).join();
		if (!loading) savedAt = Date.now();
	});
</script>

<div class="wb">
	<!-- Outline -->
	<nav class="outline c" aria-label="Document outline">
		<div class="ph">Outline</div>
		{#if loading}
			<div class="pad"><Skeleton lines={5} height={10} /></div>
		{:else if outline.length}
			<div class="ol">
				{#each outline as o (o.i)}
					{@const n = commentsUnder(o.i)}
					<button
						class="oi"
						style:padding-left="{10 + (o.level - minLevel) * 12}px"
						onclick={() => jump(o.i)}
						title={o.text}
						><span class="ell">{o.text}</span>{#if n}<span class="oc">{n}</span>{/if}</button
					>
				{/each}
			</div>
		{:else}
			<div class="pad fnt small">No headings in this document.</div>
		{/if}
	</nav>

	<!-- Document -->
	<section class="doc c">
		<div class="dh">
			<span class="mono ell" title={path || ping.primaryRef}>{path || ping.primaryRef}</span>
			<span class="spacer"></span>
			<span class="cap" title={fmtAbs(ping.raisedAt)}
				>snapshot from {fmtClock(ping.raisedAt)} · not the live file</span
			>
		</div>
		<div class="dscroll">
			{#if extraFiles.length}
				<div class="extras">
					<span class="fnt small">From the agent</span>
					{#each extraFiles as f (f.path)}
						{#if !f.captured || !f.snapshotId}
							<span class="xf missing" title="this file was not captured when the ping was raised"
								><span class="mono ell">{f.path}</span><span>not captured</span></span
							>
						{:else if f.mime?.startsWith('image/')}
							<a
								class="thumb"
								href={api.files.snapshotUrl(f.snapshotId)}
								target="_blank"
								rel="noreferrer"
								title={f.path}
							>
								<img src={api.files.snapshotUrl(f.snapshotId)} alt={f.path} />
							</a>
						{:else}
							<a
								class="xf"
								href={api.files.snapshotUrl(f.snapshotId)}
								target="_blank"
								rel="noreferrer"><span class="ico">▤</span><span class="mono ell">{f.path}</span></a
							>
						{/if}
					{/each}
				</div>
			{/if}

			{#if loading}
				<div class="paper"><Skeleton lines={10} /></div>
			{:else if error && blocks.length === 0}
				<div class="paper"><ErrorCard cause={error} /></div>
			{:else if blocks.length === 0}
				<div class="paper fnt">The document is empty.</div>
			{:else}
				<div class="paper" bind:this={planEl}>
					{#each blocks as b, i (i)}
						<div
							class="block"
							data-idx={i}
							bind:this={blockEls[i]}
							class:commented={comments.some((c) => spans(c, i))}
							class:active={hovered !== null && spans(comments[hovered], i)}
							class:pending={!!composer?.anchor &&
								i >= composer.anchor.startBlock &&
								i <= composer.anchor.endBlock}
						>
							<div class="md">{@html b.html}</div>
						</div>
					{/each}
				</div>
			{/if}
		</div>
	</section>

	<!-- Review sidebar -->
	<aside class="side lg">
		<div class="sh">
			<h2>Review</h2>
			{#if comments.length}<span class="count hot">{comments.length}</span>{/if}
		</div>
		<div class="sscroll">
			<details class="ask">
				<summary><span class="caretglyph">▸</span> The ask</summary>
				<div class="md small">{@html mdToHtml(ping.question)}</div>
			</details>

			<div class="cl" role="list">
				{#each comments as c, i (c)}
					<div
						class="comment"
						class:hot={hovered === i}
						role="listitem"
						onmouseenter={() => (hovered = i)}
						onmouseleave={() => (hovered = null)}
					>
						<div class="ch">
							<button class="jump ell" title="Scroll to this section" onclick={() => reveal(c)}>
								§ {c.heading || '(top)'} · <span class="mono">L{c.startLine}–{c.endLine}</span>
							</button>
							<button class="btn-ghost btn-sm" onclick={() => editComment(i)}>Edit</button>
							<button
								class="btn-icon"
								onclick={() => removeComment(i)}
								aria-label="Delete comment"
								title="Delete comment">×</button
							>
						</div>
						<blockquote class="mono">{trunc(c.quote, 120)}</blockquote>
						<div class="ct">{c.text}</div>
						{#if c.files?.length}
							<div class="cf">⎘ {c.files.map((f) => f.name).join(', ')}</div>
						{/if}
					</div>
				{:else}
					<p class="hint">
						<strong>Select any text</strong> in the document — a comment box opens where you let go. All
						comments go back together as one feedback prompt.
					</p>
				{/each}
			</div>

			<AttachFiles bind:files label="Attach files to your review" compact />
			{#if error && blocks.length > 0}<div class="err">{error}</div>{/if}
		</div>

		<footer class="sf">
			{#if isGate}
				<div class="acts">
					<button class="btn-primary" disabled={busy} onclick={approve}>Approve</button>
					<button class="btn-sel" disabled={busy || comments.length === 0} onclick={requestChanges}>
						Request changes ({comments.length})
					</button>
				</div>
			{:else if ping.answerKind === 'choice' && ping.options.length}
				<div class="acts">
					{#each ping.options as opt, i (opt.id)}
						<button
							class={i === 0 ? 'btn-primary' : ''}
							disabled={busy}
							title={opt.detail ?? ''}
							onclick={() => chooseOption(opt.id)}>{opt.label}</button
						>
					{/each}
				</div>
				<p class="cap">Your pick answers the agent; comments ride along as the note.</p>
			{:else}
				<div class="acts">
					<button class="btn-primary" disabled={busy || comments.length === 0} onclick={sendReview}>
						Send review ({comments.length})
					</button>
				</div>
			{/if}
			<p class="cap">
				{#if comments.length && savedAt}
					<span class="saved">✓ Draft saved</span> · kept in this browser; attachments must be re-picked
				{:else}
					Drafts are kept in this browser; attachments must be re-picked.
				{/if}
			</p>
		</footer>
	</aside>
</div>

{#if composer}
	<div
		class="composer lg-pop"
		style="left:{composer.x}px; top:{composer.y}px"
		role="dialog"
		aria-label={composer.editing !== null ? 'Edit comment' : 'Add comment'}
		tabindex="-1"
		onkeydown={onComposerKey}
	>
		<div class="cmh">
			<span class="ell">§ {composer.heading || '(top)'}</span>
			<span class="mono fnt">L{composer.startLine}–{composer.endLine}</span>
		</div>
		<blockquote class="mono">{trunc(composer.quote, 180)}</blockquote>
		<textarea
			bind:this={composerEl}
			bind:value={composer.text}
			rows="3"
			placeholder="What should change here?"></textarea>
		<AttachFiles bind:files={composer.files} label="Attach" compact />
		<div class="cmf">
			<button class="btn-primary" onclick={commitComment} disabled={!composer.text.trim()}>
				{composer.editing !== null ? 'Save' : 'Add'}
			</button>
			<button class="btn-ghost" onclick={closeComposer}>Close</button>
			<span class="spacer"></span>
			<span class="fnt keys">⌘↵ add · Esc close</span>
		</div>
	</div>
{/if}

<style>
	.wb {
		flex: 1;
		min-height: 0;
		display: grid;
		grid-template-columns: 220px minmax(0, 1fr) 320px;
		gap: 10px;
		padding: 2px 22px 16px;
	}
	.c,
	.lg {
		border-radius: 16px;
		min-height: 0;
		overflow: hidden;
		display: flex;
		flex-direction: column;
	}
	.pad {
		padding: 10px 14px;
	}
	.small {
		font-size: 12px;
	}
	.spacer {
		flex: 1;
	}

	/* outline */
	.ph {
		font-family: var(--serif);
		font-style: italic;
		font-size: 16px;
		padding: 11px 14px 6px;
		color: var(--mut);
	}
	.ol {
		overflow: auto;
		padding: 0 6px 10px;
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
	.oi {
		all: unset;
		box-sizing: border-box;
		cursor: pointer;
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 4px 8px 4px 10px;
		border-radius: 8px;
		font-size: 12px;
		color: var(--mut);
		min-width: 0;
	}
	.oi:hover {
		background: var(--p3);
		color: var(--tx);
	}
	.oi:focus-visible {
		outline: 2px solid var(--brand);
		outline-offset: -2px;
	}
	.oc {
		margin-left: auto;
		flex: none;
		font-family: var(--mono);
		font-size: 10px;
		padding: 0 6px;
		border-radius: 10px;
		background: var(--brandBg);
		color: var(--brand);
		border: 1px solid var(--brandLn);
	}

	/* document */
	.dh {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 10px 16px;
		border-bottom: 1px solid var(--cLn);
		min-width: 0;
	}
	.dh .mono {
		font-size: 11.5px;
		color: var(--mut);
	}
	.cap {
		font-size: 11px;
		color: var(--fnt);
		margin: 0;
	}
	.dh .cap {
		white-space: nowrap;
	}
	.dscroll {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: 16px 22px 40px;
	}
	.paper {
		max-width: 72ch;
		margin: 0 auto;
	}
	.extras {
		max-width: 72ch;
		margin: 0 auto 16px;
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
		align-items: center;
	}
	.thumb img {
		display: block;
		height: 56px;
		border-radius: 8px;
		border: 1px solid var(--ln);
	}
	.xf {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		max-width: 260px;
		padding: 3px 9px;
		border-radius: 9px;
		background: var(--p2);
		border: 1px solid var(--ln);
		font-size: 11.5px;
		color: var(--tx);
	}
	.xf.missing {
		background: transparent;
		border-style: dashed;
		border-color: var(--ln2);
		color: var(--fnt);
	}
	.block {
		position: relative;
		border-radius: 6px;
		padding: 0 8px;
		margin: 0 -8px 2px;
	}
	.block.commented {
		box-shadow: inset 3px 0 0 var(--brand);
	}
	.block.active,
	.block.pending {
		background: var(--brandBg);
	}
	.paper .md {
		font-size: 13.5px;
		line-height: 1.65;
	}
	.paper .md :global(h1),
	.paper .md :global(h2) {
		font-family: var(--serif);
		font-style: italic;
		font-weight: 400;
		font-size: 22px;
		margin: 18px 0 6px;
	}
	.paper .md :global(h3) {
		font-size: 14.5px;
		margin: 14px 0 6px;
	}
	.paper .md :global(pre) {
		overflow-x: auto;
	}

	/* Exact-selection highlights. ::highlight() isn't attached to an element,
     so it can't be scoped — hence :global. Falls back to the block tint above
     where the Custom Highlight API is missing. */
	:global(::highlight(apo-plan-pending)) {
		background: color-mix(in oklch, var(--brand) 32%, transparent);
	}
	:global(::highlight(apo-plan-active)) {
		background: color-mix(in oklch, var(--brand) 26%, transparent);
	}

	/* sidebar */
	.sh {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 11px 14px 8px;
	}
	.sh h2 {
		font-size: 19px;
	}
	.sscroll {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: 0 12px 12px;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.ask {
		border-radius: 12px;
		background: var(--p2);
		border: 1px solid var(--ln);
		padding: 6px 10px;
		font-size: 12px;
		flex: none;
	}
	.ask summary {
		color: var(--mut);
		font-weight: 500;
	}
	.ask .md {
		margin-top: 6px;
		font-size: 12.5px;
	}
	.cl {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.comment {
		border-radius: 12px;
		background: var(--p2);
		border: 1px solid var(--ln);
		padding: 7px 8px 9px 10px;
		display: flex;
		flex-direction: column;
		gap: 5px;
	}
	.comment.hot {
		border-color: var(--brandLn);
		background: var(--brandBg);
	}
	.ch {
		display: flex;
		align-items: center;
		gap: 2px;
		min-width: 0;
	}
	.jump {
		all: unset;
		cursor: pointer;
		flex: 1;
		font-size: 11.5px;
		color: var(--mut);
	}
	.jump:hover {
		color: var(--tx);
	}
	.jump .mono {
		font-size: 10.5px;
	}
	blockquote {
		white-space: pre-wrap;
		margin: 0;
		padding-left: 8px;
		border-left: 2px solid var(--brandLn);
		color: var(--mut);
		font-size: 11px;
		max-height: 5.5em;
		overflow: hidden;
	}
	.ct {
		white-space: pre-wrap;
		font-size: 12.5px;
	}
	.cf {
		font-size: 11px;
		color: var(--mut);
	}
	.hint {
		margin: 0;
		font-size: 12px;
		color: var(--mut);
		padding: 10px 12px;
		border: 1px dashed var(--ln2);
		border-radius: 12px;
	}
	.sf {
		border-top: 1px solid var(--lgLn);
		padding: 10px 12px 11px;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.acts {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
	.saved {
		color: var(--grn);
	}

	/* floating composer */
	.composer {
		position: fixed;
		z-index: 30;
		width: 340px;
		border-radius: 17px;
		padding: 10px 12px 12px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.cmh {
		display: flex;
		gap: 8px;
		font-size: 12px;
		font-weight: 600;
		align-items: baseline;
	}
	.cmh .mono {
		font-size: 10.5px;
		font-weight: 500;
	}
	.cmf {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.keys {
		font-size: 11px;
	}

	@media (max-width: 1100px) {
		.wb {
			grid-template-columns: minmax(0, 1fr) 300px;
		}
		.outline {
			display: none;
		}
	}
	@media (max-width: 760px) {
		.wb {
			grid-template-columns: minmax(0, 1fr);
			overflow: auto;
			padding: 2px 16px 16px;
		}
		.c,
		.lg {
			overflow: visible;
		}
		.dscroll {
			padding: 12px 14px 24px;
		}
	}
</style>
