<script lang="ts">
	import { flip } from 'svelte/animate';
	import { api } from '$lib/api';
	import type { Ping, PingFile } from '$lib/domain';
	import AttachFiles from '$lib/AttachFiles.svelte';
	import DocHead from '$lib/inbox/DocHead.svelte';
	import AgentFiles from '$lib/inbox/AgentFiles.svelte';
	import {
		byPosition,
		clearDraft,
		commentId,
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
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import { I } from '$lib/ui/icons';
	import { bump, materialize, ms, spring } from '$lib/ui/motion';
	import { onMount } from 'svelte';
	import { t, th } from '$lib/i18n/index.svelte';

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
	// What the popover shows: the open composer, and the last one while the
	// popover materialises away (so it never empties mid-exit). The same
	// object while open, so typing here is typing into the composer.
	let shownComposer = $state<Composer | null>(null);
	$effect(() => {
		if (composer) shownComposer = composer;
	});
	let composerEl = $state<HTMLTextAreaElement | null>(null);
	let hovered = $state<number | null>(null);
	let planEl = $state<HTMLDivElement | null>(null);
	// Rendered blocks by index, for anchoring and highlighting. Deliberately
	// not reactive: they are read on demand (selection, scroll, paint).
	const blockEls: HTMLElement[] = [];
	const block = (node: HTMLElement, i: number) => {
		blockEls[i] = node;
		return { update: (j: number) => void (blockEls[j] = node) };
	};

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
				error = t('plan.noFiles');
			} else if (!primary.captured || !primary.snapshotId) {
				error = t('plan.notCaptured', { path: primary.path });
			} else {
				path = primary.path;
				const res = await fetch(api.files.snapshotUrl(primary.snapshotId));
				if (!res.ok) throw new Error(t('plan.docUnavailable', { status: res.status }));
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
			id: (composer.editing !== null ? comments[composer.editing]?.id : undefined) ?? commentId(),
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
				title: t('plan.deleteTitle'),
				body: th('plan.deleteBody'),
				action: t('plan.deleteAction')
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

	// Esc is the popover's own (it closes through onclose).
	function onComposerKey(e: KeyboardEvent) {
		if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
			e.preventDefault();
			commitComment();
		}
	}

	// Comments rise into the list and leave the same way; the rest close the
	// gap on the spring.
	const move = { duration: ms('--dur-spring', 450), easing: spring };

	const trunc = (s: string, n: number) => (s.length > n ? s.slice(0, n) + '…' : s);

	// ---- submit -------------------------------------------------------------

	const feedback = () =>
		composeFeedback(
			comments,
			files.map((f) => f.name)
		);

	/** Which action is in flight, so only its button shows busy. */
	let sending = $state('');
	async function submit(answer: string, text?: string) {
		sending = answer;
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
				title: t('plan.approveTitle'),
				body: th('plan.approveBody', { count: comments.length }),
				action: t('plan.approveAnyway'),
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
	<nav class="outline c pane" aria-label={t('plan.outlineLabel')}>
		<div class="pane-head"><h2>{t('plan.outline')}</h2></div>
		{#if loading}
			<div class="pane-body"><Skeleton lines={5} height={10} /></div>
		{:else if outline.length}
			<div class="ol">
				{#each outline as o (o.i)}
					{@const n = commentsUnder(o.i)}
					<button
						class="rowbtn oi"
						style:padding-left="{10 + (o.level - minLevel) * 12}px"
						onclick={() => jump(o.i)}
						title={o.text}
						><span class="ell grow">{o.text}</span>{#if n}<span class="pill sm mono t-brand"
								>{n}</span
							>{/if}</button
					>
				{/each}
			</div>
		{:else}
			<div class="empty-line">{t('plan.noHeadings')}</div>
		{/if}
	</nav>

	<!-- Document -->
	<section class="doc c pane">
		<DocHead path={path || ping.primaryRef || ''} raisedAt={ping.raisedAt} />
		<div class="dscroll">
			{#if extraFiles.length}
				<div class="paper extras"><AgentFiles files={extraFiles} /></div>
			{/if}

			{#if loading}
				<div class="paper"><Skeleton lines={10} /></div>
			{:else if error && blocks.length === 0}
				<div class="paper"><ErrorCard cause={error} /></div>
			{:else if blocks.length === 0}
				<div class="paper fnt">{t('plan.empty')}</div>
			{:else}
				<div class="paper" bind:this={planEl}>
					{#each blocks as b, i (i)}
						<div
							class="block"
							data-idx={i}
							use:block={i}
							class:commented={comments.some((c) => spans(c, i))}
							class:active={hovered !== null && spans(comments[hovered], i)}
							class:pending={!!composer?.anchor &&
								i >= composer.anchor.startBlock &&
								i <= composer.anchor.endBlock}
						>
							<Markdown src={b.raw} doc />
						</div>
					{/each}
				</div>
			{/if}
		</div>
	</section>

	<!-- Review sidebar -->
	<aside class="side lg pane">
		<div class="pane-head">
			<h2>{t('plan.review')}</h2>
			<span class="count hot" class:gone={!comments.length} use:bump={comments.length}
				>{comments.length}</span
			>
		</div>
		<div class="sscroll">
			<details class="card-in ask">
				<summary class="disclosure">{t('plan.theAsk')}</summary>
				<Markdown src={ping.question} quiet />
			</details>

			<div class="cl" role="list">
				{#each comments as c, i (c.id)}
					<div
						class="card-in comment"
						class:hot={hovered === i}
						role="listitem"
						onmouseenter={() => (hovered = i)}
						onmouseleave={() => (hovered = null)}
						animate:flip={move}
						transition:materialize={{ origin: 'top center', y: 6 }}
					>
						<div class="ch">
							<button class="rowbtn jump" title={t('plan.scrollTo')} onclick={() => reveal(c)}>
								<span class="ell"
									>§ {c.heading || t('plan.top')} ·
									<span class="mono">L{c.startLine}–{c.endLine}</span></span
								>
							</button>
							<button class="btn-ghost btn-sm" onclick={() => editComment(i)}
								>{t('plan.edit')}</button
							>
							<button
								class="btn-icon"
								onclick={() => removeComment(i)}
								aria-label={t('plan.deleteComment')}
								title={t('plan.deleteComment')}>{I.close}</button
							>
						</div>
						<blockquote class="mono">{trunc(c.quote, 120)}</blockquote>
						<div class="ct">{c.text}</div>
						{#if c.files?.length}
							<div class="help">{I.attach} {c.files.map((f) => f.name).join(', ')}</div>
						{/if}
					</div>
				{:else}
					<p class="note">{@html th('plan.hint')}</p>
				{/each}
			</div>

			<AttachFiles bind:files label={t('plan.attachReview')} compact />
			{#if error && blocks.length > 0}<div class="err">{error}</div>{/if}
		</div>

		<footer class="pane-foot sf">
			{#if isGate}
				<div class="acts">
					<button
						class="btn-primary"
						disabled={busy}
						aria-busy={busy && sending === 'approve'}
						onclick={approve}>{t('plan.approve')}</button
					>
					<button
						class="btn-sel"
						disabled={busy || comments.length === 0}
						aria-busy={busy && sending === 'reject'}
						onclick={requestChanges}
					>
						{t('plan.requestChanges', { count: comments.length })}
					</button>
				</div>
			{:else if ping.answerKind === 'choice' && ping.options.length}
				<div class="acts">
					{#each ping.options as opt, i (opt.id)}
						<button
							class={i === 0 ? 'btn-primary' : ''}
							disabled={busy}
							aria-busy={busy && sending === opt.id}
							title={opt.detail ?? ''}
							onclick={() => chooseOption(opt.id)}>{opt.label}</button
						>
					{/each}
				</div>
				<p class="cap">{t('plan.pickHint')}</p>
			{:else}
				<div class="acts">
					<button
						class="btn-primary"
						disabled={busy || comments.length === 0}
						aria-busy={busy}
						onclick={sendReview}
					>
						{t('plan.sendReview', { count: comments.length })}
					</button>
				</div>
			{/if}
			<p class="cap">
				{#if comments.length && savedAt}
					<span class="hue-grn">{I.ok} {t('plan.draftSaved')}</span>{t('plan.draftKept')}
				{:else}
					{t('plan.draftsKept')}
				{/if}
			</p>
		</footer>
	</aside>
</div>

<!-- The floating composer: sticky (only Add/Save, Close or Esc dismiss it, so
     clicking away to re-read the plan never throws away what was typed), and
     it materialises where the selection was released. -->
<Popover
	open={composer !== null}
	sticky
	width={POPUP_W}
	at={shownComposer ? { left: shownComposer.x, top: shownComposer.y } : undefined}
	onclose={closeComposer}
>
	{#if shownComposer}
		{@const composer = shownComposer}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="composer"
			aria-label={composer.editing !== null ? t('plan.editComment') : t('plan.addComment')}
			onkeydown={onComposerKey}
		>
			<div class="cmh">
				<span class="ell">§ {composer.heading || t('plan.top')}</span>
				<span class="mono fnt">L{composer.startLine}–{composer.endLine}</span>
			</div>
			<blockquote class="mono">{trunc(composer.quote, 180)}</blockquote>
			<textarea
				bind:this={composerEl}
				bind:value={composer.text}
				rows="3"
				placeholder={t('plan.commentPlaceholder')}></textarea>
			<AttachFiles bind:files={composer.files} label={t('plan.attach')} compact />
			<div class="cmf">
				<button class="btn-primary" onclick={commitComment} disabled={!composer.text.trim()}>
					{composer.editing !== null ? t('plan.save') : t('plan.add')}
				</button>
				<button class="btn-ghost" onclick={closeComposer}>{t('plan.close')}</button>
				<span class="spacer"></span>
				<span class="help"
					><span class="kbd">⌘↵</span>
					{t('plan.addHint')} · <span class="kbd">esc</span>
					{t('plan.closeHint')}</span
				>
			</div>
		</div>
	{/if}
</Popover>

<style>
	.wb {
		flex: 1;
		min-height: 0;
		display: grid;
		grid-template-columns: 220px minmax(0, 1fr) var(--aside-w);
		gap: 10px;
		padding: 2px 22px 16px;
	}
	.outline,
	.doc,
	.side {
		min-height: 0;
		display: flex;
		flex-direction: column;
	}
	.gone {
		display: none;
	}

	/* outline */
	.ol {
		overflow: auto;
		padding: 6px;
		display: flex;
		flex-direction: column;
		gap: 1px;
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

	/* document */
	.dscroll {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: 16px 22px 40px;
	}
	.extras {
		margin-bottom: 16px;
	}
	.block {
		position: relative;
		border-radius: var(--r-tag);
		padding: 0 8px;
		margin: 0 -8px 2px;
		transition: background var(--dur-fade) var(--ease-out);
	}
	.block.commented {
		box-shadow: inset 3px 0 0 var(--brand);
	}
	.block.active,
	.block.pending {
		background: var(--brandBg);
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
	.sscroll {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: 12px;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.ask {
		padding: 6px 10px;
		font-size: var(--fs-label);
		flex: none;
	}
	.ask summary {
		color: var(--mut);
		font-weight: 500;
	}
	.ask > :global(.md) {
		margin-top: 6px;
	}
	.cl {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.comment {
		padding: 5px 6px 9px 10px;
		display: flex;
		flex-direction: column;
		gap: 5px;
		transition:
			background var(--dur-fade) var(--ease-out),
			border-color var(--dur-fade) var(--ease-out);
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
		flex: 1;
		padding: 3px 4px;
		font-size: var(--fs-help);
		color: var(--mut);
	}
	.jump:hover {
		color: var(--tx);
	}
	.jump .mono {
		font-size: var(--fs-id);
	}
	blockquote {
		white-space: pre-wrap;
		margin: 0;
		padding-left: 8px;
		border-left: 2px solid var(--brandLn);
		color: var(--mut);
		font-size: var(--fs-meta);
		max-height: 5.5em;
		overflow: hidden;
	}
	.ct {
		white-space: pre-wrap;
		font-size: var(--fs-ui);
	}
	.note {
		margin: 0;
	}
	.sf {
		flex-direction: column;
		align-items: stretch;
		gap: 6px;
		padding: 10px 12px 11px;
	}
	.acts {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
	.cap {
		font-size: var(--fs-meta);
		color: var(--fnt);
		margin: 0;
	}

	/* floating composer (inside the Popover's glass) */
	.composer {
		padding: 4px 6px 6px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.cmh {
		display: flex;
		gap: 8px;
		font-size: var(--fs-label);
		font-weight: 600;
		align-items: baseline;
	}
	.cmh .mono {
		font-size: var(--fs-id);
		font-weight: 500;
	}
	.cmf {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	@media (max-width: 1180px) {
		.wb {
			grid-template-columns: minmax(0, 1fr) var(--aside-w);
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
		.outline,
		.doc,
		.side {
			overflow: visible;
		}
		.dscroll {
			padding: 12px 14px 24px;
		}
	}
</style>
