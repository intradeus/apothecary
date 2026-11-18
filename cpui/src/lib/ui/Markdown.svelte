<script lang="ts">
	// Rendered markdown (agent output, goals, ping questions). Sanitising
	// lives in $lib/core/markdown; this is the only place {@html} meets it.
	import { mdToHtml } from '$lib/core/markdown';

	let {
		src,
		/** A document: display headings, for plans and captured files. */
		doc = false,
		/** Muted, smaller: context under a question. */
		quiet = false,
		/** File references become buttons carrying `data-path` (and
		 *  `data-line`/`data-end`); the parent handles their clicks
		 *  (`pathClick` in $lib/core/paths). */
		paths = false
	}: { src: string; doc?: boolean; quiet?: boolean; paths?: boolean } = $props();
</script>

<div class="md" class:doc class:quiet>{@html mdToHtml(src, { paths })}</div>

<style>
	.quiet {
		color: var(--text-muted);
		font-size: var(--fs-label);
	}
	/* A path reads as a link inside the prose, not as a button. */
	.md :global(.path-link) {
		display: inline;
		font: inherit;
		white-space: inherit;
		text-align: inherit;
	}
</style>
