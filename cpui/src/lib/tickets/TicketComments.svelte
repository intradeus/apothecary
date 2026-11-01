<script lang="ts">
	// A ticket's discussion, oldest first: the tracker's comments (yours too,
	// posted through it), yours on the internal tracker, and what runs wrote.
	// A new comment goes to the ticket's tracker.
	import { api } from '$lib/api';
	import type { CommentOrigin, TicketComment } from '$lib/domain';
	import { errorMessage } from '$lib/core/errors';
	import { ago, fmtAbs, shortId } from '$lib/core/format';
	import { to } from '$lib/core/urls';
	import { t } from '$lib/i18n/index.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';

	let {
		ticketId,
		syncedAt
	}: {
		ticketId: string;
		/** Reloads the thread when the ticket was synced again. */
		syncedAt?: string;
	} = $props();

	let comments = $state<TicketComment[] | null>(null);
	let error = $state('');
	let body = $state('');
	let posting = $state(false);

	const ORIGIN_TINT: Record<CommentOrigin, string> = {
		remote: 'tint-accent',
		user: 'tint-muted',
		apothecary: 'tint-running'
	};
	const originLabel = (o: CommentOrigin) =>
		({
			remote: t('tickets.comments.remote'),
			user: t('tickets.comments.user'),
			apothecary: t('tickets.comments.apothecary')
		})[o];

	$effect(() => {
		const id = ticketId;
		void syncedAt;
		api.tickets
			.comments(id)
			.then((c) => {
				if (id === ticketId) comments = c;
			})
			.catch((e) => {
				if (id === ticketId) error = errorMessage(e);
			});
	});

	async function post(e: SubmitEvent) {
		e.preventDefault();
		if (!body.trim()) return;
		posting = true;
		error = '';
		try {
			comments = await api.tickets.addComment(ticketId, body.trim());
			body = '';
		} catch (err) {
			error = errorMessage(err);
		} finally {
			posting = false;
		}
	}
</script>

<section class="glass pane" aria-label={t('tickets.comments.title')}>
	<div class="pane-head">
		<h2>{t('tickets.comments.title')}</h2>
		{#if comments}<span class="help">{comments.length}</span>{/if}
	</div>
	<div class="pane-body thread">
		{#if comments === null && !error}
			<p class="faint small">{t('tickets.comments.loading')}</p>
		{:else if comments?.length === 0}
			<p class="faint small">{t('tickets.comments.none')}</p>
		{/if}
		{#each comments ?? [] as c (c.id)}
			<article class="comment">
				<header class="meta">
					<b>{c.author || t('tickets.comments.you')}</b>
					<span class="pill sm flat {ORIGIN_TINT[c.origin]}">{originLabel(c.origin)}</span>
					{#if c.runId}<a class="mono small" href={to.run(c.runId)}>{shortId(c.runId)}</a>{/if}
					<span class="muted small" title={fmtAbs(c.createdAt)}>{ago(c.createdAt)}</span>
				</header>
				<Markdown src={c.bodyMd} />
			</article>
		{/each}
		<form class="add" onsubmit={post}>
			<textarea
				rows="3"
				bind:value={body}
				aria-label={t('tickets.comments.add')}
				placeholder={t('tickets.comments.placeholder')}
				disabled={posting}></textarea>
			<div class="acts">
				{#if error}<span class="error">{error}</span>{/if}
				<span class="spacer"></span>
				<button class="btn-sm" type="submit" disabled={!body.trim() || posting} aria-busy={posting}
					>{t('tickets.comments.post')}</button
				>
			</div>
		</form>
	</div>
</section>

<style>
	.thread {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.comment {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.comment + .comment {
		border-top: 1px solid var(--glass-line);
		padding-top: 10px;
	}
	.meta {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 8px;
	}
	.add {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.acts {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	p {
		margin: 0;
	}
</style>
