<script lang="ts">
	// The kill feed: what just happened on the board, newest first, one line
	// each, sliding in as they land. Read out politely to a screen reader.
	import type { Hue } from '$lib/design/hues';
	import { fmtTime } from '$lib/core/format';
	import { t } from '$lib/i18n/index.svelte';
	import type { FeedEntry, FeedKind } from './feed';

	let {
		entries,
		nameOf
	}: { entries: readonly FeedEntry[]; nameOf: (projectId: string) => string } = $props();

	const HUE: Record<FeedKind, Hue> = {
		deployed: 'running',
		stage: 'running',
		needs_you: 'attention',
		in_review: 'accent-2',
		completed: 'success',
		failed: 'danger',
		abandoned: 'warning',
		drone: 'accent'
	};

	function phrase(e: FeedEntry): string {
		switch (e.kind) {
			case 'stage':
				return t('swarm.feed.stage', { stage: e.detail ?? '' });
			case 'drone':
				return t('swarm.feed.drone', { title: e.detail ?? '' });
			default:
				return t(`swarm.feed.${e.kind}` as const);
		}
	}
</script>

<ol class="feed" aria-live="polite" aria-label={t('swarm.feed.label')}>
	{#each entries as e (e.id)}
		<li class="line tone-{HUE[e.kind]}">
			<time datetime={e.at}>{fmtTime(e.at)}</time>
			<span class="where">{nameOf(e.projectId)}</span>
			<span class="who">{e.title}</span>
			<span class="what">{phrase(e)}</span>
		</li>
	{:else}
		<li class="idle">{t('swarm.feed.idle')}</li>
	{/each}
</ol>

<style>
	.feed {
		display: flex;
		flex: 1;
		flex-direction: column;
		gap: 2px;
		margin: 0;
		padding: 0;
		list-style: none;
		font-family: var(--font-code);
		font-size: var(--fs-id);
		overflow: auto;
	}
	.line {
		display: grid;
		grid-template-columns: max-content max-content minmax(0, 1fr) max-content;
		align-items: baseline;
		gap: 8px;
		padding: 2px 8px;
		border-left: 2px solid var(--tone);
		background: linear-gradient(
			90deg,
			color-mix(in oklch, var(--tone) 12%, transparent),
			transparent
		);
		animation: feed-in var(--dur-spring) var(--spring) both;
	}
	time {
		color: var(--term-faint);
		font-variant-numeric: tabular-nums;
	}
	.where {
		color: var(--term-muted);
		font-variant-caps: all-small-caps;
		letter-spacing: 0.06em;
	}
	.who {
		overflow: hidden;
		color: var(--term-text);
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.what {
		color: var(--tone);
		font-weight: 600;
	}
	.idle {
		padding: 2px 8px;
		color: var(--term-faint);
	}

	@keyframes feed-in {
		from {
			opacity: 0;
			transform: translateX(-16px);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.line {
			animation: none;
		}
	}
</style>
