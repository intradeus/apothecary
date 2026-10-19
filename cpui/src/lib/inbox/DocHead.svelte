<script lang="ts">
	// The head of a reviewed document (plan review, the answered review): its
	// workspace path, and a caption saying it is the raise-time snapshot, not
	// the live file (DESIGN-FILES §9).
	import { fmtAbs, fmtClock } from '$lib/format';
	import { t } from '$lib/i18n/index.svelte';

	let {
		path,
		raisedAt,
		reviewed = false
	}: {
		path: string;
		raisedAt: string;
		/** Answered: the document as it was reviewed. */
		reviewed?: boolean;
	} = $props();
</script>

<div class="pane-head dh">
	<span class="mono mut ell" title={path}>{path}</span>
	<span class="spacer"></span>
	<span class="cap" title={fmtAbs(raisedAt)}
		>{reviewed
			? t('inbox.snapshotReviewed', { time: fmtClock(raisedAt) })
			: t('inbox.snapshotLive', { time: fmtClock(raisedAt) })}</span
	>
</div>

<style>
	.dh {
		padding: 10px 16px;
		min-width: 0;
	}
	.cap {
		font-size: var(--fs-meta);
		color: var(--fnt);
		white-space: nowrap;
	}
	@media (max-width: 760px) {
		.cap {
			display: none;
		}
	}
</style>
