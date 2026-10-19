<script lang="ts">
	// Rendered markdown (agent output, goals, ping questions). Sanitising
	// lives in $lib/markdown; this is the only place {@html} meets it.
	import { mdToHtml } from '$lib/markdown';

	let {
		src,
		/** A document: serif headings, for plans and captured files. */
		doc = false,
		/** Muted, smaller: context under a question. */
		quiet = false
	}: { src: string; doc?: boolean; quiet?: boolean } = $props();
</script>

<div class="md" class:doc class:quiet>{@html mdToHtml(src)}</div>

<style>
	.quiet {
		color: var(--mut);
		font-size: var(--fs-label);
	}
</style>
