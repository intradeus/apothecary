<script lang="ts">
	// A review's comments: the open file's, or — toggled — every file's,
	// grouped by path in the order the files are shown. Each says where it
	// is (its section and lines, or the whole file), what it quotes, what it
	// says and what it attaches; a click opens its file and lights its text.
	// With onedit/onremove the comments are a draft's; without, a submitted
	// review's, read-only.
	import { flip } from 'svelte/animate';
	import type { Snippet } from 'svelte';
	import { api } from '$lib/api';
	import type { Attachment } from '$lib/domain';
	import { isWholeFile, type DraftComment } from '$lib/docreview';
	import type { ShownFile } from './files.svelte';
	import { KIND_ICON, baseName } from './FileList.svelte';
	import FileChip from '$lib/ui/FileChip.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import { I } from '$lib/ui/icons';
	import { materialize, ms, spring } from '$lib/ui/motion';
	import { t } from '$lib/i18n/index.svelte';

	let {
		comments,
		files,
		current,
		all = $bindable(false),
		hovered = $bindable(null),
		onopen,
		onedit,
		onremove,
		attachments,
		empty
	}: {
		/** Every file's comments, in review order. */
		comments: DraftComment[];
		files: ShownFile[];
		current: string;
		/** Every file's comments rather than the open one's. */
		all?: boolean;
		hovered?: string | null;
		onopen: (c: DraftComment) => void;
		onedit?: (c: DraftComment) => void;
		onremove?: (c: DraftComment) => void;
		/** A submitted review's attachments, by id. */
		attachments?: Map<string, Attachment>;
		/** What the open file's list says while it has none. */
		empty?: Snippet;
	} = $props();

	const kindOf = (path: string) => files.find((f) => f.path === path)?.kind ?? 'text';
	const groups = $derived(
		all
			? files
					.map((f) => ({ file: f, list: comments.filter((c) => c.path === f.path) }))
					.filter((g) => g.list.length)
			: [
					{
						file: files.find((f) => f.path === current),
						list: comments.filter((c) => c.path === current)
					}
				]
	);
	const where = (c: DraftComment) => {
		if (isWholeFile(c))
			return kindOf(c.path) === 'image' ? t('doc.wholeImage') : t('doc.wholeFile');
		const lines = c.endLine > c.startLine ? `L${c.startLine}–${c.endLine}` : `L${c.startLine}`;
		return c.heading ? `§ ${c.heading} · ${lines}` : lines;
	};

	// Comments rise into the list and leave the same way; the rest close the
	// gap on the spring.
	const move = { duration: ms('--dur-spring', 450), easing: spring };
	const trunc = (s: string, n: number) => (s.length > n ? s.slice(0, n) + '…' : s);
</script>

{#if files.length > 1}
	<Segmented
		size="sm"
		glass={false}
		label={t('doc.showComments')}
		options={[
			{ value: 'file', label: t('doc.thisFile') },
			{ value: 'all', label: t('doc.allFiles'), count: comments.length || undefined }
		]}
		value={all ? 'all' : 'file'}
		onchange={(v) => (all = v === 'all')}
	/>
{/if}

{#each groups as g (g.file?.path ?? '')}
	{#if all && g.file}
		<div class="gh" title={g.file.path}>
			<span aria-hidden="true">{KIND_ICON[g.file.kind]}</span>
			<span class="ell grow mono">{baseName(g.file.path)}</span>
			<span class="fnt">{g.list.length}</span>
		</div>
	{/if}
	<div class="cl" role="list">
		{#each g.list as c (c.id)}
			<div
				class="card-in comment"
				class:hot={hovered === c.id}
				role="listitem"
				onmouseenter={() => (hovered = c.id)}
				onmouseleave={() => (hovered = null)}
				animate:flip={move}
				transition:materialize={{ origin: 'top center', y: 6 }}
			>
				<div class="ch">
					<button
						class="rowbtn jump"
						title={c.path === current ? t('doc.scrollTo') : t('doc.openAt', { path: c.path })}
						onclick={() => onopen(c)}
					>
						<span class="ell" class:mono={!c.heading && !isWholeFile(c)}>{where(c)}</span>
					</button>
					{#if onedit}
						<button class="btn-ghost btn-sm" onclick={() => onedit(c)}>{t('doc.edit')}</button>
					{/if}
					{#if onremove}
						<button
							class="btn-icon"
							onclick={() => onremove(c)}
							aria-label={t('doc.deleteComment')}
							title={t('doc.deleteComment')}>{I.close}</button
						>
					{/if}
				</div>
				{#if c.quote}<blockquote class="mono">{trunc(c.quote, 120)}</blockquote>{/if}
				<div class="ct">{c.text}</div>
				{#if c.files?.length}
					<div class="help">{I.attach} {c.files.map((f) => f.name).join(', ')}</div>
				{/if}
				{#if c.attachmentIds?.length}
					<div class="chips">
						{#each c.attachmentIds as id (id)}
							{@const a = attachments?.get(id)}
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
			</div>
		{:else}
			{@render empty?.()}
		{/each}
	</div>
{:else}
	<p class="note">{t('doc.noCommentsAnywhere')}</p>
{/each}

<style>
	.gh {
		display: flex;
		align-items: center;
		gap: 7px;
		font-size: var(--fs-label);
		color: var(--mut);
		padding: 4px 2px 0;
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
</style>
