<script lang="ts">
	// The review workbench (DESIGN3 §3): every file a ping shows — a gate's
	// files and folders of them, an agent's ask_human about its own — on one
	// page, answered with ONE structured review: comments, each on one file
	// (on the text selected, or the whole file — the only kind an image
	// takes), a summary, attachments. The engine renders the review into
	// what the agent reads; this page only collects it.
	//
	// Gates answer approve/reject — either carrying the review; an ask_human
	// answers its declared option, or its text, with the review alongside.
	import { tick, untrack } from 'svelte';
	import { api } from '$lib/api';
	import type { FormContent, Ping } from '$lib/domain';
	import AttachFiles from '$lib/AttachFiles.svelte';
	import PingFormFields from '$lib/PingFormFields.svelte';
	import { fieldLabel, formContent, formDefaults, formMissing, formShape } from '$lib/ping';
	import {
		byPlace,
		clearDraft,
		commentId,
		perFile,
		commentToSend,
		isMarkdown,
		isWholeFile,
		loadDraft,
		saveDraft,
		type CommentAnchor,
		type DraftComment
	} from '$lib/docreview';
	import { ReviewFiles } from '$lib/review/files.svelte';
	import ReviewLayout from '$lib/review/ReviewLayout.svelte';
	import FileList from '$lib/review/FileList.svelte';
	import FileView from '$lib/review/FileView.svelte';
	import Outline from '$lib/review/Outline.svelte';
	import CommentList from '$lib/review/CommentList.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import { I } from '$lib/ui/icons';
	import { bump } from '$lib/ui/motion';
	import { onMount } from 'svelte';
	import { t, th } from '$lib/i18n/index.svelte';

	let { ping, onanswered }: { ping: Ping; onanswered: () => void } = $props();

	const isGate = $derived(ping.answerKind === 'yes_no');
	// A question's form: one free-text answer is the summary itself; one
	// option to pick is a button each; anything else its fields, drawn.
	const shape = $derived(formShape(ping.form));
	const lone = $derived(ping.form?.fields[0]);
	let values = $state(formDefaults(untrack(() => ping.form)));
	const missing = $derived(formMissing(ping.form, values));

	// One ping per mount: the page remounts this for another.
	const set = new ReviewFiles(untrack(() => ping.id));
	let comments = $state<DraftComment[]>([]);
	let summary = $state('');
	let error = $state('');
	let busy = $state(false);
	let files = $state<File[]>([]); // the review's own attachments
	let showAll = $state(false);
	let view = $state<FileView | null>(null);
	let loaded = $state(false); // the draft is read

	onMount(async () => {
		await set.init();
		// The draft is read once the files are known: an old one's comments
		// are on the primary, and a comment on a file no longer shown is gone.
		const d = loadDraft(ping.id, set.primary);
		comments = d.comments.filter((c) => set.paths.includes(c.path)).sort(byPlace(set.paths));
		summary = d.summary;
		loaded = true;
	});

	const file = $derived(set.file);
	const doc = $derived(set.docs.get(set.current));
	const here = $derived(comments.filter((c) => c.path === set.current));
	const counts = $derived(perFile(comments));

	function persist() {
		saveDraft(ping.id, { comments, summary });
	}

	// ---- the composer ---------------------------------------------------------

	// The composer pops up where a selection was released (or by the "Comment
	// on this file" button). It is deliberately sticky: only Add/Save or Close
	// dismiss it — clicking away (to scroll, to re-read the file, to pick
	// another) must never throw away what was typed.
	interface Composer {
		path: string;
		anchor: CommentAnchor | null;
		quote: string;
		heading: string;
		startLine: number; // 0: the whole file
		endLine: number;
		text: string;
		files: File[];
		editing: string | null; // the id of the comment being edited, else null
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
	const holdsWork = (c: Composer | null) =>
		!!c && (!!c.text.trim() || c.files.length > 0 || c.editing !== null);
	/** The comment hovered in the list, or the one just jumped to. */
	let hovered = $state<string | null>(null);
	let flashed = $state<string | null>(null);

	const POPUP_W = 340;
	function place(rect: { left: number; bottom: number }): { x: number; y: number } {
		return {
			x: Math.max(8, Math.min(rect.left, window.innerWidth - POPUP_W - 8)),
			y: Math.max(8, Math.min(rect.bottom + 8, window.innerHeight - 180))
		};
	}

	function captureSelection() {
		const sel = window.getSelection();
		if (!sel || sel.isCollapsed || sel.rangeCount === 0 || !view) return;
		const range = sel.getRangeAt(0);
		if (!view.holds(range)) return;
		const quote = sel.toString().trim();
		if (quote.length < 2) return;
		// Never clobber a composer that already holds work.
		if (holdsWork(composer)) return;
		const a = view.anchorFrom(range);
		if (!a) return;
		composer = {
			path: set.current,
			...a,
			quote,
			text: '',
			files: [],
			editing: null,
			...place(range.getBoundingClientRect())
		};
	}

	/** A comment on the whole file: the only kind an image takes. */
	function commentOnFile(e: MouseEvent) {
		if (holdsWork(composer)) return void composerEl?.focus();
		const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
		composer = {
			path: set.current,
			anchor: null,
			quote: '',
			heading: '',
			startLine: 0,
			endLine: 0,
			text: '',
			files: [],
			editing: null,
			...place({ left: r.right - POPUP_W, bottom: r.bottom })
		};
	}

	// Listeners live on the document (a drag can be released outside the file)
	// and are attached imperatively so the file stays a plain, non-interactive
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

	function commitComment() {
		if (!composer || !composer.text.trim()) return;
		const c: DraftComment = {
			id: composer.editing ?? commentId(),
			path: composer.path,
			quote: composer.quote,
			heading: composer.heading,
			startLine: composer.startLine,
			endLine: composer.endLine,
			text: composer.text.trim(),
			...(composer.anchor ? { anchor: composer.anchor } : {}),
			...(composer.files.length ? { files: composer.files } : {})
		};
		const i = comments.findIndex((x) => x.id === composer!.editing);
		if (i >= 0) comments[i] = c;
		else comments.push(c);
		comments.sort(byPlace(set.paths));
		persist();
		closeComposer();
	}

	function closeComposer() {
		composer = null;
		window.getSelection()?.removeAllRanges();
	}

	async function editComment(c: DraftComment) {
		await show(c.path);
		const rect = view?.rectOf(c);
		composer = {
			path: c.path,
			anchor: c.anchor ?? null,
			quote: c.quote,
			heading: c.heading,
			startLine: c.startLine,
			endLine: c.endLine,
			text: c.text,
			files: [...(c.files ?? [])],
			editing: c.id,
			...place(rect ?? { left: 40, bottom: 80 })
		};
	}

	async function removeComment(c: DraftComment) {
		if (
			!(await confirmDialog({
				title: t('doc.deleteTitle'),
				body: th('doc.deleteBody'),
				action: t('doc.deleteAction')
			}))
		)
			return;
		comments = comments.filter((x) => x.id !== c.id);
		persist();
		if (hovered === c.id) hovered = null;
		if (composer?.editing === c.id) closeComposer();
	}

	// Esc is the popover's own (it closes through onclose).
	function onComposerKey(e: KeyboardEvent) {
		if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
			e.preventDefault();
			commitComment();
		}
	}

	const trunc = (s: string, n: number) => (s.length > n ? s.slice(0, n) + '…' : s);

	// ---- moving around ----------------------------------------------------------

	/** Open a file and wait for it to be on screen. */
	async function show(path: string) {
		if (path !== set.current) {
			await set.open(path);
			await tick();
		}
	}

	/** Open a comment's file, bring the comment into view and light it. */
	let flashTimer: ReturnType<typeof setTimeout> | undefined;
	async function openComment(c: DraftComment) {
		await show(c.path);
		await tick();
		view?.reveal(c);
		flashed = c.id;
		clearTimeout(flashTimer);
		flashTimer = setTimeout(() => (flashed = null), 1400);
	}

	// ---- submit -------------------------------------------------------------

	/** Which action is in flight, so only its button shows busy. */
	let sending = $state('');
	const said = $derived(comments.length > 0 || summary.trim() !== '');

	async function submit(
		answer: string,
		feedback = summary.trim(),
		content?: FormContent,
		key = answer
	) {
		sending = key;
		busy = true;
		error = '';
		try {
			// Per-comment attachments go up with the review's own, in one upload;
			// each comment then names its own among the answer's.
			const perComment = comments.map((c) => c.files ?? []);
			const all = [...perComment.flat(), ...files];
			const up = all.length ? await api.files.upload(all) : [];
			let k = 0;
			const review = comments.map((c, i) =>
				commentToSend(
					c,
					perComment[i].map(() => up[k++].id)
				)
			);
			await api.pings.answer(
				ping.id,
				answer,
				feedback || undefined,
				up.map((a) => a.id),
				review,
				content
			);
			clearDraft(ping.id);
			onanswered();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
		}
	}

	// A free-text question: the summary is the answer itself.
	const sendText = () => lone && submit('accept', '', { [lone.id]: summary.trim() }, 'accept');
	const sendForm = () => submit('accept', summary.trim(), formContent(ping.form, values));

	// "Draft saved" flashes after each change to the draft.
	let savedAt = $state<number | null>(null);
	$effect(() => {
		void comments.length;
		void comments.map((c) => c.text).join();
		void summary;
		if (loaded) savedAt = Date.now();
	});

	const emptyHint = $derived(
		file?.kind === 'image'
			? th('doc.hintImage')
			: file?.kind === 'other' || !file?.captured
				? th('doc.hintWhole')
				: th('doc.hint')
	);
</script>

<ReviewLayout onstep={(d) => set.step(d)}>
	{#snippet nav()}
		<div class="pane-head"><h2>{t('doc.files')}</h2></div>
		{#if set.loading}
			<div class="pane-body"><Skeleton lines={3} height={10} /></div>
		{:else}
			<FileList
				files={set.files}
				current={set.current}
				{counts}
				viewed={set.viewed}
				onpick={(p) => void set.open(p)}
			/>
		{/if}
		{#if file?.kind === 'text' && isMarkdown(file.path) && doc && !doc.error}
			<div class="pane-head sub"><h2>{t('doc.outline')}</h2></div>
			<Outline
				blocks={doc.blocks}
				commentBlocks={view?.commentBlocks() ?? []}
				onjump={(i) => view?.jump(i)}
			/>
		{/if}
	{/snippet}

	{#snippet main()}
		{#if set.loading}
			<div class="dscroll"><div class="paper"><Skeleton lines={10} /></div></div>
		{:else if !file}
			<div class="dscroll"><div class="paper"><ErrorCard cause={set.error} /></div></div>
		{:else}
			{#key file.path}
				<FileView
					bind:this={view}
					{file}
					{doc}
					comments={here}
					active={hovered ?? flashed}
					pending={composer?.path === file.path ? composer.anchor : null}
					raisedAt={ping.raisedAt}
					scrolls={set.scrolls}
				>
					{#snippet picker()}
						<FileList
							variant="select"
							files={set.files}
							current={set.current}
							{counts}
							viewed={set.viewed}
							onpick={(p) => void set.open(p)}
						/>
					{/snippet}
					{#snippet actions()}
						<button class="btn-sm" onclick={commentOnFile}
							>{I.add}
							{file.kind === 'image' ? t('doc.commentImage') : t('doc.commentFile')}</button
						>
					{/snippet}
				</FileView>
			{/key}
		{/if}
	{/snippet}

	{#snippet side()}
		<div class="pane-head">
			<h2>{t('doc.review')}</h2>
			<span class="count hot" class:gone={!comments.length} use:bump={comments.length}
				>{comments.length}</span
			>
		</div>
		<div class="sscroll">
			<details class="card-in ask">
				<summary class="disclosure">{t('doc.theAsk')}</summary>
				<Markdown src={ping.question} quiet />
			</details>

			<CommentList
				{comments}
				files={set.files}
				current={set.current}
				bind:all={showAll}
				bind:hovered
				onopen={openComment}
				onedit={editComment}
				onremove={removeComment}
			>
				{#snippet empty()}
					<p class="note">{@html emptyHint}</p>
				{/snippet}
			</CommentList>
		</div>

		<footer class="pane-foot sf">
			{#if !isGate && shape === 'fields' && ping.form}
				<PingFormFields form={ping.form} bind:values disabled={busy} />
			{/if}
			<label class="sum">
				<span class="lbl">{t('doc.summary')}</span>
				<textarea
					aria-label={!isGate && shape === 'text'
						? t('doc.answerPlaceholder')
						: t('doc.summaryPlaceholder')}
					bind:value={summary}
					oninput={persist}
					rows="2"
					placeholder={!isGate && shape === 'text'
						? t('doc.answerPlaceholder')
						: t('doc.summaryPlaceholder')}></textarea>
			</label>
			<AttachFiles bind:files label={t('doc.attachReview')} compact />
			{#if error}<div class="err">{error}</div>{/if}
			{#if isGate}
				<div class="acts">
					<button
						class="btn-primary"
						disabled={busy}
						aria-busy={busy && sending === 'approve'}
						onclick={() => submit('approve')}
						>{said ? t('doc.approveWith') : t('doc.approve')}</button
					>
					<button
						class="btn-sel"
						disabled={busy || !said}
						aria-busy={busy && sending === 'reject'}
						title={said ? '' : t('doc.rejectNeeds')}
						onclick={() => submit('reject')}
					>
						{t('doc.requestChanges')}
					</button>
				</div>
			{:else if shape === 'pick' && lone}
				<div class="acts">
					{#each lone.options as opt, i (opt.id)}
						<button
							class={i === 0 ? 'btn-primary' : ''}
							disabled={busy}
							aria-busy={busy && sending === opt.id}
							title={opt.detail ?? ''}
							onclick={() => submit('accept', summary.trim(), { [lone.id]: opt.id }, opt.id)}
							>{opt.label}</button
						>
					{/each}
					{@render decline()}
				</div>
				<p class="help fnt cap">{t('doc.pickHint')}</p>
			{:else if shape === 'text'}
				<div class="acts">
					<button
						class="btn-primary"
						disabled={busy || !summary.trim()}
						aria-busy={busy && sending === 'accept'}
						onclick={sendText}
					>
						{t('doc.sendReview')}
					</button>
					{@render decline()}
				</div>
			{:else}
				<div class="acts">
					<button
						class="btn-primary"
						disabled={busy || missing.length > 0}
						aria-busy={busy && sending === 'accept'}
						title={missing.length
							? t('ping.missing', { fields: missing.map(fieldLabel).join(', ') })
							: ''}
						onclick={sendForm}
					>
						{t('doc.sendReview')}
					</button>
					{@render decline()}
				</div>
			{/if}
			<p class="help fnt cap">
				{#if said && savedAt}
					<span class="hue-grn">{I.ok} {t('doc.draftSaved')}</span>{t('doc.draftKept')}
				{:else}
					{t('doc.draftsKept')}
				{/if}
			</p>
		</footer>
	{/snippet}
</ReviewLayout>

{#snippet decline()}
	<button
		class="btn-ghost"
		disabled={busy || !summary.trim()}
		aria-busy={busy && sending === 'decline'}
		title={summary.trim() ? t('ping.declineTitle') : t('doc.declineNeeds')}
		onclick={() => submit('decline')}>{t('doc.decline')}</button
	>
{/snippet}

<!-- The floating composer: sticky (only Add/Save, Close or Esc dismiss it, so
     clicking away to re-read the file never throws away what was typed), and
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
			aria-label={composer.editing !== null ? t('doc.editComment') : t('doc.addComment')}
			onkeydown={onComposerKey}
		>
			<div class="cmh">
				{#if isWholeFile(composer)}
					<span class="ell mono" title={composer.path}>{composer.path.split('/').pop()}</span>
					<span class="fnt">{t('doc.wholeFile')}</span>
				{:else}
					<span class="ell">{composer.heading ? `§ ${composer.heading}` : ''}</span>
					<span class="mono fnt">L{composer.startLine}–{composer.endLine}</span>
				{/if}
			</div>
			{#if composer.quote}<blockquote class="mono">{trunc(composer.quote, 180)}</blockquote>{/if}
			<textarea
				aria-label={t('doc.commentPlaceholder')}
				bind:this={composerEl}
				bind:value={composer.text}
				rows="3"
				placeholder={t('doc.commentPlaceholder')}></textarea>
			<AttachFiles bind:files={composer.files} label={t('doc.attach')} compact />
			<div class="cmf">
				<button class="btn-primary" onclick={commitComment} disabled={!composer.text.trim()}>
					{composer.editing !== null ? t('doc.save') : t('doc.add')}
				</button>
				<button class="btn-ghost" onclick={closeComposer}>{t('doc.close')}</button>
				<span class="spacer"></span>
				<span class="help"
					><span class="kbd">⌘↵</span>
					{t('doc.addHint')} · <span class="kbd">esc</span>
					{t('doc.closeHint')}</span
				>
			</div>
		</div>
	{/if}
</Popover>

<style>
	.gone {
		display: none;
	}
	.sub {
		border-top: 1px solid var(--ln);
	}
	.dscroll {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: 16px 22px 40px;
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
	.note {
		margin: 0;
	}
	.sf {
		flex-direction: column;
		align-items: stretch;
		gap: 8px;
		padding: 10px 12px 11px;
	}
	.sum {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.acts {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
	.cap {
		margin: 0;
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

	@container review (max-width: 700px) {
		.sscroll {
			overflow: visible;
		}
	}
</style>
