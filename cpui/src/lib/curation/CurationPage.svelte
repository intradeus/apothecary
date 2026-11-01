<script lang="ts">
	// The curation page (TODO 45): a gate over a review document — the
	// reviewer's notes before they are posted, a round's replies before they
	// are sent. The person picks what gets posted (everything, to start with:
	// unpicking is the deliberate act), rewords it, reads why the agent says
	// so, and approves — or sends the draft back with a word. The page
	// collects decisions over the document as the gate showed it; the engine
	// rebuilds the file from its raise-time snapshot and those.
	//
	// The frame is the review workbench's: the files on the left (the other
	// shown files — the threads beside the review — open read-only in the
	// middle), the items in the middle, the summary and the answer on the
	// right.
	import { errorMessage } from '$lib/core/errors';
	import { onMount, untrack } from 'svelte';
	import { api } from '$lib/api';
	import type { CurationView, Ping } from '$lib/domain';
	import AttachFiles from '$lib/artifacts/AttachFiles.svelte';
	import { curatable } from '$lib/core/ping';
	import { ReviewFiles } from '$lib/review/files.svelte';
	import ReviewLayout from '$lib/review/ReviewLayout.svelte';
	import FileList from '$lib/review/FileList.svelte';
	import FileLayoutSwitch from '$lib/app/FileLayoutSwitch.svelte';
	import FileView from '$lib/review/FileView.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import Checkbox from '$lib/ui/Checkbox.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import CurationItem from './CurationItem.svelte';
	import {
		blankKept,
		bulk,
		clearDraft,
		initialState,
		itemsOf,
		kindHue,
		kindLabel,
		loadDraft,
		saveDraft,
		tally,
		threadOf,
		toCuration,
		type Bulk,
		type CurationState
	} from './curation';

	let { ping, onanswered }: { ping: Ping; onanswered: () => void } = $props();

	// The route mounts this only for a curatable ping; the document is the
	// first shown file of a review type.
	const target = untrack(() => curatable(ping)) ?? { path: '', type: '' };
	const isReply = target.type === 'review_replies';

	// One ping per mount: the page remounts this for another.
	const set = new ReviewFiles(untrack(() => ping.id));
	let view = $state<CurationView | null>(null);
	let loadError = $state('');
	let loaded = $state(false);
	let cur = $state<CurationState>({ summary: '', items: {}, feedback: '' });
	let files = $state<File[]>([]);
	let busy = $state(false);
	let sending = $state('');
	let error = $state('');
	let savedAt = $state<number | null>(null);

	const doc = $derived(view?.documents.find((d) => d.path === target.path));
	const items = $derived(doc ? itemsOf(doc) : []);
	const threads = $derived(view?.threads?.threads ?? []);
	const dropped = $derived(view?.threads?.dropped ?? []);
	const usable = $derived(!!doc && doc.captured && !doc.error);
	const unusableWhy = $derived(
		loadError
			? loadError
			: doc?.error
				? t('cur.broken', { path: target.path }) + ' ' + doc.error
				: t('cur.notCaptured', { path: target.path })
	);

	onMount(async () => {
		await Promise.all([
			set.init(),
			api.pings
				.curation(ping.id)
				.then((v) => (view = v))
				.catch((e) => (loadError = errorMessage(e)))
		]);
		if (doc) cur = loadDraft(ping.id, initialState(doc, itemsOf(doc)));
		loaded = true;
	});

	function persist() {
		if (!loaded) return;
		saveDraft(ping.id, cur);
		savedAt = Date.now();
	}
	function pick(mode: Bulk) {
		cur = bulk(cur, items, mode);
		persist();
	}

	const tl = $derived(doc ? tally(doc, items, cur) : null);
	const allKept = $derived(
		items.length > 0 && items.every((it) => cur.items[it.key]?.keep !== false)
	);
	const someKept = $derived(items.some((it) => cur.items[it.key]?.keep !== false));
	const hasNits = $derived(items.some((it) => it.kind === 'nit'));
	const blanks = $derived(blankKept(items, cur));
	const curation = $derived(doc && usable ? toCuration(doc, items, cur) : undefined);
	const summaryReworded = $derived(
		!!doc?.review && cur.summary.trim() !== (doc.review.summary ?? '').trim()
	);
	const postsNothing = $derived(
		!!doc && !!tl && tl.kept === 0 && (!doc.review || !cur.summary.trim())
	);
	const feedback = $derived(cur.feedback.trim());
	// The main pane shows the items on the review itself; another shown file
	// (the threads) reads like any reviewed document.
	const file = $derived(set.file);
	const onDoc = $derived(!set.current || set.current === target.path);
	const counts = $derived({ [target.path]: items.length });

	async function submit(answer: 'approve' | 'reject') {
		sending = answer;
		busy = true;
		error = '';
		try {
			const up = files.length ? await api.files.upload(files) : [];
			await api.pings.answer(ping.id, {
				answer,
				feedback: feedback || undefined,
				attachmentIds: up.map((a) => a.id),
				curated: curation ? { [target.path]: curation } : undefined
			});
			clearDraft(ping.id);
			onanswered();
		} catch (e) {
			error = errorMessage(e);
		} finally {
			busy = false;
		}
	}

	const approveLabel = $derived(
		!usable
			? t('cur.approveAsWritten')
			: postsNothing
				? t('cur.approveNone')
				: isReply
					? t('cur.approveSend', { count: tl?.kept ?? 0 })
					: t('cur.approvePost', { count: tl?.kept ?? 0 })
	);
</script>

<ReviewLayout onstep={(d) => set.step(d)}>
	{#snippet nav()}
		<div class="pane-head">
			<h2>{t('doc.files')}</h2>
			{#if set.files.length > 1}<span class="spacer"></span><FileLayoutSwitch />{/if}
		</div>
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
	{/snippet}

	{#snippet main()}
		{#if !loaded}
			<div class="doc-scroll"><div class="paper"><Skeleton lines={8} /></div></div>
		{:else if onDoc}
			<div class="doc-scroll items">
				{#if set.files.length > 1}
					<FileList
						variant="select"
						files={set.files}
						current={set.current}
						{counts}
						viewed={set.viewed}
						onpick={(p) => void set.open(p)}
					/>
				{/if}
				{#if !usable}
					<ErrorCard cause={t('cur.loadFailed')} detail={unusableWhy} />
				{:else if items.length === 0}
					<p class="note">{t('cur.empty')}</p>
				{:else}
					<div class="bar">
						<Checkbox
							checked={allKept}
							indeterminate={!allKept && someKept}
							disabled={busy}
							label={isReply
								? t('cur.sentOf', { kept: tl?.kept ?? 0, total: items.length })
								: t('cur.postedOf', { kept: tl?.kept ?? 0, total: items.length })}
							onchange={(v) => pick(v ? 'all' : 'none')}
						/>
						<span class="spacer"></span>
						<button class="btn-ghost btn-sm" disabled={busy || allKept} onclick={() => pick('all')}
							>{t('cur.all')}</button
						>
						<button
							class="btn-ghost btn-sm"
							disabled={busy || !someKept}
							onclick={() => pick('none')}>{t('cur.none')}</button
						>
						{#if hasNits}
							<button class="btn-ghost btn-sm" disabled={busy} onclick={() => pick('dropNits')}
								>{t('cur.dropNits')}</button
							>
						{/if}
					</div>
					{#if postsNothing}
						<Callout hue="warning" glyph={I.warn}>{t('cur.noneWarn')}</Callout>
					{/if}
					{#each items as it (it.key)}
						{#if cur.items[it.key]}
							<CurationItem
								item={it}
								bind:state={cur.items[it.key]}
								thread={threadOf(threads, it.thread)}
								{isReply}
								disabled={busy}
								onchange={persist}
							/>
						{/if}
					{/each}
				{/if}
			</div>
		{:else if file}
			{#key file.path}
				<FileView
					{file}
					doc={set.docs.get(file.path)}
					comments={[]}
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
				</FileView>
			{/key}
		{:else}
			<div class="doc-scroll"><div class="paper"><ErrorCard cause={set.error} /></div></div>
		{/if}
	{/snippet}

	{#snippet side()}
		<div class="pane-head">
			<h2>{t('cur.title')}</h2>
			{#if tl}<span class="count" class:hot={tl.dropped > 0 || tl.reworded > 0}
					>{tl.kept}/{tl.total}</span
				>{/if}
		</div>
		<div class="side-scroll">
			<details class="card-in ask">
				<summary class="disclosure">{t('cur.theAsk')}</summary>
				<Markdown src={ping.question} quiet />
			</details>

			{#if usable && doc?.review}
				<div class="sum field">
					<span class="sum-head">
						<label class="caption" for="cur-summary">{t('cur.summary')}</label>
						{#if summaryReworded}
							<span class="pill sm tint-warning">{t('cur.reworded')}</span>
							<button
								class="linklike"
								disabled={busy}
								onclick={() => {
									cur.summary = doc?.review?.summary ?? '';
									persist();
								}}>{t('cur.restore')}</button
							>
						{/if}
					</span>
					<textarea
						id="cur-summary"
						bind:value={cur.summary}
						oninput={persist}
						rows="4"
						disabled={busy}
						placeholder={t('cur.summaryHelp')}></textarea>
					<span class="help">{t('cur.summaryHelp')}</span>
				</div>
			{/if}

			{#if dropped.length}
				<details class="card-in drp">
					<summary class="disclosure">{t('cur.droppedBefore')}</summary>
					<ul>
						{#each dropped as n, i (i)}
							<li>
								<span class="pill sm tint-{kindHue(n.kind)}">{kindLabel(n.kind)}</span>
								<span class="mono faint">{n.path}:{n.line}</span>
								<span class="truncate">{n.body}</span>
							</li>
						{/each}
					</ul>
				</details>
			{/if}
		</div>

		<footer class="pane-foot sf">
			<label class="fb">
				<span class="caption">{t('cur.feedback')}</span>
				<textarea
					bind:value={cur.feedback}
					oninput={persist}
					rows="2"
					disabled={busy}
					placeholder={t('cur.feedbackPlaceholder')}></textarea>
			</label>
			<AttachFiles bind:files label={t('doc.attachReview')} compact />
			{#if blanks.length}<div class="error">{t('cur.blanksWarn')}</div>{/if}
			{#if error}<div class="error">{error}</div>{/if}
			<div class="acts">
				<button
					class="btn-primary"
					disabled={busy || !loaded || blanks.length > 0}
					aria-busy={busy && sending === 'approve'}
					onclick={() => submit('approve')}>{approveLabel}</button
				>
				<button
					class="btn-sel"
					disabled={busy || !loaded || !feedback}
					aria-busy={busy && sending === 'reject'}
					title={feedback ? '' : t('cur.rejectNeeds')}
					onclick={() => submit('reject')}>{t('cur.requestChanges')}</button
				>
			</div>
			<p class="help faint cap">
				{#if savedAt}
					<span class="hue-success">{I.ok} {t('cur.draftSaved')}</span>{t('cur.draftKept')}
				{:else}
					{t('cur.draftsKept')}
				{/if}
			</p>
		</footer>
	{/snippet}
</ReviewLayout>

<style>
	.items {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 12px 16px 40px;
	}
	.bar {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 2px 2px 6px;
		font-size: var(--fs-label);
	}
	.note {
		margin: 0;
	}
	.ask,
	.drp {
		padding: 6px 10px;
		font-size: var(--fs-label);
		flex: none;
	}
	.ask summary,
	.drp summary {
		color: var(--text-muted);
		font-weight: 500;
	}
	.ask > :global(.md) {
		margin-top: 6px;
	}
	.drp ul {
		margin: 6px 0 0;
		padding: 0;
		list-style: none;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.drp li {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
	}
	.sum {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.sum-head {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.sum-head .caption {
		margin: 0;
	}
	.sum textarea {
		resize: vertical;
	}
	.sf {
		flex-direction: column;
		align-items: stretch;
		gap: 8px;
		padding: 10px 12px 11px;
	}
	.fb {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.acts {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
	.cap,
	.error {
		margin: 0;
	}
</style>
