<script lang="ts">
	// A submitted review, read-only, in the workbench's own frame: the files
	// the ping showed, each as it was reviewed (its raise-time snapshot —
	// long after the workspace is gone, DESIGN-FILES §9), its comments where
	// they were made (their quote lit when it can be found again, their lines
	// otherwise; whole-file comments listed), the summary and what the review
	// attached.
	import { onMount, tick, untrack } from 'svelte';
	import { api } from '$lib/api';
	import type { Attachment, ReviewComment } from '$lib/domain';
	import { byPlace, commentAsShown, isMarkdown, perFile, type DraftComment } from '$lib/docreview';
	import { ReviewFiles } from './files.svelte';
	import ReviewLayout from './ReviewLayout.svelte';
	import FileList from './FileList.svelte';
	import FileView from './FileView.svelte';
	import Outline from './Outline.svelte';
	import CommentList from './CommentList.svelte';
	import FileChip from '$lib/ui/FileChip.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import { t } from '$lib/i18n/index.svelte';

	let {
		pingId,
		runId,
		review = [],
		summary,
		attachmentIds = [],
		attachments,
		raisedAt,
		embedded = false,
		keys = true
	}: {
		pingId: string;
		runId: string;
		review?: ReviewComment[];
		summary?: string;
		/** The answer's attachments. */
		attachmentIds?: string[];
		/** The run's attachments, when the caller holds them; else fetched. */
		attachments?: Attachment[];
		raisedAt: string;
		embedded?: boolean;
		/** j/k move between files (off where the keys belong to the page). */
		keys?: boolean;
	} = $props();

	// One ping per mount: its callers key it on the ping.
	const set = new ReviewFiles(untrack(() => pingId));
	let fetched = $state<Attachment[] | null>(null);
	let showAll = $state(false);
	let hovered = $state<string | null>(null);
	let flashed = $state<string | null>(null);
	let view = $state<FileView | null>(null);

	onMount(() => {
		void set.init();
		if (!attachments && attachmentIds.length)
			api.runs
				.files(runId)
				.then((f) => (fetched = f.attachments))
				.catch(() => (fetched = []));
	});

	const comments = $derived(review.map(commentAsShown).sort(byPlace(set.paths)));
	const byId = $derived(new Map((attachments ?? fetched ?? []).map((a) => [a.id, a])));
	// What the review attached as a whole: what no comment names.
	const reviewFiles = $derived(
		attachmentIds.filter((id) => !review.some((c) => c.attachmentIds.includes(id)))
	);
	const file = $derived(set.file);
	const doc = $derived(set.docs.get(set.current));
	const here = $derived(comments.filter((c) => c.path === set.current));
	const counts = $derived(perFile(comments));

	let flashTimer: ReturnType<typeof setTimeout> | undefined;
	async function openComment(c: DraftComment) {
		if (c.path !== set.current) {
			await set.open(c.path);
			await tick();
		}
		await tick();
		view?.reveal(c);
		flashed = c.id;
		clearTimeout(flashTimer);
		flashTimer = setTimeout(() => (flashed = null), 1400);
	}
</script>

<ReviewLayout {embedded} onstep={keys ? (d) => set.step(d) : undefined}>
	{#snippet nav()}
		<div class="pane-head"><h2>{t('doc.files')}</h2></div>
		{#if set.loading}
			<div class="pane-body"><Skeleton lines={3} height={10} /></div>
		{:else}
			<FileList files={set.files} current={set.current} {counts} onpick={(p) => void set.open(p)} />
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
					{raisedAt}
					reviewed
					scrolls={set.scrolls}
				>
					{#snippet picker()}
						<FileList
							variant="select"
							files={set.files}
							current={set.current}
							{counts}
							onpick={(p) => void set.open(p)}
						/>
					{/snippet}
				</FileView>
			{/key}
		{/if}
	{/snippet}

	{#snippet side()}
		<div class="pane-head">
			<h2>{t('doc.review')}</h2>
			{#if comments.length}<span class="count">{comments.length}</span>{/if}
		</div>
		<div class="sscroll">
			{#if summary}
				<div class="sum">
					<div class="lbl">{t('doc.summary')}</div>
					<Markdown src={summary} />
				</div>
			{/if}
			{#if reviewFiles.length}
				<div class="chips">
					{#each reviewFiles as id (id)}
						{@const a = byId.get(id)}
						{#if a}
							<FileChip
								name={a.filename}
								size={a.size}
								href={api.files.attachmentUrl(a.id)}
								thumb={a.mime.startsWith('image/') ? api.files.attachmentUrl(a.id) : undefined}
							/>
						{:else}
							<FileChip name={id} state="missing" reason={t('doc.attachmentGone')} />
						{/if}
					{/each}
				</div>
			{/if}
			{#if comments.length}
				<CommentList
					{comments}
					files={set.files}
					current={set.current}
					bind:all={showAll}
					bind:hovered
					onopen={openComment}
					attachments={byId}
				>
					{#snippet empty()}
						<p class="note">{t('doc.noCommentsHere')}</p>
					{/snippet}
				</CommentList>
			{:else if !summary}
				<p class="note">{t('doc.noComments')}</p>
			{/if}
		</div>
	{/snippet}
</ReviewLayout>

<style>
	.sub {
		border-top: 1px solid var(--ln);
	}
	.dscroll {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: 16px 22px 40px;
	}
	.sscroll {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: 12px;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.sum {
		border-left: 3px solid var(--brandLn);
		padding: 2px 0 2px 12px;
	}
	.sum .lbl {
		margin-bottom: 4px;
	}
	.note {
		margin: 0;
	}
	@container review (max-width: 700px) {
		.sscroll {
			overflow: visible;
		}
	}
</style>
