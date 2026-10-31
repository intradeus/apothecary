<script lang="ts">
	// The head of a reviewed file (the review being written, the one
	// submitted): its workspace path, what can be done with it, and a caption
	// saying it is the raise-time snapshot, not the live file (DESIGN-FILES §9).
	import type { Snippet } from 'svelte';
	import { fmtAbs, fmtClock } from '$lib/core/format';
	import { t } from '$lib/i18n/index.svelte';

	let {
		path,
		raisedAt,
		reviewed = false,
		children
	}: {
		path: string;
		raisedAt: string;
		/** Answered: the document as it was reviewed. */
		reviewed?: boolean;
		/** Actions on the file, at the end of the head. */
		children?: Snippet;
	} = $props();
</script>

<div class="pane-head dh">
	<span class="mono muted truncate" title={path}>{path}</span>
	<span class="spacer"></span>
	<span class="cap" title={fmtAbs(raisedAt)}
		>{reviewed
			? t('inbox.snapshotReviewed', { time: fmtClock(raisedAt) })
			: t('inbox.snapshotLive', { time: fmtClock(raisedAt) })}</span
	>
	{@render children?.()}
</div>

<style>
	.dh {
		padding: 10px 16px;
		min-width: 0;
	}
	.cap {
		font-size: var(--fs-meta);
		color: var(--text-faint);
		white-space: nowrap;
	}
	@media (max-width: 760px) {
		.cap {
			display: none;
		}
	}
</style>
